package model

import "time"

type Recording struct {
	ID              string    `json:"id"`
	Filename        string    `json:"filename"`
	Title           string    `json:"title"`
	MimeType        string    `json:"mime_type"`
	AudioSize       int64     `json:"audio_size"`
	Status          string    `json:"status"`
	ProcessingError string    `json:"processing_error,omitempty"`
	ProcessingStage string    `json:"processing_stage,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type RecordingResult struct {
	Recording         Recording `json:"recording"`
	Transcript        string    `json:"transcript"`
	Summary           string    `json:"summary"`
	KeyPoints         []string  `json:"key_points"`
	Segments          []Segment `json:"segments"`
	DiarizationStatus string    `json:"diarization_status"`
}

// Transcription is persisted before the summary, including speaker assignments.
type Transcription struct {
	Text              string
	Segments          []Segment
	DiarizationStatus string
}

type Segment struct {
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
	Text      string  `json:"text"`
	SpeakerID string  `json:"speaker_id,omitempty"`
}
