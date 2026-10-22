package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ai-recorder/internal/auth"
	"ai-recorder/internal/model"
	"ai-recorder/internal/repository"
	"ai-recorder/internal/testutil"
	"github.com/google/uuid"
)

func TestProcessingAPI(t *testing.T) {
	pool := testutil.Database(t)
	repo := repository.NewRecordingRepository(pool)
	id := uuid.NewString()
	if err := repo.Create(context.Background(), model.Recording{ID: id, Filename: "test.wav", MimeType: "audio/wav"}, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(repo, 1<<20).RegisterRoutes(mux)
	api := auth.New(pool).Wrap(mux)
	register := httptest.NewRequest("POST", "http://localhost:8080/api/auth/register", strings.NewReader(`{"username":"Tester","password":"test-handler-password-9284"}`))
	register.RemoteAddr = "127.0.0.1:45000"
	register.Header.Set("X-Echo-Request", "1")
	registration := httptest.NewRecorder()
	api.ServeHTTP(registration, register)
	if registration.Code != 200 {
		t.Fatalf("register: %d", registration.Code)
	}
	cookie := registration.Result().Cookies()[0]
	call := func(method, path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(method, "http://localhost:8080"+path, nil)
		request.AddCookie(cookie)
		request.Header.Set("X-Echo-Request", "1")
		api.ServeHTTP(response, request)
		return response
	}
	post := func(path string) *httptest.ResponseRecorder {
		return call(http.MethodPost, path)
	}
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/recordings/not-an-id/process", http.StatusBadRequest},
		{"/api/recordings/" + id + "/process?mode=invalid", http.StatusBadRequest},
		{"/api/recordings/" + id + "/process?mode=summary", http.StatusConflict},
		{"/api/recordings/" + uuid.NewString() + "/process", http.StatusNotFound},
	} {
		if response := post(tc.path); response.Code != tc.code {
			t.Fatalf("%s: got %d, want %d", tc.path, response.Code, tc.code)
		}
	}
	var wg sync.WaitGroup
	var queued atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := post("/api/recordings/" + id + "/process")
			if response.Code != http.StatusAccepted {
				t.Errorf("enqueue status: %d", response.Code)
				return
			}
			var body struct {
				Queued bool `json:"queued"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Error(err)
			}
			if body.Queued {
				queued.Add(1)
			}
		}()
	}
	wg.Wait()
	if queued.Load() != 1 {
		t.Fatalf("API enqueued %d jobs", queued.Load())
	}
	job, err := repo.ClaimJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveTranscript(context.Background(), job, "Текст доступен до резюме"); err != nil {
		t.Fatal(err)
	}
	response := call(http.MethodGet, "/api/recordings/"+id+"/result")
	var result model.RecordingResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Transcript == "" || result.Recording.ProcessingStage != "summarizing" {
		t.Fatalf("partial result unavailable: %s", response.Body)
	}
}
