package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"chronicle-server/pkg/auth"
	"chronicle-server/pkg/hugopublish"

	"github.com/go-chi/chi/v5"
)

// HugoPublishHandler exposes probe/publish/import over the Hugo-publish
// plugin's own git-credential (a GitHub PAT or similar), carried per-request
// in the JSON body — this handler persists nothing server-side; the plugin's
// own ctx.state is where the (client-side-encrypted) token lives between
// calls.
type HugoPublishHandler struct{}

func NewHugoPublishHandler() *HugoPublishHandler {
	return &HugoPublishHandler{}
}

func (h *HugoPublishHandler) Mount(r chi.Router) {
	r.Post("/probe", h.probe)
	r.Post("/publish", h.publish)
	r.Post("/import", h.importStory)
}

func writeJSONError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (h *HugoPublishHandler) probe(w http.ResponseWriter, r *http.Request) {
	if auth.GetUserID(r.Context()) == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req hugopublish.ProbeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, errors.New("invalid probe request"))
		return
	}
	result, err := hugopublish.Probe(req)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *HugoPublishHandler) publish(w http.ResponseWriter, r *http.Request) {
	if auth.GetUserID(r.Context()) == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req hugopublish.PublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, errors.New("invalid publish request"))
		return
	}
	result, err := hugopublish.Publish(req)
	if err != nil {
		if errors.Is(err, hugopublish.ErrNonFastForward) {
			writeJSONError(w, http.StatusConflict, err)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *HugoPublishHandler) importStory(w http.ResponseWriter, r *http.Request) {
	if auth.GetUserID(r.Context()) == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req hugopublish.ImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, errors.New("invalid import request"))
		return
	}
	result, err := hugopublish.ImportStory(req)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
