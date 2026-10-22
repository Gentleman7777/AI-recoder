package handler

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"testing"
	"time"

	"ai-recorder/internal/auth"
	"ai-recorder/internal/repository"
	"ai-recorder/internal/testutil"
)

// Opt-in browser fixture: real authentication and recording API, isolated database schema.
// ECHO_AUTH_PREVIEW=1 TEST_DATABASE_URL=... go test ./internal/handler -run TestBrowserPreview -v -timeout 15m
func TestBrowserPreview(t *testing.T) {
	if os.Getenv("ECHO_AUTH_PREVIEW") != "1" {
		t.Skip("opt-in browser fixture")
	}
	pool := testutil.Database(t)
	mux := http.NewServeMux()
	done := make(chan struct{})
	var once sync.Once
	// This route exists only in the opt-in fixture, and requires a real session.
	mux.HandleFunc("POST /api/preview/stop", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		once.Do(func() { close(done) })
	})
	New(repository.NewRecordingRepository(pool), 200*1024*1024).RegisterRoutes(mux)
	mux.HandleFunc("GET /storage-tests", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "../../tests/storage-tests.html") })
	mux.Handle("GET /", http.FileServer(http.Dir("../../web")))
	listener, err := net.Listen("tcp", "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: auth.New(pool).Wrap(mux), ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	t.Log("Isolated auth preview: http://localhost:18080/")
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	timer := time.NewTimer(12 * time.Minute)
	defer timer.Stop()
	select {
	case <-stop.Done():
	case <-timer.C:
	case <-done:
	}
}
