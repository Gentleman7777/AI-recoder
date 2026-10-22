package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-recorder/internal/testutil"
	"golang.org/x/crypto/bcrypt"
)

func authRequest(method, path, body string, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(method, "http://localhost:8080"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:45000"
	r.Header.Set("X-Echo-Request", "1")
	r.Header.Set("Origin", "http://localhost:8080")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

func TestAccessBoundary(t *testing.T) {
	a := &Auth{}
	h := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, path := range []string{"/api/recordings", "/api/stats", "/api/recordings/id/audio", "/api/recordings/id/result", "/api/future", "/api"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, authRequest("GET", path, "", nil))
		if w.Code != 401 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unprotected %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/", "/auth.js", "/health"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, authRequest("GET", path, "", nil))
		if w.Code != 204 {
			t.Fatalf("public shell %s: %d", path, w.Code)
		}
	}
	for _, method := range []string{"POST", "DELETE", "PUT"} {
		w := httptest.NewRecorder()
		r := authRequest(method, "/api/recordings/id", "", nil)
		r.Header.Del("X-Echo-Request")
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("missing CSRF header: %d", w.Code)
		}
	}
}

func TestBootstrapAndOriginGuards(t *testing.T) {
	for _, tc := range []struct {
		host, remote, origin, forwarded string
		local, write                    bool
	}{
		{"localhost:8080", "127.0.0.1:1", "http://localhost:8080", "", true, true},
		{"[::1]:8080", "[::1]:1", "http://[::1]:8080", "", true, true},
		{"echo.example", "127.0.0.1:1", "https://echo.example", "", false, true},
		{"localhost:8080", "192.168.1.2:1", "http://localhost:8080", "", false, true},
		{"localhost:8080", "127.0.0.1:1", "http://localhost:8080", "203.0.113.1", false, true},
		{"localhost:8080", "127.0.0.1:1", "https://evil.example", "", true, false},
		{"localhost:8080", "127.0.0.1:1", "http://localhost:9999", "", true, false},
		{"echo.example", "127.0.0.1:1", "http://echo.example", "", false, false},
		{"localhost:8080", "127.0.0.1:1", "null", "", true, false},
	} {
		r := authRequest("POST", "/api/auth/register", "", nil)
		r.Host = tc.host
		r.RemoteAddr = tc.remote
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Forwarded-For", tc.forwarded)
		if localSetup(r) != tc.local || sameOriginWrite(r) != tc.write {
			t.Errorf("guards for %+v: local=%v write=%v", tc, localSetup(r), sameOriginWrite(r))
		}
	}

}

func TestAttemptLimiterConcurrent(t *testing.T) {
	a := &Auth{}
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.allowAttempt() {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("allowed %d attempts", allowed.Load())
	}
	a.mu.Lock()
	for i := range a.attempts {
		a.attempts[i] = time.Now().Add(-2 * time.Minute)
	}
	a.mu.Unlock()
	if !a.allowAttempt() {
		t.Fatal("limiter did not expire")
	}
}

func TestSessionLifecycle(t *testing.T) {
	pool := testutil.Database(t)
	a := New(pool)
	h := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	call := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, authRequest(method, path, body, cookie))
		return w
	}
	credentials := `{"username":"Владелец","password":"test-only-secret-9284"}`
	w := call("POST", "/api/auth/register", credentials, nil)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure || cookie.Path != "/" {
		t.Fatal("unsafe local cookie")
	}
	var hash, storedToken []byte
	if err := pool.QueryRow(context.Background(), "SELECT password_hash FROM auth_users").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if string(hash) == "test-only-secret-9284" || bcrypt.CompareHashAndPassword(hash, []byte("test-only-secret-9284")) != nil {
		t.Fatal("password is not hashed correctly")
	}
	if err := pool.QueryRow(context.Background(), "SELECT token_hash FROM auth_user_sessions").Scan(&storedToken); err != nil {
		t.Fatal(err)
	}
	if string(storedToken) == cookie.Value || len(storedToken) != 32 {
		t.Fatal("raw session token stored")
	}
	if call("POST", "/api/auth/register", credentials, nil).Code != 409 {
		t.Fatal("duplicate account accepted")
	}
	if call("GET", "/api/recordings/id/audio", "", cookie).Code != 204 {
		t.Fatal("authenticated audio blocked")
	}
	if call("POST", "/api/auth/login", `{"username":"Владелец","password":"wrong"}`, nil).Code != 401 {
		t.Fatal("wrong password accepted")
	}
	w = call("GET", "/api/auth/status", "", cookie)
	var status struct {
		Authenticated bool   `json:"authenticated"`
		UserID        string `json:"user_id"`
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || !status.Authenticated || status.UserID == "" {
		t.Fatal("bad session status")
	}
	// A fresh middleware instance represents an application restart.
	restarted := New(pool).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	w = httptest.NewRecorder()
	restarted.ServeHTTP(w, authRequest("GET", "/api/recordings", "", cookie))
	if w.Code != 204 {
		t.Fatal("restart lost session")
	}
	w = call("POST", "/api/auth/login", credentials, cookie)
	if w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
	rotated := w.Result().Cookies()[0]
	if rotated.Value == cookie.Value || call("GET", "/api/recordings", "", cookie).Code != 401 {
		t.Fatal("login failed to rotate session")
	}
	if call("DELETE", "/api/recordings/id", "", rotated).Code != 204 {
		t.Fatal("authenticated mutation blocked")
	}
	r := authRequest("DELETE", "/api/recordings/id", "", rotated)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin deletion accepted")
	}
	if call("POST", "/api/auth/logout", "", rotated).Code != 200 || call("GET", "/api/recordings", "", rotated).Code != 401 {
		t.Fatal("logout did not revoke session")
	}
	w = call("POST", "/api/auth/login", credentials, nil)
	expired := w.Result().Cookies()[0]
	if _, err := pool.Exec(context.Background(), "UPDATE auth_user_sessions SET expires_at = NOW() - INTERVAL '1 second'"); err != nil {
		t.Fatal(err)
	}
	if call("GET", "/api/recordings", "", expired).Code != 401 {
		t.Fatal("expired session accepted")
	}
	// Public HTTPS hosts get a different, Secure cookie, even behind a local tunnel.
	r = authRequest("POST", "/api/auth/login", credentials, nil)
	r.Host = "echo.example"
	r.Header.Set("Origin", "https://echo.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("HTTPS login: %d", w.Code)
	}
	secure := w.Result().Cookies()[0]
	if !secure.Secure || secure.Name != "__Host-echo_session" || secure.Domain != "" {
		t.Fatal("unsafe public cookie")
	}
}
