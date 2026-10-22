package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ai-recorder/internal/auth"
	"ai-recorder/internal/config"
	"ai-recorder/internal/database"
	"ai-recorder/internal/handler"
	"ai-recorder/internal/repository"
	"ai-recorder/internal/service"
)

//go:embed web
var embeddedWeb embed.FS

func main() {
	cfg := config.Load()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect to PostgreSQL: %v", err)
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool); err != nil {
		log.Fatalf("database migrations: %v", err)
	}

	repo := repository.NewRecordingRepository(pool)
	processor := service.NewProcessor(repo, cfg)
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); processor.Run(workerCtx) }()
	api := handler.New(repo, cfg.MaxUploadBytes)

	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	registerWeb(mux)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           withCommonHeaders(auth.New(pool).Wrap(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("AI Recorder listening on http://localhost%s", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	cancelWorkers()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown: %v", err)
	}
	select {
	case <-workerDone:
	case <-shutdownCtx.Done():
		log.Print("worker shutdown timed out; pending jobs will resume after lease expiry")
	}
}

func registerWeb(mux *http.ServeMux) {
	webFS, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		log.Fatalf("embedded web assets: %v", err)
	}
	assets := http.FileServer(http.FS(webFS))
	indexHTML, err := fs.ReadFile(webFS, "index.html")
	if err != nil {
		log.Fatalf("embedded web index: %v", err)
	}

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Unknown file paths and unknown API endpoints are not rewritten as HTML.
		file, err := fs.Stat(webFS, path)
		if err != nil || file.IsDir() {
			if strings.HasPrefix(path, "api/") || strings.HasPrefix(path, "health") || strings.Contains(path, ".") {
				http.NotFound(w, r)
				return
			}
			path = "index.html"
		}
		if path == "index.html" || path == "sw.js" || path == "manifest.webmanifest" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if path == "index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(indexHTML)
			return
		}
		r.URL.Path = "/" + path
		assets.ServeHTTP(w, r)
	})
}

func withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(self), geolocation=()")
		next.ServeHTTP(w, r)
	})
}
