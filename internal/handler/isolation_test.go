package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-recorder/internal/auth"
	"ai-recorder/internal/model"
	"ai-recorder/internal/repository"
	"ai-recorder/internal/testutil"
)

func TestUserRecordingIsolation(t *testing.T) {
	pool := testutil.Database(t)
	repo := repository.NewRecordingRepository(pool)
	mux := http.NewServeMux()
	New(repo, 1<<20).RegisterRoutes(mux)
	api := auth.New(pool).Wrap(mux)
	call := func(method, path string, body io.Reader, cookie *http.Cookie, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://echo.example"+path, body)
		r.Header.Set("Origin", "https://echo.example")
		r.Header.Set("X-Echo-Request", "1")
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	register := func(name string) (*http.Cookie, string) {
		body := `{"username":"` + name + `","password":"test-user-password-9284"}`
		w := call("POST", "/api/auth/register", strings.NewReader(body), nil, "application/json")
		if w.Code != 200 {
			t.Fatalf("register %s: %d %s", name, w.Code, w.Body)
		}
		var data struct {
			ID string `json:"user_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || data.ID == "" {
			t.Fatal("missing user id")
		}
		return w.Result().Cookies()[0], data.ID
	}
	alice, aliceID := register("Alice")
	bob, bobID := register("Bob")
	// Usernames are unique without regard to case, even through public registration.
	if w := call("POST", "/api/auth/register", strings.NewReader(`{"username":"ALICE","password":"other-test-password"}`), nil, "application/json"); w.Code != 409 {
		t.Fatalf("duplicate username: %d", w.Code)
	}
	upload := func(cookie *http.Cookie, filename, forgedOwner string) string {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, _ := form.CreateFormFile("audio", filename)
		file.Write([]byte("private audio " + filename))
		form.WriteField("user_id", forgedOwner)
		form.Close()
		w := call("POST", "/api/recordings", &body, cookie, form.FormDataContentType())
		if w.Code != 201 {
			t.Fatalf("upload: %d %s", w.Code, w.Body)
		}
		var record model.Recording
		if err := json.Unmarshal(w.Body.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		return record.ID
	}
	audioA := upload(alice, "alice.wav", bobID)
	audioB := upload(bob, "bob.wav", aliceID)
	changedRequest := httptest.NewRequest("POST", "https://echo.example/api/recordings/"+audioA+"/process", nil)
	changedRequest.AddCookie(alice)
	changedRequest.Header.Set("X-Echo-Request", "1")
	changedRequest.Header.Set("X-Echo-Account", bobID)
	changedResponse := httptest.NewRecorder()
	api.ServeHTTP(changedResponse, changedRequest)
	if changedResponse.Code != 409 {
		t.Fatal("stale browser account was accepted")
	}
	var owner string
	if err := pool.QueryRow(context.Background(), "SELECT user_id::text FROM recordings WHERE id=$1", audioA).Scan(&owner); err != nil || owner != aliceID {
		t.Fatal("client-controlled recording owner")
	}
	// Workers operate across users; their results still inherit the recording owner.
	if _, err := repo.Enqueue(context.Background(), audioA, "full"); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.SaveRecognition(context.Background(), job, model.Transcription{Text: "Alice private transcript", Segments: []model.Segment{{Start: 0, End: 1, Text: "Alice private transcript", SpeakerID: "speaker_1"}}, DiarizationStatus: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteJob(context.Background(), job, "Alice private summary", []string{"private point"}, "Личные планы Алисы"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cookie     *http.Cookie
		own, other string
		completed  int64
	}{{alice, audioA, audioB, 1}, {bob, audioB, audioA, 0}} {
		w := call("GET", "/api/recordings", nil, tc.cookie, "")
		var list struct {
			Items []model.Recording `json:"items"`
		}
		if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 1 || list.Items[0].ID != tc.own {
			t.Fatalf("mixed library: %s", w.Body)
		}
		w = call("GET", "/api/stats", nil, tc.cookie, "")
		var stats repository.Stats
		if json.Unmarshal(w.Body.Bytes(), &stats) != nil || stats.Total != 1 || stats.Completed != tc.completed {
			t.Fatalf("mixed stats: %s", w.Body)
		}
		for _, suffix := range []string{"", "/audio", "/audio?download=1", "/result"} {
			if w := call("GET", "/api/recordings/"+tc.other+suffix, nil, tc.cookie, ""); w.Code != 404 {
				t.Fatalf("foreign GET %s: %d", suffix, w.Code)
			}
		}
		for _, mode := range []string{"full", "resume", "summary"} {
			if w := call("POST", "/api/recordings/"+tc.other+"/process?mode="+mode, nil, tc.cookie, ""); w.Code != 404 {
				t.Fatalf("foreign processing %s: %d", mode, w.Code)
			}
		}
		if w := call("DELETE", "/api/recordings/"+tc.other, nil, tc.cookie, ""); w.Code != 404 {
			t.Fatalf("foreign deletion: %d", w.Code)
		}
		if w := call("GET", "/api/recordings/"+tc.own+"/audio", nil, tc.cookie, ""); w.Code != 200 {
			t.Fatalf("own audio: %d", w.Code)
		}
	}
	w := call("GET", "/api/recordings/"+audioA+"/result", nil, alice, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Alice private transcript") || !strings.Contains(w.Body.String(), "Alice private summary") || !strings.Contains(w.Body.String(), "Личные планы Алисы") || !strings.Contains(w.Body.String(), `"speaker_id":"speaker_1"`) {
		t.Fatal("owner lost results")
	}
	rangeRequest := httptest.NewRequest("GET", "https://echo.example/api/recordings/"+audioA+"/audio", nil)
	rangeRequest.AddCookie(alice)
	rangeRequest.Header.Set("Range", "bytes=0-3")
	rangeResponse := httptest.NewRecorder()
	api.ServeHTTP(rangeResponse, rangeRequest)
	if rangeResponse.Code != 206 || rangeResponse.Body.String() != "priv" || rangeResponse.Header().Get("Content-Range") == "" {
		t.Fatalf("audio seeking failed: %d %s", rangeResponse.Code, rangeResponse.Body)
	}
	download := call("GET", "/api/recordings/"+audioA+"/audio?download=1", nil, alice, "")
	_, params, err := mime.ParseMediaType(download.Header().Get("Content-Disposition"))
	if err != nil || params["filename"] != "Личные планы Алисы.wav" {
		t.Fatalf("contextual download filename: %v %v", params, err)
	}
	if w := call("POST", "/api/recordings/"+audioB+"/process", nil, bob, ""); w.Code != 202 {
		t.Fatalf("own process: %d", w.Code)
	}
	job, err = repo.ClaimJob(context.Background())
	if err != nil || job.RecordingID != audioB {
		t.Fatal("cross-user queue broken")
	}
	if w := call("DELETE", "/api/recordings/"+audioA, nil, alice, ""); w.Code != 204 {
		t.Fatalf("own deletion: %d", w.Code)
	}
	if w := call("GET", "/api/recordings/"+audioB, nil, bob, ""); w.Code != 200 {
		t.Fatal("deletion affected other user")
	}
	// Routes fail closed even if authentication middleware is accidentally omitted.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/recordings", nil))
	if w.Code != 401 {
		t.Fatal("handler allowed an unscoped request")
	}
	if w := call("POST", "/api/auth/logout", nil, alice, ""); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w := call("GET", "/api/recordings", nil, bob, ""); w.Code != 200 {
		t.Fatal("logout revoked someone else's session")
	}
}
