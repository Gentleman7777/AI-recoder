package service

import (
	"ai-recorder/internal/model"
	"testing"
)

func TestSpeakerAlignment(t *testing.T) {
	words := []model.Segment{
		{Start: 0, End: 1, Text: " Привет."},
		{Start: 1, End: 2, Text: " Как дела?"},
		{Start: 2, End: 3, Text: " Хорошо."},
		{Start: 3, End: 4, Text: " Шум."},
		{Start: 4, End: 5, Text: " Одновременно."},
		{Start: 5, End: 6, Text: " Пока."},
	}
	turns := []speakerTurn{{0, 2, 8}, {2, 3, 2}, {4, 5, 8}, {4, 5, 2}, {5, 6, 8}}
	got := assignSpeakers(words, turns)
	if len(got) != 4 {
		t.Fatalf("expected 4 grouped turns, got %+v", got)
	}
	if got[0].SpeakerID != "speaker_1" || got[0].Text != "Привет. Как дела?" || got[1].SpeakerID != "speaker_2" || got[2].SpeakerID != "" || got[3].SpeakerID != "speaker_1" {
		t.Fatalf("incorrect voice mapping, gaps or returning speaker: %+v", got)
	}
	if got[0].Start != 0 || got[0].End != 2 {
		t.Fatalf("lost seek timing: %+v", got[0])
	}
}

func TestWhisperWordTimestamps(t *testing.T) {
	segments, err := parseWhisperSegments([]byte(`{"transcription":[{"offsets":{"from":1200,"to":1800},"text":" Привет"},{"offsets":{"from":1800,"to":2000},"text":"!"}]}`))
	if err != nil || len(segments) != 2 || segments[0].Start != 1.2 || segments[1].End != 2 {
		t.Fatalf("incorrect timestamp units: %+v %v", segments, err)
	}
	if _, err := parseWhisperSegments([]byte(`{"transcription":[{"offsets":{"from":2000,"to":1000},"text":"broken"}]}`)); err == nil {
		t.Fatal("accepted inverted interval")
	}
}

func TestContextTitle(t *testing.T) {
	if got := cleanTitle("\n «Планы на следующую неделю» \t"); got != "Планы на следующую неделю" {
		t.Fatal(got)
	}
	long := ""
	for i := 0; i < 100; i++ {
		long += "я"
	}
	if got := cleanTitle(long); len([]rune(got)) != 80 {
		t.Fatalf("title length: %d", len([]rune(got)))
	}
}
