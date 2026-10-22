package service

import (
	"ai-recorder/internal/config"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Explicit opt-in only: uses public sample audio, installed models and Ollama.
func TestLocalSpeakerModels(t *testing.T) {
	if os.Getenv("ECHO_SPEAKER_SMOKE") != "1" {
		t.Skip("set ECHO_SPEAKER_SMOKE=1 with local model paths")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Load()
	cfg.DiarizationPython = filepath.Join(root, ".venv-diarization", "Scripts", "python.exe")
	cfg.DiarizationScript = filepath.Join(root, "scripts", "diarize.py")
	cfg.DiarizationSegmentation = filepath.Join(root, "models", "diarization", "sherpa-onnx-pyannote-segmentation-3-0", "model.onnx")
	cfg.DiarizationEmbedding = filepath.Join(root, "models", "diarization", "nemo_en_titanet_small.onnx")
	cfg.WhisperLanguage = "auto"
	p := NewProcessor(nil, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sample := filepath.Join(root, "models", "diarization", "test-four-speakers.wav")
	turns, err := p.diarize(ctx, sample)
	if err != nil {
		t.Fatal(err)
	}
	voices := map[int]bool{}
	for _, turn := range turns {
		voices[turn.Speaker] = true
	}
	if len(voices) != 4 || len(turns) < 8 || turns[0].Speaker != turns[len(turns)-1].Speaker {
		t.Fatalf("public four-speaker example: %d voices / %d turns", len(voices), len(turns))
	}
	// Verify the actual installed whisper-cli JSON schema and word timing flags.
	wav := filepath.Join(t.TempDir(), "sample.wav")
	if err := p.convertToWAV(ctx, sample, wav); err != nil {
		t.Fatal(err)
	}
	text, err := p.transcribe(ctx, wav)
	if err != nil {
		t.Fatal(err)
	}
	grouped := assignSpeakers(text.Segments, turns)
	if text.Text == "" || len(grouped) < 4 {
		t.Fatalf("missing transcript or aligned turns: %d", len(grouped))
	}
	assigned := map[string]bool{}
	for _, segment := range grouped {
		if segment.SpeakerID != "" {
			assigned[segment.SpeakerID] = true
		}
	}
	if len(assigned) != 4 {
		t.Fatalf("expected all four voices in aligned transcript, got %d", len(assigned))
	}
	t.Logf("public sample: %d voices, %d timed words, %d grouped turns", len(voices), len(text.Segments), len(grouped))
}

func TestLocalContextTitle(t *testing.T) {
	if os.Getenv("ECHO_TITLE_SMOKE") != "1" {
		t.Skip("set ECHO_TITLE_SMOKE=1 to use local Ollama")
	}
	p := NewProcessor(nil, config.Load())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	summary, _, title, err := p.summarize(ctx, "Обсудили запуск приложения. До пятницы нужно проверить запись голоса на телефоне, исправить ошибки и подготовить инструкцию для пользователей.")
	if err != nil {
		t.Fatal(err)
	}
	if title == "" || summary == "" {
		t.Fatal("missing contextual title")
	}
	t.Logf("synthetic note title: %s", title)
}
