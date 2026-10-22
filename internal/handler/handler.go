package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai-recorder/internal/auth"
	"ai-recorder/internal/model"
	"ai-recorder/internal/repository"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Handler struct {
	repo           *repository.RecordingRepository
	maxUploadBytes int64
}

func New(repo *repository.RecordingRepository, maxUploadBytes int64) *Handler {
	return &Handler{repo: repo, maxUploadBytes: maxUploadBytes}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("POST /api/recordings", h.requireUser(h.upload))
	mux.HandleFunc("GET /api/recordings", h.requireUser(h.list))
	mux.HandleFunc("GET /api/stats", h.requireUser(h.stats))
	mux.HandleFunc("DELETE /api/recordings/{id}", h.requireUser(h.delete))
	mux.HandleFunc("GET /api/recordings/{id}", h.requireUser(h.get))
	mux.HandleFunc("GET /api/recordings/{id}/audio", h.requireUser(h.audio))
	mux.HandleFunc("POST /api/recordings/{id}/process", h.requireUser(h.process))
	mux.HandleFunc("GET /api/recordings/{id}/result", h.requireUser(h.result))
}

func (h *Handler) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if auth.UserID(r.Context()) == "" {
			writeError(w, 401, "Войдите, чтобы открыть записи.")
			return
		}
		next(w, r)
	}
}
func (h *Handler) userRepo(r *http.Request) *repository.RecordingRepository {
	return h.repo.ForUser(auth.UserID(r.Context()))
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "AI Recorder server is running"})
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(h.maxUploadBytes); err != nil {
		writeError(w, http.StatusBadRequest, "Не удалось прочитать форму или файл слишком большой")
		return
	}
	file, header, err := r.FormFile("audio")
	if err != nil {
		writeError(w, http.StatusBadRequest, `Передай аудиофайл в поле "audio"`)
		return
	}
	defer file.Close()

	if header.Size <= 0 {
		writeError(w, http.StatusBadRequest, "Файл пустой")
		return
	}
	if header.Size > h.maxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "Файл превышает допустимый размер")
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, h.maxUploadBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Не удалось прочитать аудиофайл")
		return
	}
	if int64(len(data)) > h.maxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "Файл превышает допустимый размер")
		return
	}

	filename := filepath.Base(strings.ReplaceAll(header.Filename, `\`, `/`))
	if filename == "." || filename == string(filepath.Separator) || filename == "" {
		filename = "recording.audio"
	}
	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(filename))
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	rec := model.Recording{
		ID:        uuid.NewString(),
		Filename:  filename,
		MimeType:  mimeType,
		AudioSize: int64(len(data)),
		Status:    "uploaded",
	}
	if err := h.userRepo(r).Create(r.Context(), rec, data); err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось сохранить аудио в PostgreSQL")
		return
	}
	created, err := h.userRepo(r).Get(r.Context(), rec.ID)
	if err != nil {
		writeJSON(w, http.StatusCreated, rec)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 20, 1, 100)
	offset := queryInt(r, "offset", 0, 0, 1000000)
	items, err := h.userRepo(r).List(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось получить список записей")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	s, err := h.userRepo(r).GetStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось получить статистику")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ok, err := h.userRepo(r).Delete(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRepoError(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "Запись не найдена")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	rec, err := h.userRepo(r).Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRepoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (h *Handler) audio(w http.ResponseWriter, r *http.Request) {
	data, mimeType, filename, err := h.userRepo(r).GetAudio(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRepoError(w, err)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	if rec, err := h.userRepo(r).Get(r.Context(), r.PathValue("id")); err == nil && rec.Title != "" {
		name := strings.Map(func(c rune) rune {
			if c < 32 || strings.ContainsRune(`<>:"/\|?*`, c) {
				return '-'
			}
			return c
		}, rec.Title)
		name = strings.Trim(name, " .")
		if name != "" {
			filename = name + filepath.Ext(filename)
		}
	}
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": safeHeaderFilename(filename)}))
	// Byte ranges let the audio player seek directly to a speaker's timestamp.
	http.ServeContent(w, r, filename, time.Time{}, bytes.NewReader(data))
}

func (h *Handler) process(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "Некорректный ID записи")
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "resume"
	}
	if mode != "resume" && mode != "full" && mode != "summary" {
		writeError(w, http.StatusBadRequest, "Допустимые режимы: resume, full, summary")
		return
	}
	queued, err := h.userRepo(r).Enqueue(r.Context(), id, mode)
	if errors.Is(err, repository.ErrNoTranscript) {
		writeError(w, http.StatusConflict, "Сначала нужно получить расшифровку")
		return
	}
	if err != nil {
		writeRepoError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "processing", "queued": queued})
}

func (h *Handler) result(w http.ResponseWriter, r *http.Request) {
	result, err := h.userRepo(r).GetResult(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Результат обработки пока отсутствует")
			return
		}
		writeRepoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeRepoError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Запись не найдена")
		return
	}
	writeError(w, http.StatusInternalServerError, "Ошибка чтения данных")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func queryInt(r *http.Request, key string, fallback, min, max int) int {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < min {
		return fallback
	}
	if n > max {
		return max
	}
	return n
}

func safeHeaderFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.ReplaceAll(name, `"`, "")
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	if name == "" {
		return "recording.audio"
	}
	return name
}
