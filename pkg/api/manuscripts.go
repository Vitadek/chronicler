package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"chronicle-server/pkg/auth"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"

	"github.com/go-chi/chi/v5"
)

type ManuscriptsHandler struct {
	cfg      *config.Config
	database *sql.DB
}

func NewManuscriptsHandler(cfg *config.Config, database *sql.DB) *ManuscriptsHandler {
	return &ManuscriptsHandler{
		cfg:      cfg,
		database: database,
	}
}

func (h *ManuscriptsHandler) Mount(r chi.Router) {
	r.Get("/", h.list)
	// Portable per-user archives live with manuscript CRUD. Import is additive
	// and cannot replace the SQLite database or overwrite an existing record.
	archive := NewManuscriptArchiveHandler(h.cfg, h.database)
	r.Get("/archive/export", archive.GetExport)
	r.Post("/archive/import", archive.PostImport)
	r.Get("/{id}", h.getOne)
	r.Post("/", h.create)
	r.Put("/{id}", h.update)
	r.Delete("/{manuscriptId}/chapters/{chapterId}", h.deleteChapter)
	r.Delete("/{id}", h.deleteManuscript)
}

func (h *ManuscriptsHandler) list(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	list, err := db.ListManuscripts(h.database, userId)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if list == nil {
		w.Write([]byte("[]"))
	} else {
		json.NewEncoder(w).Encode(list)
	}
}

func (h *ManuscriptsHandler) getOne(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")

	manuscript, err := db.LoadManuscript(h.database, userId, id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if manuscript == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "Manuscript not found"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(manuscript)
}

func (h *ManuscriptsHandler) create(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var mRecord db.ManuscriptRecord
	if err := json.NewDecoder(r.Body).Decode(&mRecord); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid manuscript payload"})
		return
	}

	result, err := db.SaveLegacyManuscript(h.database, userId, &mRecord, true)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if len(result.Conflicts) > 0 {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":      "A manuscript with this id already exists",
			"manuscript": result.Manuscript,
			"conflicts":  result.Conflicts,
		})
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(result.Manuscript)
}

func (h *ManuscriptsHandler) update(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")

	var mRecord db.ManuscriptRecord
	if err := json.NewDecoder(r.Body).Decode(&mRecord); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid manuscript payload"})
		return
	}

	mRecord.Metadata.ID = id

	result, err := db.SaveLegacyManuscript(h.database, userId, &mRecord, false)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if len(result.Conflicts) > 0 {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":      "The manuscript changed on another device",
			"manuscript": result.Manuscript,
			"conflicts":  result.Conflicts,
		})
		return
	}

	json.NewEncoder(w).Encode(result.Manuscript)
}

type deleteBody struct {
	BaseRevision *int `json:"baseRevision"`
}

func (h *ManuscriptsHandler) deleteChapter(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	manuscriptId := chi.URLParam(r, "manuscriptId")
	chapterId := chi.URLParam(r, "chapterId")

	var delBody deleteBody
	_ = json.NewDecoder(r.Body).Decode(&delBody) // ignore error, default baseRevision to nil

	ok, revision, manuscriptRevision, currentRevision, err := db.DeleteChapter(h.database, userId, manuscriptId, chapterId, delBody.BaseRevision)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if !ok {
		if currentRevision == 0 {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "Chapter not found"})
		} else {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":           "The chapter changed on another device",
				"currentRevision": currentRevision,
			})
		}
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":                 true,
		"revision":           revision,
		"manuscriptRevision": manuscriptRevision,
	})
}

func (h *ManuscriptsHandler) deleteManuscript(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")

	var delBody deleteBody
	_ = json.NewDecoder(r.Body).Decode(&delBody) // ignore error, default baseRevision to nil

	ok, _, currentRevision, err := db.DeleteManuscript(h.database, userId, id, delBody.BaseRevision)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if !ok {
		if currentRevision == 0 {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "Manuscript not found"})
		} else {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":           "The manuscript changed on another device",
				"currentRevision": currentRevision,
			})
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
