package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"chronicle-server/pkg/auth"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"

	"github.com/go-chi/chi/v5"
)

type SettingsHandler struct {
	cfg      *config.Config
	database *sql.DB
}

func NewSettingsHandler(cfg *config.Config, database *sql.DB) *SettingsHandler {
	return &SettingsHandler{
		cfg:      cfg,
		database: database,
	}
}

func (h *SettingsHandler) Mount(r chi.Router) {
	r.Get("/", h.getSettings)
	r.Put("/", h.putSettings)
}

const maxSettingsBytes = 128 * 1024

func (h *SettingsHandler) getSettings(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	buf, _, err := db.GetBlob(h.database, fmt.Sprintf("settings/%s", userId))
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if buf == nil {
		w.Write([]byte(`{"settings":null}`))
		return
	}

	var settings map[string]string
	if err := json.Unmarshal(buf, &settings); err != nil {
		w.Write([]byte(`{"settings":null}`))
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"settings": settings})
}

type settingsPutBody struct {
	Settings map[string]interface{} `json:"settings"`
}

func (h *SettingsHandler) putSettings(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var body settingsPutBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Settings == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Body must be { settings: { key: value, ... } }"})
		return
	}

	cleanSettings := make(map[string]string)
	for k, v := range body.Settings {
		strVal, ok := v.(string)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Setting %q must be a string", k)})
			return
		}
		cleanSettings[k] = strVal
	}

	jsonBytes, errMarshal := json.Marshal(cleanSettings)
	if errMarshal != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": errMarshal.Error()})
		return
	}

	if len(jsonBytes) > maxSettingsBytes {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		json.NewEncoder(w).Encode(map[string]string{"error": "Settings too large"})
		return
	}

	err := db.PutBlob(h.database, fmt.Sprintf("settings/%s", userId), jsonBytes, "application/json")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
