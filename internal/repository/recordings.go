package repository

import (
	"context"
	"encoding/json"
	"errors"

	"ai-recorder/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RecordingRepository struct {
	db     *pgxpool.Pool
	userID *string
}

// ForUser creates a request-scoped repository. An empty ID grants no access.
// The unscoped repository is reserved for trusted background workers.
func (r *RecordingRepository) ForUser(id string) *RecordingRepository {
	return &RecordingRepository{db: r.db, userID: &id}
}

func (r *RecordingRepository) owner() any {
	if r.userID == nil {
		return nil
	}
	if *r.userID == "" {
		return "00000000-0000-0000-0000-000000000000"
	}
	return *r.userID
}

func NewRecordingRepository(db *pgxpool.Pool) *RecordingRepository {
	return &RecordingRepository{db: db}
}

func (r *RecordingRepository) Create(ctx context.Context, rec model.Recording, audio []byte) error {
	if r.userID != nil && *r.userID == "" {
		return errors.New("missing recording owner")
	}
	_, err := r.db.Exec(ctx, `
INSERT INTO recordings (id, filename, mime_type, audio_data, audio_size, status, user_id)
VALUES ($1, $2, $3, $4, $5, 'uploaded', $6)`,
		rec.ID, rec.Filename, rec.MimeType, audio, len(audio), r.owner())
	return err
}

func (r *RecordingRepository) List(ctx context.Context, limit, offset int) ([]model.Recording, error) {
	rows, err := r.db.Query(ctx, `
SELECT id::text, filename, title, mime_type, audio_size, status, processing_error, processing_stage, created_at, updated_at
FROM recordings WHERE ($3::uuid IS NULL OR user_id=$3)
ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset, r.owner())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]model.Recording, 0)
	for rows.Next() {
		var rec model.Recording
		if err := rows.Scan(&rec.ID, &rec.Filename, &rec.Title, &rec.MimeType, &rec.AudioSize, &rec.Status, &rec.ProcessingError, &rec.ProcessingStage, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, rec)
	}
	return result, rows.Err()
}

func (r *RecordingRepository) Get(ctx context.Context, id string) (model.Recording, error) {
	var rec model.Recording
	err := r.db.QueryRow(ctx, `
SELECT id::text, filename, title, mime_type, audio_size, status, processing_error, processing_stage, created_at, updated_at
FROM recordings WHERE id = $1 AND ($2::uuid IS NULL OR user_id=$2)`, id, r.owner()).Scan(
		&rec.ID, &rec.Filename, &rec.Title, &rec.MimeType, &rec.AudioSize, &rec.Status,
		&rec.ProcessingError, &rec.ProcessingStage, &rec.CreatedAt, &rec.UpdatedAt)
	return rec, err
}

func (r *RecordingRepository) GetAudio(ctx context.Context, id string) ([]byte, string, string, error) {
	var data []byte
	var mimeType, filename string
	err := r.db.QueryRow(ctx, `SELECT audio_data, mime_type, filename FROM recordings WHERE id = $1 AND ($2::uuid IS NULL OR user_id=$2)`, id, r.owner()).
		Scan(&data, &mimeType, &filename)
	return data, mimeType, filename, err
}

func (r *RecordingRepository) GetResult(ctx context.Context, id string) (model.RecordingResult, error) {
	rec, err := r.Get(ctx, id)
	if err != nil {
		return model.RecordingResult{}, err
	}

	result := model.RecordingResult{
		Recording: rec,
		KeyPoints: []string{},
		Segments:  []model.Segment{},
	}
	var segmentsJSON, pointsJSON []byte
	err = r.db.QueryRow(ctx, `
SELECT COALESCE(t.full_text, ''), COALESCE(t.segments, '[]'::jsonb), COALESCE(s.summary_text, ''), COALESCE(s.key_points, '[]'::jsonb), COALESCE(t.diarization_status, 'unavailable')
FROM recordings r LEFT JOIN transcripts t ON t.recording_id=r.id
LEFT JOIN summaries s ON s.recording_id=r.id
WHERE r.id=$1 AND ($2::uuid IS NULL OR r.user_id=$2)`, id, r.owner()).Scan(&result.Transcript, &segmentsJSON, &result.Summary, &pointsJSON, &result.DiarizationStatus)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(segmentsJSON, &result.Segments); err != nil {
		return result, err
	}
	if err := json.Unmarshal(pointsJSON, &result.KeyPoints); err != nil {
		return result, err
	}
	return result, nil
}

func IsNotFound(err error) bool {
	return err == pgx.ErrNoRows
}

// Stats contains totals visible to this repository, not only the current page.
type Stats struct {
	Total      int64 `json:"total"`
	Completed  int64 `json:"completed"`
	Processing int64 `json:"processing"`
	Failed     int64 `json:"failed"`
	TotalBytes int64 `json:"total_bytes"`
}

func (r *RecordingRepository) GetStats(ctx context.Context) (Stats, error) {
	var s Stats
	err := r.db.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE status='completed'), COUNT(*) FILTER (WHERE status='processing'), COUNT(*) FILTER (WHERE status='failed'), COALESCE(SUM(audio_size),0) FROM recordings WHERE ($1::uuid IS NULL OR user_id=$1)`, r.owner()).Scan(&s.Total, &s.Completed, &s.Processing, &s.Failed, &s.TotalBytes)
	return s, err
}

func (r *RecordingRepository) Delete(ctx context.Context, id string) (bool, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM recordings WHERE id=$1 AND ($2::uuid IS NULL OR user_id=$2)`, id, r.owner())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
