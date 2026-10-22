package repository

import (
	"ai-recorder/internal/model"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrJobLost = errors.New("processing job lease lost")
var ErrNoTranscript = errors.New("transcript is not available")

const JobLease = 90 * time.Second

type Job struct {
	RecordingID string
	Token       string
	Mode        string
}

// Enqueue serializes requests on the recording. An active job is never replaced.
func (r *RecordingRepository) Enqueue(ctx context.Context, id, mode string) (bool, error) {
	if mode != "resume" && mode != "full" && mode != "summary" {
		return false, errors.New("invalid processing mode")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	var lockedID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM recordings WHERE id=$1 AND ($2::uuid IS NULL OR user_id=$2) FOR UPDATE`, id, r.owner()).Scan(&lockedID); err != nil {
		return false, err
	}
	if mode == "summary" {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM transcripts WHERE recording_id=$1 AND full_text <> '')`, id).Scan(&exists); err != nil {
			return false, err
		}
		if !exists {
			return false, ErrNoTranscript
		}
	}
	tag, err := tx.Exec(ctx, `
INSERT INTO processing_jobs (recording_id, mode) VALUES ($1,$2)
ON CONFLICT (recording_id) DO UPDATE SET mode=EXCLUDED.mode, state='queued',
 lease_token=NULL, lease_until=NULL, attempts=0, created_at=NOW(), updated_at=NOW()
WHERE processing_jobs.state IN ('done','failed')`, id, mode)
	if err != nil {
		return false, err
	}
	queued := tag.RowsAffected() > 0
	if queued {
		_, err = tx.Exec(ctx, `UPDATE recordings SET status='processing', processing_stage='queued', processing_error='', updated_at=NOW() WHERE id=$1`, id)
		if err != nil {
			return false, err
		}
	}
	return queued, tx.Commit(ctx)
}

func (r *RecordingRepository) ClaimJob(ctx context.Context) (Job, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback(context.Background())
	var job Job
	// All job mutations lock the recording first, so enqueue, completion and claim
	// use the same lock order. SKIP LOCKED lets other workers continue immediately.
	err = tx.QueryRow(ctx, `
SELECT j.recording_id::text, j.mode FROM processing_jobs j
JOIN recordings r ON r.id=j.recording_id
WHERE j.state='queued' OR (j.state='running' AND j.lease_until < NOW())
ORDER BY j.created_at, j.recording_id LIMIT 1 FOR UPDATE OF r SKIP LOCKED`).Scan(&job.RecordingID, &job.Mode)
	if err != nil {
		return Job{}, err
	}
	job.Token = uuid.NewString()
	err = tx.QueryRow(ctx, `UPDATE processing_jobs SET state='running', lease_token=$2,
 lease_until=NOW()+$3*INTERVAL '1 second', attempts=attempts+1, updated_at=NOW()
WHERE recording_id=$1 AND (state='queued' OR (state='running' AND lease_until<NOW()))
RETURNING mode`, job.RecordingID, job.Token, JobLease.Seconds()).Scan(&job.Mode)
	if err != nil {
		return Job{}, err
	}
	stage := "transcribing"
	if job.Mode == "summary" {
		stage = "summarizing"
	}
	_, err = tx.Exec(ctx, `UPDATE recordings SET status='processing', processing_stage=$2, processing_error='', updated_at=NOW() WHERE id=$1`, job.RecordingID, stage)
	if err != nil {
		return Job{}, err
	}
	return job, tx.Commit(ctx)
}

func (r *RecordingRepository) RenewJob(ctx context.Context, job Job) error {
	tag, err := r.db.Exec(ctx, `UPDATE processing_jobs SET lease_until=NOW()+$3*INTERVAL '1 second', updated_at=NOW()
WHERE recording_id=$1 AND lease_token=$2 AND state='running' AND lease_until > NOW()`, job.RecordingID, job.Token, JobLease.Seconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrJobLost
	}
	return nil
}

// ownedJob fences every output write: an expired worker cannot overwrite a retry.
func (r *RecordingRepository) ownedJob(ctx context.Context, job Job) (pgx.Tx, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM recordings WHERE id=$1 FOR UPDATE`, job.RecordingID).Scan(&id)
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT recording_id::text FROM processing_jobs WHERE recording_id=$1
AND lease_token=$2 AND state='running' AND lease_until>NOW() FOR UPDATE`, job.RecordingID, job.Token).Scan(&id)
	}
	if err != nil {
		_ = tx.Rollback(context.Background())
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrJobLost
		}
		return nil, err
	}
	return tx, nil
}

func (r *RecordingRepository) SaveTranscript(ctx context.Context, job Job, text string) error {
	return r.SaveRecognition(ctx, job, model.Transcription{Text: text, DiarizationStatus: "unavailable"})
}

func (r *RecordingRepository) SaveRecognition(ctx context.Context, job Job, result model.Transcription) error {
	if result.Segments == nil {
		result.Segments = []model.Segment{}
	}
	segments, err := json.Marshal(result.Segments)
	if err != nil {
		return err
	}
	tx, err := r.ownedJob(ctx, job)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `INSERT INTO transcripts (recording_id,full_text,segments,diarization_status) VALUES ($1,$2,$3,$4)
ON CONFLICT (recording_id) DO UPDATE SET full_text=EXCLUDED.full_text, segments=EXCLUDED.segments, diarization_status=EXCLUDED.diarization_status, updated_at=NOW()`, job.RecordingID, result.Text, segments, result.DiarizationStatus)
	if err != nil {
		return err
	}
	// A restarted job resumes at Ollama once the transcript is durable.
	if _, err = tx.Exec(ctx, `UPDATE processing_jobs SET mode='summary', updated_at=NOW() WHERE recording_id=$1`, job.RecordingID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM summaries WHERE recording_id=$1`, job.RecordingID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE recordings SET processing_stage='summarizing', updated_at=NOW() WHERE id=$1`, job.RecordingID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *RecordingRepository) MarkSummarizing(ctx context.Context, job Job) error {
	tx, err := r.ownedJob(ctx, job)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `UPDATE recordings SET processing_stage='summarizing', updated_at=NOW() WHERE id=$1`, job.RecordingID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *RecordingRepository) CompleteJob(ctx context.Context, job Job, summary string, points []string, titles ...string) error {
	pointsJSON, err := json.Marshal(points)
	if err != nil {
		return err
	}
	tx, err := r.ownedJob(ctx, job)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if len(titles) > 0 && titles[0] != "" {
		if _, err = tx.Exec(ctx, `UPDATE recordings SET title=$2 WHERE id=$1`, job.RecordingID, titles[0]); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO summaries (recording_id,summary_text,key_points) VALUES ($1,$2,$3)
ON CONFLICT (recording_id) DO UPDATE SET summary_text=EXCLUDED.summary_text,key_points=EXCLUDED.key_points,updated_at=NOW()`, job.RecordingID, summary, pointsJSON)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE recordings SET status='completed', processing_stage='', processing_error='', updated_at=NOW() WHERE id=$1`, job.RecordingID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE processing_jobs SET state='done',lease_token=NULL,lease_until=NULL,updated_at=NOW() WHERE recording_id=$1`, job.RecordingID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *RecordingRepository) FinishJob(ctx context.Context, job Job, message string, requeue bool) error {
	tx, err := r.ownedJob(ctx, job)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	state, status := "failed", "failed"
	if requeue {
		state, status, message = "queued", "processing", ""
	}
	if _, err = tx.Exec(ctx, `UPDATE processing_jobs SET state=$2,lease_token=NULL,lease_until=NULL,updated_at=NOW() WHERE recording_id=$1`, job.RecordingID, state); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE recordings SET status=$2,processing_error=$3,
processing_stage=CASE WHEN $2='processing' THEN 'queued' ELSE processing_stage END,updated_at=NOW() WHERE id=$1`, job.RecordingID, status, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
