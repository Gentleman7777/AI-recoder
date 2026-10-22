package repository

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"ai-recorder/internal/database"
	"ai-recorder/internal/model"
	"ai-recorder/internal/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func recording(t *testing.T, r *RecordingRepository) string {
	t.Helper()
	id := uuid.NewString()
	if err := r.Create(context.Background(), model.Recording{ID: id, Filename: "test.wav", MimeType: "audio/wav"}, []byte("test audio")); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestQueueConcurrencyAndRecovery(t *testing.T) {
	pool := testutil.Database(t)
	r := NewRecordingRepository(pool)
	ctx := context.Background()
	id := recording(t, r)
	var wg sync.WaitGroup
	var enqueued atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := r.Enqueue(ctx, id, "full")
			if err != nil {
				t.Error(err)
			}
			if ok {
				enqueued.Add(1)
			}
		}()
	}
	wg.Wait()
	if enqueued.Load() != 1 {
		t.Fatalf("enqueued %d times", enqueued.Load())
	}
	var claimed atomic.Int32
	jobs := make(chan Job, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := r.ClaimJob(ctx)
			if err == nil {
				claimed.Add(1)
				jobs <- job
			} else if !errors.Is(err, pgx.ErrNoRows) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(jobs)
	if claimed.Load() != 1 {
		t.Fatalf("claimed %d times", claimed.Load())
	}
	old := <-jobs
	if err := r.RenewJob(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE processing_jobs SET lease_until=NOW()-INTERVAL '1 second' WHERE recording_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	// A fresh repository represents another process after a crash.
	restarted := NewRecordingRepository(pool)
	current, err := restarted.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Token == old.Token {
		t.Fatal("reclaimed job reused the previous owner token")
	}
	for _, err := range []error{r.RenewJob(ctx, old), r.SaveTranscript(ctx, old, "stale"), r.CompleteJob(ctx, old, "stale", []string{}), r.FinishJob(ctx, old, "stale", false)} {
		if !errors.Is(err, ErrJobLost) {
			t.Fatalf("stale worker write: %v", err)
		}
	}
	if err := r.SaveRecognition(ctx, old, model.Transcription{Text: "stale speaker text", Segments: []model.Segment{{Text: "stale", SpeakerID: "speaker_9"}}}); !errors.Is(err, ErrJobLost) {
		t.Fatalf("stale speaker write: %v", err)
	}
	if err := r.CompleteJob(ctx, old, "stale", []string{}, "stale title"); !errors.Is(err, ErrJobLost) {
		t.Fatalf("stale title write: %v", err)
	}
	if err := restarted.SaveTranscript(ctx, current, "Расшифровка сохранена"); err != nil {
		t.Fatal(err)
	}
	result, err := r.GetResult(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Transcript != "Расшифровка сохранена" || result.Summary != "" || result.Recording.ProcessingStage != "summarizing" {
		t.Fatalf("transcript not independently visible: %+v", result)
	}
	if err := restarted.FinishJob(ctx, current, "Ollama unavailable", false); err != nil {
		t.Fatal(err)
	}
	result, err = r.GetResult(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Recording.Status != "failed" || result.Transcript == "" || result.Summary != "" {
		t.Fatalf("summary failure lost transcript: %+v", result)
	}
	if _, err := r.Enqueue(ctx, id, "summary"); err != nil {
		t.Fatal(err)
	}
	job, err := r.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if job.Mode != "summary" {
		t.Fatalf("unexpected retry mode: %s", job.Mode)
	}
	if err := r.FinishJob(ctx, job, "", true); err != nil {
		t.Fatal(err)
	}
	job, err = restarted.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.CompleteJob(ctx, job, "Готовое резюме", []string{"Тезис"}); err != nil {
		t.Fatal(err)
	}
	result, err = r.GetResult(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Recording.Status != "completed" || result.Summary != "Готовое резюме" || result.Transcript == "" {
		t.Fatalf("retry failed: %+v", result)
	}
}

func TestCheckpointAndMigration(t *testing.T) {
	pool := testutil.Database(t)
	r := NewRecordingRepository(pool)
	ctx := context.Background()
	id := recording(t, r)
	if _, err := r.Enqueue(ctx, id, "summary"); !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("summary without text: %v", err)
	}
	// Simulate the previous application's interrupted in-memory worker.
	if _, err := pool.Exec(ctx, `UPDATE recordings SET status='processing' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	job, err := r.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SaveTranscript(ctx, job, "Checkpoint"); err != nil {
		t.Fatal(err)
	}
	// Re-running migration must not replace an active lease.
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := r.RenewJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE processing_jobs SET lease_until=NOW()-INTERVAL '1 second' WHERE recording_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	recovered, err := r.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Mode != "summary" {
		t.Fatalf("saved transcript would be recognized twice: %s", recovered.Mode)
	}
	if _, err := r.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteJob(ctx, recovered, "obsolete", []string{}); !errors.Is(err, ErrJobLost) {
		t.Fatalf("deleted recording write: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM processing_jobs WHERE recording_id=$1`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("deleted recording left a job")
	}
}
