package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"chronicle-server/pkg/auth"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/replica"

	"github.com/go-chi/chi/v5"
)

type CoversHandler struct {
	cfg      *config.Config
	database *sql.DB
}

func NewCoversHandler(cfg *config.Config, database *sql.DB) *CoversHandler {
	return &CoversHandler{
		cfg:      cfg,
		database: database,
	}
}

func (h *CoversHandler) Mount(r chi.Router) {
	r.Post("/{manuscriptId}", h.PostCover)
	r.Get("/{filename}", h.GetCover)
	r.Delete("/{manuscriptId}", h.DeleteCover)
}

func sniffImage(buf []byte) (mime string, ext string, ok bool) {
	if len(buf) < 12 {
		return "", "", false
	}
	// PNG: 89 50 4E 47 0D 0A 1A 0A
	if buf[0] == 0x89 && buf[1] == 0x50 && buf[2] == 0x4e && buf[3] == 0x47 {
		return "image/png", "png", false || true
	}
	// JPEG: FF D8 FF
	if buf[0] == 0xff && buf[1] == 0xd8 && buf[2] == 0xff {
		return "image/jpeg", "jpg", true
	}
	// WEBP: RIFF .... WEBP
	if buf[0] == 0x52 && buf[1] == 0x49 && buf[2] == 0x46 && buf[3] == 0x46 &&
		buf[8] == 0x57 && buf[9] == 0x45 && buf[10] == 0x42 && buf[11] == 0x50 {
		return "image/webp", "webp", true
	}
	return "", "", false
}

const maxCoverBytes = 8 * 1024 * 1024 // 8 MB

func (h *CoversHandler) PostCover(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Not authenticated"}`))
		return
	}

	manuscriptId := chi.URLParam(r, "manuscriptId")
	mIDReg := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	if !mIDReg.MatchString(manuscriptId) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid manuscript id"}`))
		return
	}

	// Limit reader to maxCoverBytes + 1 so we can detect if it exceeds limit
	limitedReader := io.LimitReader(r.Body, maxCoverBytes+1)
	buf, err := io.ReadAll(limitedReader)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Failed to read request body"}`))
		return
	}

	if len(buf) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Empty body"}`))
		return
	}

	if len(buf) > maxCoverBytes {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		w.Write([]byte(`{"error":"Cover too large (max 8 MB)"}`))
		return
	}

	mime, ext, ok := sniffImage(buf)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		w.Write([]byte(`{"error":"Unsupported image type (use PNG, JPEG, or WebP)"}`))
		return
	}

	tx, err := h.database.Begin()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"Failed to start database transaction"}`))
		return
	}
	defer tx.Rollback()

	// 1. Wipe any existing covers for this manuscript
	prefix := fmt.Sprintf("covers/%s/%s.", userId, manuscriptId)
	pattern := fmt.Sprintf("covers/%s/%s.%%", userId, manuscriptId)
	rows, err := tx.Query("SELECT key FROM storage_blobs WHERE key LIKE ?", pattern)
	if err == nil {
		var keysToDelete []string
		for rows.Next() {
			var k string
			if errScan := rows.Scan(&k); errScan == nil && strings.HasPrefix(k, prefix) {
				keysToDelete = append(keysToDelete, k)
			}
		}
		rows.Close()

		for _, k := range keysToDelete {
			_, _ = tx.Exec("DELETE FROM storage_blobs WHERE key = ?", k)
			gen, errGen := replica.NextStorageGeneration(tx, k)
			if errGen == nil {
				_ = replica.EnqueueAtGeneration(tx, replica.PortableReplicaKey(k), "delete", gen, nil, "", "")
			}
		}
	}

	// 2. Write new cover
	randBuf := make([]byte, 6)
	_, _ = rand.Read(randBuf)
	random := hex.EncodeToString(randBuf)
	filename := fmt.Sprintf("%s.%s.%s", manuscriptId, random, ext)
	key := fmt.Sprintf("covers/%s/%s", userId, filename)

	checksum := replica.Sha256(buf)
	gen, err := replica.NextStorageGeneration(tx, key)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"Failed to allocate generation"}`))
		return
	}

	now := time.Now().UnixNano() / int64(time.Millisecond)
	_, err = tx.Exec(`
		INSERT INTO storage_blobs (key, content, content_type, generation, checksum, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			content = excluded.content,
			content_type = excluded.content_type,
			generation = excluded.generation,
			checksum = excluded.checksum,
			updated_at = excluded.updated_at
	`, key, buf, mime, gen, checksum, now)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"Failed to save cover art"}`))
		return
	}

	// Enqueue put replica
	replicaKey := replica.PortableReplicaKey(key)
	err = replica.EnqueueAtGeneration(tx, replicaKey, "put", gen, buf, mime, checksum)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"Failed to queue replication"}`))
		return
	}

	if errCommit := tx.Commit(); errCommit != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"Failed to commit database transaction"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprintf(`{"coverArt":"%s","mime":"%s","bytes":%d}`, filename, mime, len(buf))))
}

func (h *CoversHandler) GetCover(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Not authenticated"}`))
		return
	}

	filename := chi.URLParam(r, "filename")
	fileReg := regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	if !fileReg.MatchString(filename) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid filename"}`))
		return
	}

	key := fmt.Sprintf("covers/%s/%s", userId, filename)
	var content []byte
	var contentType string
	err := h.database.QueryRow("SELECT content, content_type FROM storage_blobs WHERE key = ?", key).Scan(&content, &contentType)
	if err == sql.ErrNoRows {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"Cover not found"}`))
		return
	} else if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"Database error"}`))
		return
	}

	mime := "image/jpeg"
	if strings.HasSuffix(filename, ".png") {
		mime = "image/png"
	} else if strings.HasSuffix(filename, ".webp") {
		mime = "image/webp"
	}

	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Write(content)
}

func (h *CoversHandler) DeleteCover(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Not authenticated"}`))
		return
	}

	manuscriptId := chi.URLParam(r, "manuscriptId")
	mIDReg := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	if !mIDReg.MatchString(manuscriptId) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid manuscript id"}`))
		return
	}

	tx, err := h.database.Begin()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"Failed to start database transaction"}`))
		return
	}
	defer tx.Rollback()

	prefix := fmt.Sprintf("covers/%s/%s.", userId, manuscriptId)
	pattern := fmt.Sprintf("covers/%s/%s.%%", userId, manuscriptId)
	rows, err := tx.Query("SELECT key FROM storage_blobs WHERE key LIKE ?", pattern)
	if err == nil {
		var keysToDelete []string
		for rows.Next() {
			var k string
			if errScan := rows.Scan(&k); errScan == nil && strings.HasPrefix(k, prefix) {
				keysToDelete = append(keysToDelete, k)
			}
		}
		rows.Close()

		for _, k := range keysToDelete {
			_, _ = tx.Exec("DELETE FROM storage_blobs WHERE key = ?", k)
			gen, errGen := replica.NextStorageGeneration(tx, k)
			if errGen == nil {
				_ = replica.EnqueueAtGeneration(tx, replica.PortableReplicaKey(k), "delete", gen, nil, "", "")
			}
		}
	}

	if errCommit := tx.Commit(); errCommit != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"Failed to commit database transaction"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}
