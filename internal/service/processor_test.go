package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-recorder/internal/config"
	"ai-recorder/internal/model"
	"ai-recorder/internal/repository"
	"ai-recorder/internal/testutil"
	"github.com/google/uuid"
)

func TestSummaryValidation(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		ok            bool
	}{
		{"valid", `{"title":"Планы на неделю","summary":"Итог","key_points":["Тезис"]}`, true},
		{"no points needed", `{"title":"Планы на неделю","summary":"Итог","key_points":[]}`, true},
		{"empty object", `{}`, false},
		{"missing title", `{"summary":"Итог","key_points":[]}`, false},
		{"blank title", `{"title":"  ","summary":"Итог","key_points":[]}`, false},
		{"empty text", `{"summary":"  ","key_points":[]}`, false},
		{"missing points", `{"summary":"Итог"}`, false},
		{"null points", `{"title":"Планы на неделю","summary":"Итог","key_points":null}`, false},
		{"invalid json", `some text`, false},
		{"invalid point", `{"title":"Планы на неделю","summary":"Итог","key_points":[" "]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Format    map[string]any `json:"format"`
					Think     *bool          `json:"think"`
					KeepAlive string         `json:"keep_alive"`
					Options   struct {
						MaxTokens int `json:"num_predict"`
					} `json:"options"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Format["type"] != "object" {
					t.Error("request did not include a JSON schema")
				}
				if request.Think == nil || *request.Think {
					t.Error("short-note summaries must explicitly disable hidden reasoning by default")
				}
				if request.Options.MaxTokens != 256 || request.KeepAlive != "5m" {
					t.Errorf("unexpected summary resource limits: %+v", request.Options)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": tc.content}})
			}))
			defer server.Close()
			p := NewProcessor(nil, config.Config{OllamaURL: server.URL, OllamaModel: "test"})
			_, _, _, err := p.summarize(context.Background(), "Текст разговора")
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v, error=%v", tc.ok, err)
			}
		})
	}
}

func TestSummaryTokenLimitAndModelOverride(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(map[bool]string{true: "truncation", false: "model override"}[truncated], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Think     bool   `json:"think"`
					KeepAlive string `json:"keep_alive"`
					Options   struct {
						MaxTokens int `json:"num_predict"`
					} `json:"options"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if !request.Think || request.KeepAlive != "0s" || request.Options.MaxTokens != 1024 {
					t.Error("explicit model configuration was not honored")
				}
				content, reason := `{"title":"Планы на неделю","summary":"Итог","key_points":[]}`, "stop"
				if truncated {
					content, reason = `{"summary":"Незавершённый текст`, "length"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"done_reason": reason, "message": map[string]string{"content": content}})
			}))
			defer server.Close()
			p := NewProcessor(nil, config.Config{OllamaURL: server.URL, OllamaModel: "test", OllamaThink: true, OllamaMaxTokens: 1024, OllamaKeepAlive: "0s"})
			_, _, _, err := p.summarize(context.Background(), "Проверочная расшифровка")
			if truncated {
				if err == nil || !strings.Contains(err.Error(), "OLLAMA_MAX_TOKENS") {
					t.Fatalf("unclear truncation error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTranscriptSurvivesOllamaFailureAndRetry(t *testing.T) {
	pool := testutil.Database(t)
	repo := repository.NewRecordingRepository(pool)
	ctx := context.Background()
	id := uuid.NewString()
	if err := repo.Create(ctx, model.Recording{ID: id, Filename: "test.wav", MimeType: "audio/wav"}, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This is checked before Ollama responds: the text must already be durable.
		result, err := repo.GetResult(ctx, id)
		if err != nil || result.Transcript != "Проверочная расшифровка" || result.Recording.ProcessingStage != "summarizing" {
			t.Errorf("transcript not available while summarizing: %+v, %v", result, err)
		}
		if requests.Add(1) == 1 {
			http.Error(w, "model unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": `{"title":"Планы на неделю","summary":"Итог","key_points":["Тезис"]}`}})
	}))
	defer server.Close()
	p := NewProcessor(repo, config.Config{OllamaURL: server.URL, OllamaModel: "fixture"})
	var recognized atomic.Int32
	p.recognizeAudio = func(context.Context, string) (model.Transcription, error) {
		recognized.Add(1)
		return model.Transcription{Text: "Проверочная расшифровка", Segments: []model.Segment{{Start: 0, End: 1, Text: "Проверочная расшифровка", SpeakerID: "speaker_1"}}, DiarizationStatus: "completed"}, nil
	}
	if _, err := repo.Enqueue(ctx, id, "full"); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.runJob(ctx, job)
	result, err := repo.GetResult(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Recording.Status != "failed" || result.Transcript == "" || result.Summary != "" {
		t.Fatalf("failure discarded text or reported success: %+v", result)
	}
	if _, err := repo.Enqueue(ctx, id, "summary"); err != nil {
		t.Fatal(err)
	}
	job, err = repo.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.runJob(ctx, job)
	result, err = repo.GetResult(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Recording.Status != "completed" || result.Summary != "Итог" || result.Recording.Title != "Планы на неделю" || len(result.Segments) != 1 || result.Segments[0].SpeakerID != "speaker_1" || result.DiarizationStatus != "completed" || recognized.Load() != 1 {
		t.Fatalf("retry did not reuse transcript: %+v, recognitions=%d", result, recognized.Load())
	}
}

func TestWorkerShutdownRequeuesJob(t *testing.T) {
	pool := testutil.Database(t)
	repo := repository.NewRecordingRepository(pool)
	ctx := context.Background()
	id := uuid.NewString()
	if err := repo.Create(ctx, model.Recording{ID: id, Filename: "test.wav", MimeType: "audio/wav"}, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Enqueue(ctx, id, "full"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	p := NewProcessor(repo, config.Config{ProcessingWorkers: 1})
	p.recognizeAudio = func(ctx context.Context, _ string) (model.Transcription, error) {
		close(entered)
		<-ctx.Done()
		return model.Transcription{}, ctx.Err()
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); p.Run(workerCtx) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop")
	}
	job, err := repo.ClaimJob(ctx)
	if err != nil || job.RecordingID != id {
		t.Fatalf("shutdown did not release job: %+v, %v", job, err)
	}
}
