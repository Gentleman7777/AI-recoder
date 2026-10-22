package config

import (
	"bufio"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr                string
	DatabaseURL             string
	MaxUploadBytes          int64
	WhisperCLI              string
	FFmpegCLI               string
	WhisperModel            string
	WhisperLanguage         string
	DiarizationEnabled      bool
	DiarizationPython       string
	DiarizationScript       string
	DiarizationSegmentation string
	DiarizationEmbedding    string
	DiarizationThreshold    string
	OllamaURL               string
	OllamaModel             string
	OllamaThink             bool
	OllamaMaxTokens         int
	OllamaKeepAlive         string
	ProcessingWorkers       int
}

func Load() Config {
	loadDotEnv(".env")
	maxMB := int64(200)
	if value := os.Getenv("MAX_UPLOAD_MB"); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed > 0 {
			maxMB = parsed
		}
	}

	workers, err := strconv.Atoi(envOr("PROCESSING_WORKERS", "1"))
	if err != nil || workers < 1 || workers > 8 {
		workers = 1
	}
	maxTokens, err := strconv.Atoi(envOr("OLLAMA_MAX_TOKENS", "768"))
	if err != nil || maxTokens < 128 || maxTokens > 8192 {
		maxTokens = 768
	}
	think, _ := strconv.ParseBool(envOr("OLLAMA_THINK", "false"))
	diarizationEnabled, _ := strconv.ParseBool(envOr("DIARIZATION_ENABLED", "false"))
	diarizationThreshold := envOr("DIARIZATION_THRESHOLD", "0.9")
	if threshold, err := strconv.ParseFloat(diarizationThreshold, 64); err != nil || math.IsNaN(threshold) || threshold <= 0 || threshold > 2 {
		diarizationThreshold = "0.9"
	}
	keepAlive := envOr("OLLAMA_KEEP_ALIVE", "5m")
	if duration, err := time.ParseDuration(keepAlive); err != nil || duration < 0 || duration > 24*time.Hour {
		keepAlive = "5m"
	}
	return Config{
		HTTPAddr:                envOr("HTTP_ADDR", ":8080"),
		DatabaseURL:             envOr("DATABASE_URL", ""),
		MaxUploadBytes:          maxMB * 1024 * 1024,
		WhisperCLI:              os.Getenv("WHISPER_CLI"),
		FFmpegCLI:               envOr("FFMPEG_CLI", "ffmpeg"),
		WhisperModel:            os.Getenv("WHISPER_MODEL"),
		WhisperLanguage:         envOr("WHISPER_LANGUAGE", "auto"),
		DiarizationEnabled:      diarizationEnabled,
		DiarizationPython:       envOr("DIARIZATION_PYTHON", "python"),
		DiarizationScript:       envOr("DIARIZATION_SCRIPT", "scripts/diarize.py"),
		DiarizationSegmentation: envOr("DIARIZATION_SEGMENTATION", "models/diarization/sherpa-onnx-pyannote-segmentation-3-0/model.onnx"),
		DiarizationEmbedding:    envOr("DIARIZATION_EMBEDDING", "models/diarization/nemo_en_titanet_small.onnx"),
		DiarizationThreshold:    diarizationThreshold,
		OllamaURL:               strings.TrimRight(envOr("OLLAMA_URL", "http://localhost:11434"), "/"),
		OllamaModel:             envOr("OLLAMA_MODEL", "qwen3:4b"),
		OllamaThink:             think,
		OllamaMaxTokens:         maxTokens,
		OllamaKeepAlive:         keepAlive,
		ProcessingWorkers:       workers,
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// loadDotEnv is a minimal .env reader to avoid adding another dependency.
func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}
