package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"chronicle-server/pkg/auth"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/plugins"

	"github.com/go-chi/chi/v5"
	"github.com/go-git/go-git/v5"
)

type PluginsHandler struct {
	cfg      *config.Config
	database *sql.DB
	checker  *plugins.CapabilitiesChecker
}

func NewPluginsHandler(cfg *config.Config, database *sql.DB) *PluginsHandler {
	return &PluginsHandler{
		cfg:      cfg,
		database: database,
		checker:  plugins.NewCapabilitiesChecker(cfg),
	}
}

func (h *PluginsHandler) Mount(r chi.Router) {
	r.Get("/", h.GetPlugins)
	r.Post("/install", h.PostInstall)
	r.Post("/{id}/check-updates", h.PostCheckUpdates)
	r.Post("/{id}/update", h.PostUpdate)
	r.Post("/{id}/pin", h.PostPin)
	r.Put("/{id}/enabled", h.PutEnabled)
	r.Put("/{id}/state", h.PutState)
	r.Get("/{id}/module.js", h.GetModule)
	r.Delete("/{id}", h.DeletePlugin)
}

type UserPluginState struct {
	ID           string `db:"id"`
	PluginID     string `db:"plugin_id"`
	ManuscriptID *string `db:"manuscript_id"`
	Enabled      int    `db:"enabled"`
	State        string `db:"state"`
}

func recordId(pluginId string, manuscriptId *string) string {
	mScope := "global"
	if manuscriptId != nil && *manuscriptId != "" {
		mScope = *manuscriptId
	}
	return fmt.Sprintf("plugin_%s_%s", pluginId, mScope)
}

func (h *PluginsHandler) userRows(userId string) ([]UserPluginState, error) {
	rows, err := h.database.Query("SELECT id, plugin_id, manuscript_id, enabled, state FROM plugin_states WHERE user_id = ?", userId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []UserPluginState
	for rows.Next() {
		var s UserPluginState
		if err := rows.Scan(&s.ID, &s.PluginID, &s.ManuscriptID, &s.Enabled, &s.State); err == nil {
			states = append(states, s)
		}
	}
	return states, nil
}

func (h *PluginsHandler) listForUser(userId string) ([]plugins.ResolveInput, error) {
	rows, err := h.userRows(userId)
	if err != nil {
		return nil, err
	}

	pluginsDir := filepath.Join(h.cfg.DataDir, "plugins")
	installed := plugins.InstalledIDs(pluginsDir)

	var list []plugins.ResolveInput
	for _, id := range installed {
		disk, err := plugins.DescribePlugin(pluginsDir, id)
		if err != nil || disk == nil {
			continue
		}

		var enabled bool

		for _, row := range rows {
			if row.PluginID == id && row.ManuscriptID == nil {
				enabled = row.Enabled == 1
				break
			}
		}

		list = append(list, plugins.ResolveInput{
			PluginDeps: plugins.PluginDeps{
				ID:        disk.ID,
				Provides:  disk.Provides,
				Requires:  disk.Requires,
				Wants:     disk.Wants,
				Conflicts: disk.Conflicts,
				Replaces:  disk.Replaces,
			},
			Enabled:    enabled,
			BuildError: disk.BuildError,
		})
	}
	return list, nil
}

func (h *PluginsHandler) stateForUser(userId string) ([]plugins.ResolveInput, []string, plugins.Resolution, error) {
	pluginsList, err := h.listForUser(userId)
	if err != nil {
		return nil, nil, plugins.Resolution{}, err
	}

	hostCaps := h.checker.HostCapabilities()
	resolution := plugins.Resolve(pluginsList, hostCaps)
	return pluginsList, hostCaps, resolution, nil
}

func (h *PluginsHandler) upsertRecord(userId string, pluginId string, manuscriptId *string, enabled *bool, state *string) error {
	id := recordId(pluginId, manuscriptId)

	var existingEnabled int
	var existingState string
	err := h.database.QueryRow("SELECT enabled, state FROM plugin_states WHERE user_id = ? AND id = ?", userId, id).Scan(&existingEnabled, &existingState)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	finalEnabled := existingEnabled
	if enabled != nil {
		if *enabled {
			finalEnabled = 1
		} else {
			finalEnabled = 0
		}
	}

	finalState := "{}"
	if existingState != "" {
		finalState = existingState
	}
	if state != nil {
		finalState = *state
	}

	now := time.Now().UnixNano() / int64(time.Millisecond)

	_, err = h.database.Exec(`
		INSERT INTO plugin_states (user_id, id, plugin_id, manuscript_id, enabled, state, last_modified)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, id) DO UPDATE SET
			enabled = excluded.enabled,
			state = excluded.state,
			last_modified = excluded.last_modified
	`, userId, id, pluginId, manuscriptId, finalEnabled, finalState, now)
	return err
}

func (h *PluginsHandler) GetPlugins(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Not authenticated"}`))
		return
	}

	pluginsList, hostCaps, resolution, err := h.stateForUser(userId)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	pluginsDir := filepath.Join(h.cfg.DataDir, "plugins")
	var responsePlugins []map[string]interface{}

	for _, p := range pluginsList {
		disk, _ := plugins.DescribePlugin(pluginsDir, p.ID)
		if disk == nil {
			continue
		}

		status := resolution.Status[p.ID]
		var missingReasons []string
		for _, m := range status.Missing {
			missingReasons = append(missingReasons, plugins.ExplainMissingHostCapability(h.cfg, m))
		}

		var unmetWantsReasons []string
		for _, m := range status.UnmetWants {
			unmetWantsReasons = append(unmetWantsReasons, plugins.ExplainMissingHostCapability(h.cfg, m))
		}

		buildErr := p.BuildError
		if buildErr == nil {
			if cycleErr, exists := resolution.Cycles[p.ID]; exists {
				buildErr = &cycleErr
			}
		}

		// Retrieve user specific state
		state := "{}"
		rows, _ := h.userRows(userId)
		for _, r := range rows {
			if r.PluginID == p.ID && r.ManuscriptID == nil {
				state = r.State
				break
			}
		}

		pm := map[string]interface{}{
			"id":                p.ID,
			"name":              disk.Name,
			"description":       disk.Description,
			"version":           disk.Version,
			"source":            disk.Source,
			"gitUrl":            disk.GitUrl,
			"commit":            disk.Commit,
			"pinnedRef":         disk.PinnedRef,
			"buildError":        buildErr,
			"provides":          disk.Provides,
			"requires":          disk.Requires,
			"wants":             disk.Wants,
			"conflicts":         disk.Conflicts,
			"replaces":          disk.Replaces,
			"dependencies":      disk.Dependencies,
			"enabled":           p.Enabled,
			"state":             state,
			"status":            status,
			"missingReasons":    missingReasons,
			"unmetWantsReasons": unmetWantsReasons,
		}
		responsePlugins = append(responsePlugins, pm)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"plugins":          responsePlugins,
		"hostCapabilities": hostCaps,
		"shadowedCore":     resolution.ShadowedCore,
		"activationOrder":  resolution.ActivationOrder,
	})
}

type InstallBody struct {
	Url  *string `json:"url"`
	Path *string `json:"path"`
}

func (h *PluginsHandler) PostInstall(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Not authenticated"}`))
		return
	}

	var body InstallBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (body.Url == nil && body.Path == nil) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Provide a git \"url\" or a local \"path\"."}`))
		return
	}

	pluginsDir := filepath.Join(h.cfg.DataDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	staging := filepath.Join(pluginsDir, fmt.Sprintf(".staging-%d", time.Now().UnixNano()))
	defer os.RemoveAll(staging)

	if body.Path != nil {
		src, err := filepath.Abs(*body.Path)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		if _, err := os.Stat(src); os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(fmt.Sprintf(`{"error":"No such folder: %s"}`, src)))
			return
		}

		// Read manifest to validate before copy
		if _, err := plugins.ReadManifest(src); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		// Recursive directory copy
		if err := copyDirectory(src, staging); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
	} else {
		// Clone using git
		if err := os.MkdirAll(staging, 0755); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		_, err := git.PlainClone(staging, false, &git.CloneOptions{
			URL:          *body.Url,
			Depth:        50,
			SingleBranch: true,
		})
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
	}

	manifest, err := plugins.ReadManifest(staging)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	finalDir, err := plugins.PluginDir(pluginsDir, manifest.ID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if _, err := os.Stat(finalDir); !os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf(`{"error":"Plugin %q is already installed."}`, manifest.ID)))
		return
	}

	if err := os.Rename(staging, finalDir); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	sourceType := "local"
	if body.Url != nil {
		sourceType = "git"
	}
	_ = plugins.WriteMeta(finalDir, plugins.RepoMeta{
		GitUrl:    body.Url,
		PinnedRef: nil,
		Source:    sourceType,
	})

	cwd, _ := os.Getwd()
	builtOk, buildErr := plugins.BuildPlugin(pluginsDir, manifest.ID, cwd)

	disk, _ := plugins.DescribePlugin(pluginsDir, manifest.ID)

	if !builtOk {
		var buildErrStr string
		if buildErr != nil {
			buildErrStr = buildErr.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":  fmt.Sprintf("Build failed: %s", buildErrStr),
			"plugin": disk,
		})
		return
	}

	// Fetch resolution state
	pluginsList, _ := h.listForUser(userId)
	hostCaps := h.checker.HostCapabilities()
	res := plugins.Resolve(pluginsList, hostCaps)
	status := res.Status[manifest.ID]

	var missingReasons []string
	for _, m := range status.Missing {
		missingReasons = append(missingReasons, plugins.ExplainMissingHostCapability(h.cfg, m))
	}
	var unmetWantsReasons []string
	for _, m := range status.UnmetWants {
		unmetWantsReasons = append(unmetWantsReasons, plugins.ExplainMissingHostCapability(h.cfg, m))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"plugin":            disk,
		"missing":           status.Missing,
		"missingReasons":    missingReasons,
		"unmetWants":        status.UnmetWants,
		"unmetWantsReasons": unmetWantsReasons,
	})
}

func (h *PluginsHandler) PostCheckUpdates(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !plugins.ValidatePluginID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid plugin id"}`))
		return
	}

	pluginsDir := filepath.Join(h.cfg.DataDir, "plugins")
	status, err := plugins.CheckForUpdates(pluginsDir, id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (h *PluginsHandler) PostUpdate(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Not authenticated"}`))
		return
	}

	id := chi.URLParam(r, "id")
	if !plugins.ValidatePluginID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid plugin id"}`))
		return
	}

	pluginsDir := filepath.Join(h.cfg.DataDir, "plugins")
	if err := plugins.PullRepo(pluginsDir, id); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	cwd, _ := os.Getwd()
	builtOk, buildErr := plugins.BuildPlugin(pluginsDir, id, cwd)

	disk, _ := plugins.DescribePlugin(pluginsDir, id)
	if !builtOk {
		var buildErrStr string
		if buildErr != nil {
			buildErrStr = buildErr.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":  fmt.Sprintf("Build failed: %s", buildErrStr),
			"plugin": disk,
		})
		return
	}

	rows, _ := h.userRows(userId)
	var enabled bool
	state := "{}"
	for _, r := range rows {
		if r.PluginID == id && r.ManuscriptID == nil {
			enabled = r.Enabled == 1
			state = r.State
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"plugin": map[string]interface{}{
			"id":           disk.ID,
			"name":         disk.Name,
			"description":  disk.Description,
			"version":      disk.Version,
			"source":       disk.Source,
			"gitUrl":       disk.GitUrl,
			"commit":       disk.Commit,
			"pinnedRef":    disk.PinnedRef,
			"buildError":   disk.BuildError,
			"provides":     disk.Provides,
			"requires":     disk.Requires,
			"wants":        disk.Wants,
			"conflicts":    disk.Conflicts,
			"replaces":     disk.Replaces,
			"dependencies": disk.Dependencies,
			"enabled":      enabled,
			"state":        state,
		},
	})
}

type PinBody struct {
	Ref *string `json:"ref"`
}

func (h *PluginsHandler) PostPin(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Not authenticated"}`))
		return
	}

	id := chi.URLParam(r, "id")
	if !plugins.ValidatePluginID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid plugin id"}`))
		return
	}

	var body PinBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"ref must be a string or null"}`))
		return
	}

	pluginsDir := filepath.Join(h.cfg.DataDir, "plugins")
	if err := plugins.PinRepo(pluginsDir, id, body.Ref); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if body.Ref != nil {
		cwd, _ := os.Getwd()
		_, _ = plugins.BuildPlugin(pluginsDir, id, cwd)
	}

	disk, _ := plugins.DescribePlugin(pluginsDir, id)
	row := UserPluginState{}
	rows, _ := h.userRows(userId)
	for _, r := range rows {
		if r.PluginID == id && r.ManuscriptID == nil {
			row = r
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"plugin": map[string]interface{}{
			"id":           disk.ID,
			"name":         disk.Name,
			"description":  disk.Description,
			"version":      disk.Version,
			"source":       disk.Source,
			"gitUrl":       disk.GitUrl,
			"commit":       disk.Commit,
			"pinnedRef":    disk.PinnedRef,
			"buildError":   disk.BuildError,
			"provides":     disk.Provides,
			"requires":     disk.Requires,
			"wants":        disk.Wants,
			"conflicts":    disk.Conflicts,
			"replaces":     disk.Replaces,
			"dependencies": disk.Dependencies,
			"enabled":      row.Enabled == 1,
			"state":        row.State,
		},
	})
}

type EnabledBody struct {
	Enabled bool `json:"enabled"`
}

func (h *PluginsHandler) PutEnabled(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Not authenticated"}`))
		return
	}

	id := chi.URLParam(r, "id")
	if !plugins.ValidatePluginID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid plugin id"}`))
		return
	}

	var body EnabledBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"enabled must be a boolean"}`))
		return
	}

	pluginsList, hostCaps, resolution, err := h.stateForUser(userId)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	var target *plugins.ResolveInput
	for i := range pluginsList {
		if pluginsList[i].ID == id {
			target = &pluginsList[i]
			break
		}
	}

	if target == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(fmt.Sprintf(`{"error":"Plugin %q is not installed."}`, id)))
		return
	}

	if body.Enabled {
		status := resolution.Status[id]
		if len(status.Missing) > 0 {
			var missingReasons []string
			for _, m := range status.Missing {
				missingReasons = append(missingReasons, plugins.ExplainMissingHostCapability(h.cfg, m))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   fmt.Sprintf("%s can't be enabled yet. %s", target.ID, strings.Join(missingReasons, " ")),
				"missing": status.Missing,
			})
			return
		}

		if len(status.ConflictsWith) > 0 {
			var names []string
			seenNames := make(map[string]bool)
			for _, c := range status.ConflictsWith {
				var name = c.PluginID
				for _, p := range pluginsList {
					if p.ID == c.PluginID {
						disk, _ := plugins.DescribePlugin(filepath.Join(h.cfg.DataDir, "plugins"), p.ID)
						if disk != nil {
							name = disk.Name
						}
						break
					}
				}
				if !seenNames[name] {
					seenNames[name] = true
					names = append(names, name)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":         fmt.Sprintf("%s conflicts with %s — they provide the same capability. Disable one first.", target.ID, strings.Join(names, ", ")),
				"conflictsWith": status.ConflictsWith,
			})
			return
		}
	} else {
		// Verify dependents
		dependents := plugins.DependentsOf(id, pluginsList, hostCaps)
		if len(dependents) > 0 {
			var names []string
			for _, d := range dependents {
				var name = d
				for _, p := range pluginsList {
					if p.ID == d {
						disk, _ := plugins.DescribePlugin(filepath.Join(h.cfg.DataDir, "plugins"), p.ID)
						if disk != nil {
							name = disk.Name
						}
						break
					}
				}
				names = append(names, name)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			verb := "requires"
			if len(dependents) > 1 {
				verb = "require"
			}
			pronoun := "it"
			if len(dependents) > 1 {
				pronoun = "them"
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":      fmt.Sprintf("%s %s %s. Disable %s first.", strings.Join(names, ", "), verb, target.ID, pronoun),
				"dependents": dependents,
			})
			return
		}
	}

	if err := h.upsertRecord(userId, id, nil, &body.Enabled, nil); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

type StateBody struct {
	State        string  `json:"state"`
	ManuscriptID *string `json:"manuscriptId"`
}

func (h *PluginsHandler) PutState(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Not authenticated"}`))
		return
	}

	id := chi.URLParam(r, "id")
	if !plugins.ValidatePluginID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid plugin id"}`))
		return
	}

	var body StateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.State) > 256*1024 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid plugin state"}`))
		return
	}

	if err := h.upsertRecord(userId, id, body.ManuscriptID, nil, &body.State); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (h *PluginsHandler) GetModule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !plugins.ValidatePluginID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid plugin id"}`))
		return
	}

	pluginsDir := filepath.Join(h.cfg.DataDir, "plugins")
	modulePath, err := plugins.BuiltModulePath(pluginsDir, id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if _, err := os.Stat(modulePath); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"Plugin is not built. Re-install or update it."}`))
		return
	}

	content, err := os.ReadFile(modulePath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(content)
}

func (h *PluginsHandler) DeletePlugin(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Not authenticated"}`))
		return
	}

	id := chi.URLParam(r, "id")
	if !plugins.ValidatePluginID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid plugin id"}`))
		return
	}

	pluginsList, hostCaps, _, err := h.stateForUser(userId)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Check if there are dependents before deleting
	dependents := plugins.DependentsOf(id, pluginsList, hostCaps)
	if len(dependents) > 0 {
		var names []string
		for _, d := range dependents {
			var name = d
			for _, p := range pluginsList {
				if p.ID == d {
					disk, _ := plugins.DescribePlugin(filepath.Join(h.cfg.DataDir, "plugins"), p.ID)
					if disk != nil {
						name = disk.Name
					}
					break
				}
			}
			names = append(names, name)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		verb := "depend"
		if len(dependents) == 1 {
			verb = "depends"
		}
		pronoun := "those plugins"
		if len(dependents) == 1 {
			pronoun = "that plugin"
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":      fmt.Sprintf("Can't uninstall — %s %s on it. Disable %s first.", strings.Join(names, ", "), verb, pronoun),
			"dependents": dependents,
		})
		return
	}

	pluginsDir := filepath.Join(h.cfg.DataDir, "plugins")
	if err := plugins.RemovePlugin(pluginsDir, id); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	_, _ = h.database.Exec("DELETE FROM plugin_states WHERE user_id = ? AND plugin_id = ?", userId, id)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	info, err := os.Stat(src)
	if err == nil {
		_ = out.Chmod(info.Mode())
	}

	_, err = io.Copy(out, in)
	return err
}

func copyDirectory(srcDir, dstDir string) error {
	info, err := os.Stat(srcDir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dstDir, info.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		if entry.IsDir() {
			if err := copyDirectory(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}
