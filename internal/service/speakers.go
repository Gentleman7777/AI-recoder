package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strings"
	"unicode"

	"ai-recorder/internal/model"
)

type speakerTurn struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker int     `json:"speaker"`
}

func (p *Processor) diarize(ctx context.Context, path string) ([]speakerTurn, error) {
	threshold := p.cfg.DiarizationThreshold
	if threshold == "" {
		threshold = "0.9"
	}
	cmd := exec.CommandContext(ctx, p.cfg.DiarizationPython, p.cfg.DiarizationScript,
		"--audio", path, "--segmentation", p.cfg.DiarizationSegmentation, "--embedding", p.cfg.DiarizationEmbedding, "--threshold", threshold)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// Do not persist subprocess output, which can contain paths or audio data.
		return nil, fmt.Errorf("speaker model process: %w", err)
	}
	var turns []speakerTurn
	if err := json.Unmarshal(stdout.Bytes(), &turns); err != nil {
		return nil, fmt.Errorf("invalid speaker timing output: %w", err)
	}
	for _, turn := range turns {
		if math.IsNaN(turn.Start) || math.IsInf(turn.Start, 0) || math.IsNaN(turn.End) || math.IsInf(turn.End, 0) || turn.Start < 0 || turn.End <= turn.Start || turn.Speaker < 0 {
			return nil, fmt.Errorf("invalid speaker interval")
		}
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].Start < turns[j].Start })
	return turns, nil
}

func parseWhisperSegments(data []byte) ([]model.Segment, error) {
	var response struct {
		Transcription []struct {
			Offsets struct {
				From float64 `json:"from"`
				To   float64 `json:"to"`
			} `json:"offsets"`
			Text string `json:"text"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("invalid Whisper timestamps: %w", err)
	}
	segments := make([]model.Segment, 0, len(response.Transcription))
	for _, item := range response.Transcription {
		if strings.TrimSpace(item.Text) == "" {
			continue
		}
		if item.Offsets.From < 0 || item.Offsets.To < item.Offsets.From {
			return nil, fmt.Errorf("invalid Whisper interval")
		}
		segments = append(segments, model.Segment{Start: item.Offsets.From / 1000, End: item.Offsets.To / 1000, Text: item.Text})
	}
	return segments, nil
}

// Match each timed word to the voice with the most overlap. Gaps and ambiguous
// simultaneous speech stay unassigned rather than inventing a speaker.
func assignSpeakers(words []model.Segment, turns []speakerTurn) []model.Segment {
	labels := map[int]string{}
	for _, turn := range turns {
		if _, ok := labels[turn.Speaker]; !ok {
			labels[turn.Speaker] = fmt.Sprintf("speaker_%d", len(labels)+1)
		}
	}
	var result []model.Segment
	for _, word := range words {
		scores := map[int]float64{}
		for _, turn := range turns {
			if turn.Start > word.End {
				break
			}
			overlap := math.Min(word.End, turn.End) - math.Max(word.Start, turn.Start)
			if overlap > 0 {
				scores[turn.Speaker] += overlap
			}
		}
		best, second, chosen := 0.0, 0.0, -1
		for speaker, score := range scores {
			if score > best {
				second, best, chosen = best, score, speaker
			} else if score > second {
				second = score
			}
		}
		word.SpeakerID = ""
		if best > 0 && second < best*0.8 {
			word.SpeakerID = labels[chosen]
		}
		n := len(result)
		if n > 0 && result[n-1].SpeakerID == word.SpeakerID && word.Start-result[n-1].End <= 1.5 && len([]rune(result[n-1].Text)) < 500 {
			result[n-1].End = math.Max(result[n-1].End, word.End)
			result[n-1].Text += word.Text
		} else {
			result = append(result, word)
		}
	}
	for i := range result {
		result[i].Text = strings.TrimSpace(result[i].Text)
	}
	return result
}

func cleanTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	title = strings.Trim(title, " \"'«»“”`#")
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, title)
	runes := []rune(title)
	if len(runes) > 80 {
		title = strings.TrimSpace(string(runes[:80]))
	}
	return title
}
