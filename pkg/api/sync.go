package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"chronicle-server/pkg/auth"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"
	"chronicle-server/pkg/replica"

	"github.com/go-chi/chi/v5"
)

type SyncHandler struct {
	cfg      *config.Config
	database *sql.DB
}

func NewSyncHandler(cfg *config.Config, database *sql.DB) *SyncHandler {
	return &SyncHandler{
		cfg:      cfg,
		database: database,
	}
}

func (h *SyncHandler) Mount(r chi.Router) {
	r.Post("/", h.syncV1)
	r.Post("/v2", h.syncV2)
	r.Get("/v2/bootstrap", h.syncV2Bootstrap)
}

// ============================================================================
// Sync V1 (Legacy LWW)
// ============================================================================

type ManuscriptSyncIn struct {
	ID           string `json:"id"`
	Data         string `json:"data"`
	LastModified int64  `json:"last_modified"`
	Deleted      bool   `json:"deleted,omitempty"`
}

type ChapterSyncIn struct {
	ID           string  `json:"id"`
	ManuscriptID string  `json:"manuscript_id"`
	Title        *string `json:"title,omitempty"`
	Content      *string `json:"content,omitempty"`
	Position     *int    `json:"position,omitempty"`
	LastModified int64   `json:"last_modified"`
	Deleted      bool    `json:"deleted,omitempty"`
}

type ProfileSyncIn struct {
	Data         string `json:"data"`
	LastModified int64  `json:"last_modified"`
}

type PluginStateSyncIn struct {
	ID           string  `json:"id"`
	PluginID     string  `json:"plugin_id"`
	ManuscriptID *string `json:"manuscript_id,omitempty"`
	Enabled      bool    `json:"enabled,omitempty"`
	State        string  `json:"state"`
	LastModified int64   `json:"last_modified"`
}

type SyncPush struct {
	Manuscripts []ManuscriptSyncIn  `json:"manuscripts"`
	Chapters    []ChapterSyncIn     `json:"chapters"`
	Profile     *ProfileSyncIn      `json:"profile,omitempty"`
	Plugins     []PluginStateSyncIn `json:"plugins"`
}

type SyncRequest struct {
	Since int64    `json:"since"`
	Push  SyncPush `json:"push"`
}

type SyncResponse struct {
	ServerTime int64       `json:"serverTime"`
	Pull       SyncPullOut `json:"pull"`
}

type SyncPullOut struct {
	Manuscripts []ManuscriptSyncOut  `json:"manuscripts"`
	Chapters    []ChapterSyncOut     `json:"chapters"`
	Profile     *ProfileSyncIn       `json:"profile"`
	Plugins     []PluginStateSyncOut `json:"plugins"`
}

type ManuscriptSyncOut struct {
	ID           string `json:"id"`
	Data         string `json:"data"`
	LastModified int64  `json:"last_modified"`
	Deleted      bool   `json:"deleted"`
}

type ChapterSyncOut struct {
	ID           string  `json:"id"`
	ManuscriptID string  `json:"manuscript_id"`
	Title        *string `json:"title"`
	Content      *string `json:"content"`
	Position     *int    `json:"position"`
	LastModified int64   `json:"last_modified"`
	Deleted      bool    `json:"deleted"`
}

type PluginStateSyncOut struct {
	ID           string  `json:"id"`
	PluginID     string  `json:"plugin_id"`
	ManuscriptID *string `json:"manuscript_id"`
	Enabled      bool    `json:"enabled"`
	State        string  `json:"state"`
	LastModified int64   `json:"last_modified"`
}

func (h *SyncHandler) syncV1(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var req SyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid sync payload"})
		return
	}

	serverTime := time.Now().UnixNano() / int64(time.Millisecond)

	tx, err := h.database.Begin()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Database error"})
		return
	}
	defer tx.Rollback()

	// 1. Process Manuscripts Push
	if len(req.Push.Manuscripts) > 0 {
		stmtGetMs, errStmt := tx.Prepare("SELECT last_modified, deleted_at, revision FROM manuscripts WHERE user_id = ? AND id = ?")
		if errStmt != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": errStmt.Error()})
			return
		}
		defer stmtGetMs.Close()

		stmtUpMs, errStmt := tx.Prepare(`
			INSERT INTO manuscripts (user_id, id, data, last_modified, deleted_at, revision)
			VALUES (?, ?, ?, ?, ?, 1)
			ON CONFLICT(user_id, id) DO UPDATE SET
				data          = excluded.data,
				last_modified = excluded.last_modified,
				deleted_at    = excluded.deleted_at,
				revision      = manuscripts.revision + 1
		`)
		if errStmt != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": errStmt.Error()})
			return
		}
		defer stmtUpMs.Close()

		for _, m := range req.Push.Manuscripts {
			var existingLastModified int64
			var existingDeletedAt sql.NullInt64
			var existingRevision int

			errGet := stmtGetMs.QueryRow(userId, m.ID).Scan(&existingLastModified, &existingDeletedAt, &existingRevision)
			if errGet == nil && existingDeletedAt.Valid {
				// Retained tombstone is terminal: ignore resurrection attempts
				continue
			}

			if errGet == sql.ErrNoRows || m.LastModified > existingLastModified {
				var deletedAtVal interface{} = nil
				data := m.Data
				if m.Deleted {
					deletedAtVal = m.LastModified
					data = fmt.Sprintf(`{"id":"%s"}`, m.ID)
				}

				_, errUp := stmtUpMs.Exec(userId, m.ID, data, m.LastModified, deletedAtVal)
				if errUp != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": errUp.Error()})
					return
				}

				newRevision := existingRevision + 1
				operation := "upsert"
				if m.Deleted {
					operation = "delete"
				}
				_, _ = db.RecordChange(tx, userId, "manuscript", nil, m.ID, operation, newRevision, m.LastModified)

				if m.Deleted {
					// Purge collaboration residues
					scopedPrefix := fmt.Sprintf("%s/%s:", url.QueryEscape(userId), m.ID)
					_, _ = tx.Exec("DELETE FROM ydocs WHERE substr(name, 1, ?) = ?", len(scopedPrefix), scopedPrefix)
					legacyPrefix := fmt.Sprintf("%s:", m.ID)
					_, _ = tx.Exec("DELETE FROM ydocs WHERE substr(name, 1, ?) = ?", len(legacyPrefix), legacyPrefix)
					_, _ = tx.Exec("DELETE FROM chapter_pre_collab WHERE user_id = ? AND manuscript_id = ?", userId, m.ID)

					// Enqueue manuscript replica tombstone
					tombData := replica.SerializeManuscriptTombstone(userId, m.ID, m.LastModified, newRevision)
					_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/manuscript.json", userId, m.ID), tombData, "application/json")

					// Tombstone active chapters
					rows, errCh := tx.Query("SELECT id, revision FROM chapters WHERE user_id = ? AND manuscript_id = ? AND deleted_at IS NULL", userId, m.ID)
					if errCh == nil {
						type chTomb struct{ id string; rev int }
						var chs []chTomb
						for rows.Next() {
							var c chTomb
							if errScan := rows.Scan(&c.id, &c.rev); errScan == nil {
								chs = append(chs, c)
							}
						}
						rows.Close()

						for _, ch := range chs {
							chRev := ch.rev + 1
							_, _ = tx.Exec(`
								UPDATE chapters
								   SET title = NULL, content = NULL, position = NULL,
								       last_modified = ?, deleted_at = ?, revision = ?
								 WHERE user_id = ? AND manuscript_id = ? AND id = ?
							`, m.LastModified, m.LastModified, chRev, userId, m.ID, ch.id)

							_, _ = db.RecordChange(tx, userId, "chapter", &m.ID, ch.id, "delete", chRev, m.LastModified)

							chTombData := replica.SerializeChapterTombstone(userId, m.ID, ch.id, m.LastModified, chRev)
							_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/chapters/%s.html", userId, m.ID, ch.id), chTombData, "text/html; charset=utf-8")
						}
					}
				} else {
					// Enqueue manuscript replica put
					replData, _ := replica.SerializeManuscript(userId, m.ID, m.LastModified, newRevision, m.Data)
					_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/manuscript.json", userId, m.ID), replData, "application/json")
				}
			}
		}
	}

	// 2. Process Chapters Push
	if len(req.Push.Chapters) > 0 {
		stmtGetCh, errStmt := tx.Prepare(`
			SELECT last_modified, deleted_at, revision FROM chapters
			 WHERE user_id = ? AND manuscript_id = ? AND id = ?
		`)
		if errStmt != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": errStmt.Error()})
			return
		}
		defer stmtGetCh.Close()

		stmtHasMs, errStmt := tx.Prepare("SELECT 1 FROM manuscripts WHERE user_id = ? AND id = ? AND deleted_at IS NULL")
		if errStmt != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": errStmt.Error()})
			return
		}
		defer stmtHasMs.Close()

		stmtUpCh, errStmt := tx.Prepare(`
			INSERT INTO chapters (user_id, manuscript_id, id, title, content, position, last_modified, deleted_at, revision)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
			ON CONFLICT(user_id, manuscript_id, id) DO UPDATE SET
				title         = excluded.title,
				content       = excluded.content,
				position      = excluded.position,
				last_modified = excluded.last_modified,
				deleted_at    = excluded.deleted_at,
				revision      = chapters.revision + 1
		`)
		if errStmt != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": errStmt.Error()})
			return
		}
		defer stmtUpCh.Close()

		for _, c := range req.Push.Chapters {
			var existingLastModified int64
			var existingDeletedAt sql.NullInt64
			var existingRevision int

			errGet := stmtGetCh.QueryRow(userId, c.ManuscriptID, c.ID).Scan(&existingLastModified, &existingDeletedAt, &existingRevision)

			// Check if active manuscript exists
			var hasActiveMs int
			_ = stmtHasMs.QueryRow(userId, c.ManuscriptID).Scan(&hasActiveMs)

			if !c.Deleted && hasActiveMs == 0 {
				continue // skip orphan chapters
			}
			if c.Deleted && errGet == sql.ErrNoRows {
				continue // skip deleting non-existent chapters
			}
			if errGet == nil && existingDeletedAt.Valid {
				continue // skip resurrection attempts on retained tombstones
			}

			if errGet == sql.ErrNoRows || c.LastModified > existingLastModified {
				var delAtVal interface{} = nil
				if c.Deleted {
					delAtVal = c.LastModified
				}

				_, errUp := stmtUpCh.Exec(userId, c.ManuscriptID, c.ID, c.Title, c.Content, c.Position, c.LastModified, delAtVal)
				if errUp != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": errUp.Error()})
					return
				}

				newRevision := existingRevision + 1
				operation := "upsert"
				if c.Deleted {
					operation = "delete"
				}
				_, _ = db.RecordChange(tx, userId, "chapter", &c.ManuscriptID, c.ID, operation, newRevision, c.LastModified)

				if c.Deleted {
					// Purge chapter collaboration residues
					_, _ = tx.Exec("DELETE FROM ydocs WHERE name = ?", fmt.Sprintf("%s/%s:%s", url.QueryEscape(userId), c.ManuscriptID, c.ID))
					_, _ = tx.Exec("DELETE FROM ydocs WHERE name = ?", fmt.Sprintf("%s:%s", c.ManuscriptID, c.ID))
					_, _ = tx.Exec(`
						DELETE FROM chapter_pre_collab
						 WHERE user_id = ? AND manuscript_id = ? AND chapter_id = ?
					`, userId, c.ManuscriptID, c.ID)

					// Enqueue replica tombstone
					chTombData := replica.SerializeChapterTombstone(userId, c.ManuscriptID, c.ID, c.LastModified, newRevision)
					_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/chapters/%s.html", userId, c.ManuscriptID, c.ID), chTombData, "text/html; charset=utf-8")
				} else {
					// Enqueue replica put
					titleStr := ""
					if c.Title != nil {
						titleStr = *c.Title
					}
					contentStr := ""
					if c.Content != nil {
						contentStr = *c.Content
					}
					posInt := 0
					if c.Position != nil {
						posInt = *c.Position
					}

					replData := replica.SerializeChapter(userId, c.ManuscriptID, c.ID, titleStr, posInt, c.LastModified, newRevision, contentStr)
					_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/chapters/%s.html", userId, c.ManuscriptID, c.ID), replData, "text/html; charset=utf-8")
				}

				_, _ = db.TouchManuscriptForChapterChange(tx, userId, c.ManuscriptID, c.LastModified)
			}
		}
	}

	// 3. Process Profile Push
	if req.Push.Profile != nil {
		var existingLastModified int64
		var existingRevision int

		errGet := tx.QueryRow("SELECT last_modified, revision FROM profiles WHERE user_id = ?", userId).Scan(&existingLastModified, &existingRevision)
		if errGet == sql.ErrNoRows || req.Push.Profile.LastModified > existingLastModified {
			_, errUp := tx.Exec(`
				INSERT INTO profiles (user_id, data, last_modified, revision)
				VALUES (?, ?, ?, 1)
				ON CONFLICT(user_id) DO UPDATE SET
					data          = excluded.data,
					last_modified = excluded.last_modified,
					revision      = profiles.revision + 1
			`, userId, req.Push.Profile.Data, req.Push.Profile.LastModified)
			if errUp != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": errUp.Error()})
				return
			}

			newRevision := existingRevision + 1
			_, _ = db.RecordChange(tx, userId, "profile", nil, "profile", "upsert", newRevision, req.Push.Profile.LastModified)

			// Enqueue profile replica put
			replData, _ := replica.SerializeProfile(userId, req.Push.Profile.Data, req.Push.Profile.LastModified, newRevision)
			_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("profiles/%s", userId), replData, "application/json")
		}
	}

	// 4. Process Plugins Push
	if len(req.Push.Plugins) > 0 {
		stmtGetPl, errStmt := tx.Prepare("SELECT last_modified FROM plugin_states WHERE user_id = ? AND id = ?")
		if errStmt != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": errStmt.Error()})
			return
		}
		defer stmtGetPl.Close()

		stmtUpPl, errStmt := tx.Prepare(`
			INSERT INTO plugin_states (user_id, id, plugin_id, manuscript_id, enabled, state, last_modified)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, id) DO UPDATE SET
				plugin_id     = excluded.plugin_id,
				manuscript_id = excluded.manuscript_id,
				enabled       = excluded.enabled,
				state         = excluded.state,
				last_modified = excluded.last_modified
		`)
		if errStmt != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": errStmt.Error()})
			return
		}
		defer stmtUpPl.Close()

		for _, p := range req.Push.Plugins {
			var existingLastModified int64
			errGet := stmtGetPl.QueryRow(userId, p.ID).Scan(&existingLastModified)
			if errGet == sql.ErrNoRows || p.LastModified > existingLastModified {
				_, errUp := stmtUpPl.Exec(userId, p.ID, p.PluginID, p.ManuscriptID, p.Enabled, p.State, p.LastModified)
				if errUp != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": errUp.Error()})
					return
				}
			}
		}
	}

	if errCommit := tx.Commit(); errCommit != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to commit sync v1 transaction"})
		return
	}

	// ---- Pull newer than req.Since ----
	manuscriptsOut := make([]ManuscriptSyncOut, 0, 16)
	rowsMs, errMs := h.database.Query("SELECT id, data, last_modified, deleted_at FROM manuscripts WHERE user_id = ? AND last_modified > ?", userId, req.Since)
	if errMs == nil {
		defer rowsMs.Close()
		for rowsMs.Next() {
			var mOut ManuscriptSyncOut
			var deletedAt sql.NullInt64
			if errScan := rowsMs.Scan(&mOut.ID, &mOut.Data, &mOut.LastModified, &deletedAt); errScan == nil {
				mOut.Deleted = deletedAt.Valid
				manuscriptsOut = append(manuscriptsOut, mOut)
			}
		}
	}

	chaptersOut := make([]ChapterSyncOut, 0, 32)
	rowsCh, errCh := h.database.Query("SELECT id, manuscript_id, title, content, position, last_modified, deleted_at FROM chapters WHERE user_id = ? AND last_modified > ?", userId, req.Since)
	if errCh == nil {
		defer rowsCh.Close()
		for rowsCh.Next() {
			var cOut ChapterSyncOut
			var title, content sql.NullString
			var pos sql.NullInt64
			var deletedAt sql.NullInt64
			if errScan := rowsCh.Scan(&cOut.ID, &cOut.ManuscriptID, &title, &content, &pos, &cOut.LastModified, &deletedAt); errScan == nil {
				cOut.Deleted = deletedAt.Valid
				if title.Valid {
					cOut.Title = &title.String
				}
				if content.Valid {
					cOut.Content = &content.String
				}
				if pos.Valid {
					pVal := int(pos.Int64)
					cOut.Position = &pVal
				}
				chaptersOut = append(chaptersOut, cOut)
			}
		}
	}

	var profileOut *ProfileSyncIn
	var profData string
	var profLastMod int64
	errProf := h.database.QueryRow("SELECT data, last_modified FROM profiles WHERE user_id = ? AND last_modified > ?", userId, req.Since).Scan(&profData, &profLastMod)
	if errProf == nil {
		profileOut = &ProfileSyncIn{Data: profData, LastModified: profLastMod}
	}

	pluginsOut := make([]PluginStateSyncOut, 0, 16)
	rowsPl, errPl := h.database.Query("SELECT id, plugin_id, manuscript_id, enabled, state, last_modified FROM plugin_states WHERE user_id = ? AND last_modified > ?", userId, req.Since)
	if errPl == nil {
		defer rowsPl.Close()
		for rowsPl.Next() {
			var pOut PluginStateSyncOut
			var msID sql.NullString
			var enabledInt int
			if errScan := rowsPl.Scan(&pOut.ID, &pOut.PluginID, &msID, &enabledInt, &pOut.State, &pOut.LastModified); errScan == nil {
				pOut.Enabled = (enabledInt != 0)
				if msID.Valid {
					pOut.ManuscriptID = &msID.String
				}
				pluginsOut = append(pluginsOut, pOut)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SyncResponse{
		ServerTime: serverTime,
		Pull: SyncPullOut{
			Manuscripts: manuscriptsOut,
			Chapters:    chaptersOut,
			Profile:     profileOut,
			Plugins:     pluginsOut,
		},
	})
}

// ============================================================================
// Sync V2 (Epoch & Cursor)
// ============================================================================

type SyncV2Request struct {
	Cursor int64             `json:"cursor"`
	Epoch  *string           `json:"epoch,omitempty"`
	Changes []json.RawMessage `json:"changes"`
}

type V2ChangeHeader struct {
	Entity    string `json:"entity"`
	Operation string `json:"operation"`
}

type V2Result struct {
	Entity       string      `json:"entity"`
	ID           string      `json:"id"`
	ManuscriptID string      `json:"manuscriptId,omitempty"`
	Status       string      `json:"status"` // "accepted" | "conflict"
	Reason       string      `json:"reason,omitempty"`
	Revision     int         `json:"revision"`
	Current      interface{} `json:"current"`
}

type SyncV2Response struct {
	Epoch   string        `json:"epoch"`
	Cursor  int64         `json:"cursor"`
	Results []V2Result    `json:"results"`
	Changes []interface{} `json:"changes"`
	HasMore bool          `json:"hasMore"`
	Reset   bool          `json:"reset,omitempty"`
}

type V2ManuscriptUpsert struct {
	Entity       string `json:"entity"`
	Operation    string `json:"operation"`
	ID           string `json:"id"`
	BaseRevision int    `json:"baseRevision"`
	Data         string `json:"data"`
}

type V2ChapterUpsert struct {
	Entity       string `json:"entity"`
	Operation    string `json:"operation"`
	ManuscriptID string `json:"manuscriptId"`
	ID           string `json:"id"`
	BaseRevision int    `json:"baseRevision"`
	Title        string `json:"title"`
	Content      string `json:"content"`
	Position     int    `json:"position"`
}

type V2ProfileUpsert struct {
	Entity       string `json:"entity"`
	Operation    string `json:"operation"`
	BaseRevision int    `json:"baseRevision"`
	Data         string `json:"data"`
}

func manuscriptTombstoneData(id string) string {
	return fmt.Sprintf(`{"id":"%s"}`, id)
}

func currentV2Record(database replica.Queryable, userId string, entity string, id string, manuscriptId string) (int, interface{}, error) {
	if entity == "manuscript" {
		var rawData string
		var lastModified int64
		var revision int
		var deletedAt sql.NullInt64

		err := database.QueryRow(`
			SELECT data, last_modified, revision, deleted_at FROM manuscripts
			 WHERE user_id = ? AND id = ?
		`, userId, id).Scan(&rawData, &lastModified, &revision, &deletedAt)

		if err == sql.ErrNoRows {
			return 0, nil, nil
		}
		if err != nil {
			return 0, nil, err
		}

		if deletedAt.Valid {
			return revision, map[string]interface{}{
				"entity":    entity,
				"id":        id,
				"operation": "delete",
				"revision":  revision,
				"updatedAt": lastModified,
			}, nil
		}

		// Parse the data field to structure it for JSON
		// `data` travels as the raw JSON STRING straight from the column, not
		// as a decoded object. That is the wire contract the Node server set
		// (`data: row.data`) and what clients expect — tests/formal does
		// JSON.parse(change.data). Decoding it here produced a nested object,
		// so JSON.parse got "[object Object]" and threw.

		return revision, map[string]interface{}{
			"entity":    entity,
			"id":        id,
			"operation": "upsert",
			"data":      rawData,
			"revision":  revision,
			"updatedAt": lastModified,
		}, nil
	}

	if entity == "chapter" {
		var title, content sql.NullString
		var pos sql.NullInt64
		var lastModified int64
		var deletedAt sql.NullInt64
		var revision int

		err := database.QueryRow(`
			SELECT title, content, position, last_modified, deleted_at, revision
			  FROM chapters
			 WHERE user_id = ? AND manuscript_id = ? AND id = ?
		`, userId, manuscriptId, id).Scan(&title, &content, &pos, &lastModified, &deletedAt, &revision)

		if err == sql.ErrNoRows {
			return 0, nil, nil
		}
		if err != nil {
			return 0, nil, err
		}

		if deletedAt.Valid {
			return revision, map[string]interface{}{
				"entity":       entity,
				"manuscriptId": manuscriptId,
				"id":           id,
				"operation":    "delete",
				"revision":     revision,
				"updatedAt":    lastModified,
			}, nil
		}

		var positionInt *int
		if pos.Valid {
			val := int(pos.Int64)
			positionInt = &val
		}

		return revision, map[string]interface{}{
			"entity":       entity,
			"manuscriptId": manuscriptId,
			"id":           id,
			"operation":    "upsert",
			"title":        title.String,
			"content":      content.String,
			"position":     positionInt,
			"revision":     revision,
			"updatedAt":    lastModified,
		}, nil
	}

	if entity == "profile" {
		var rawData string
		var lastModified int64
		var revision int

		err := database.QueryRow(`
			SELECT data, last_modified, revision FROM profiles WHERE user_id = ?
		`, userId).Scan(&rawData, &lastModified, &revision)

		if err == sql.ErrNoRows {
			return 0, nil, nil
		}
		if err != nil {
			return 0, nil, err
		}

		// `data` travels as the raw JSON STRING straight from the column, not
		// as a decoded object. That is the wire contract the Node server set
		// (`data: row.data`) and what clients expect — tests/formal does
		// JSON.parse(change.data). Decoding it here produced a nested object,
		// so JSON.parse got "[object Object]" and threw.

		return revision, map[string]interface{}{
			"entity":    entity,
			"id":        "profile",
			"operation": "upsert",
			"data":      rawData,
			"revision":  revision,
			"updatedAt": lastModified,
		}, nil
	}

	return 0, nil, nil
}

func (h *SyncHandler) syncV2(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var req SyncV2Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid sync v2 payload"})
		return
	}

	tx, err := h.database.Begin()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Database transaction start failed"})
		return
	}
	defer tx.Rollback()

	transactionEpoch, errEpoch := db.GetSyncHistoryEpoch(tx) // read through tx: it may lazily INSERT
	if errEpoch != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Epoch read failed"})
		return
	}

	var preMutationMaxCursor int64
	_ = tx.QueryRow("SELECT COALESCE(MAX(seq), 0) AS seq FROM change_log WHERE user_id = ?", userId).Scan(&preMutationMaxCursor)

	resetReason := ""
	if req.Epoch != nil && *req.Epoch != transactionEpoch {
		resetReason = "history_epoch_mismatch"
	} else if req.Cursor > preMutationMaxCursor {
		resetReason = "cursor_ahead_of_history"
	}

	historyResetRequired := resetReason != ""
	results := make([]V2Result, 0, len(req.Changes))

	if historyResetRequired {
		// Populate all changes as conflicts
		for _, rawCh := range req.Changes {
			var hdr V2ChangeHeader
			_ = json.Unmarshal(rawCh, &hdr)

			id := ""
			manuscriptId := ""

			if hdr.Entity == "manuscript" {
				var temp struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(rawCh, &temp)
				id = temp.ID
			} else if hdr.Entity == "chapter" {
				var temp struct {
					ID           string `json:"id"`
					ManuscriptID string `json:"manuscriptId"`
				}
				_ = json.Unmarshal(rawCh, &temp)
				id = temp.ID
				manuscriptId = temp.ManuscriptID
			} else if hdr.Entity == "profile" {
				id = "profile"
			}

			currRev, currVal, _ := currentV2Record(tx, userId, hdr.Entity, id, manuscriptId)

			results = append(results, V2Result{
				Entity:       hdr.Entity,
				ID:           id,
				ManuscriptID: manuscriptId,
				Status:       "conflict",
				Reason:       resetReason,
				Revision:     currRev,
				Current:      currVal,
			})
		}
	} else {
		// Mutate
		for _, rawCh := range req.Changes {
			var hdr V2ChangeHeader
			_ = json.Unmarshal(rawCh, &hdr)

			id := ""
			manuscriptId := ""
			baseRevision := 0

			if hdr.Entity == "manuscript" {
				var temp struct {
					ID           string `json:"id"`
					BaseRevision int    `json:"baseRevision"`
				}
				_ = json.Unmarshal(rawCh, &temp)
				id = temp.ID
				baseRevision = temp.BaseRevision
			} else if hdr.Entity == "chapter" {
				var temp struct {
					ID           string `json:"id"`
					ManuscriptID string `json:"manuscriptId"`
					BaseRevision int    `json:"baseRevision"`
				}
				_ = json.Unmarshal(rawCh, &temp)
				id = temp.ID
				manuscriptId = temp.ManuscriptID
				baseRevision = temp.BaseRevision
			} else if hdr.Entity == "profile" {
				var temp struct {
					BaseRevision int `json:"baseRevision"`
				}
				_ = json.Unmarshal(rawCh, &temp)
				id = "profile"
				baseRevision = temp.BaseRevision
			}

			currRev, currVal, errC := currentV2Record(tx, userId, hdr.Entity, id, manuscriptId)
			if errC != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": errC.Error()})
				return
			}

			if currRev != baseRevision {
				results = append(results, V2Result{
					Entity:       hdr.Entity,
					ID:           id,
					ManuscriptID: manuscriptId,
					Status:       "conflict",
					Revision:     currRev,
					Current:      currVal,
				})
				continue
			}

			newRevision := currRev + 1
			now := time.Now().UnixNano() / int64(time.Millisecond)

			if hdr.Entity == "manuscript" {
				if hdr.Operation == "upsert" {
					var chV2 V2ManuscriptUpsert
					_ = json.Unmarshal(rawCh, &chV2)

					_, errExec := tx.Exec(`
						INSERT INTO manuscripts (user_id, id, data, last_modified, deleted_at, revision)
						VALUES (?, ?, ?, ?, NULL, ?)
						ON CONFLICT(user_id, id) DO UPDATE SET
							data          = excluded.data,
							last_modified = excluded.last_modified,
							deleted_at    = NULL,
							revision      = excluded.revision
					`, userId, id, chV2.Data, now, newRevision)
					if errExec != nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						json.NewEncoder(w).Encode(map[string]string{"error": errExec.Error()})
						return
					}

					replData, _ := replica.SerializeManuscript(userId, id, now, newRevision, chV2.Data)
					_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/manuscript.json", userId, id), replData, "application/json")
				} else {
					// Delete
					_, errExec := tx.Exec(`
						UPDATE manuscripts
						   SET data = ?, deleted_at = ?, last_modified = ?, revision = ?
						 WHERE user_id = ? AND id = ?
					`, manuscriptTombstoneData(id), now, now, newRevision, userId, id)
					if errExec != nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						json.NewEncoder(w).Encode(map[string]string{"error": errExec.Error()})
						return
					}

					// Purge collab residues
					scopedPrefix := fmt.Sprintf("%s/%s:", url.QueryEscape(userId), id)
					_, _ = tx.Exec("DELETE FROM ydocs WHERE substr(name, 1, ?) = ?", len(scopedPrefix), scopedPrefix)
					legacyPrefix := fmt.Sprintf("%s:", id)
					_, _ = tx.Exec("DELETE FROM ydocs WHERE substr(name, 1, ?) = ?", len(legacyPrefix), legacyPrefix)
					_, _ = tx.Exec("DELETE FROM chapter_pre_collab WHERE user_id = ? AND manuscript_id = ?", userId, id)

					// Replica manuscript tombstone
					replData := replica.SerializeManuscriptTombstone(userId, id, now, newRevision)
					_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/manuscript.json", userId, id), replData, "application/json")

					// Tombstone active chapters
					rows, errCh := tx.Query("SELECT id, revision FROM chapters WHERE user_id = ? AND manuscript_id = ? AND deleted_at IS NULL", userId, id)
					if errCh == nil {
						type chTomb struct{ id string; rev int }
						var chs []chTomb
						for rows.Next() {
							var c chTomb
							if errScan := rows.Scan(&c.id, &c.rev); errScan == nil {
								chs = append(chs, c)
							}
						}
						rows.Close()

						for _, ch := range chs {
							chRev := ch.rev + 1
							_, _ = tx.Exec(`
								UPDATE chapters
								   SET title = NULL, content = NULL, position = NULL,
								       deleted_at = ?, last_modified = ?, revision = ?
								 WHERE user_id = ? AND manuscript_id = ? AND id = ?
							`, now, now, chRev, userId, id, ch.id)

							_, _ = db.RecordChange(tx, userId, "chapter", &id, ch.id, "delete", chRev, now)

							chReplData := replica.SerializeChapterTombstone(userId, id, ch.id, now, chRev)
							_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/chapters/%s.html", userId, id, ch.id), chReplData, "text/html; charset=utf-8")
						}
					}
				}
				_, _ = db.RecordChange(tx, userId, "manuscript", nil, id, hdr.Operation, newRevision, now)

			} else if hdr.Entity == "chapter" {
				if hdr.Operation == "upsert" {
					var chV2 V2ChapterUpsert
					_ = json.Unmarshal(rawCh, &chV2)

					var hasActiveMs int
					_ = tx.QueryRow("SELECT 1 FROM manuscripts WHERE user_id = ? AND id = ? AND deleted_at IS NULL", userId, manuscriptId).Scan(&hasActiveMs)

					if hasActiveMs == 0 {
						results = append(results, V2Result{
							Entity:       "chapter",
							ID:           id,
							ManuscriptID: manuscriptId,
							Status:       "conflict",
							Revision:     currRev,
							Current:      nil,
						})
						continue
					}

					_, errExec := tx.Exec(`
						INSERT INTO chapters (user_id, manuscript_id, id, title, content, position, last_modified, deleted_at, revision)
						VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)
						ON CONFLICT(user_id, manuscript_id, id) DO UPDATE SET
							title         = excluded.title,
							content       = excluded.content,
							position      = excluded.position,
							last_modified = excluded.last_modified,
							deleted_at    = NULL,
							revision      = excluded.revision
					`, userId, manuscriptId, id, chV2.Title, chV2.Content, chV2.Position, now, newRevision)
					if errExec != nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						json.NewEncoder(w).Encode(map[string]string{"error": errExec.Error()})
						return
					}

					replData := replica.SerializeChapter(userId, manuscriptId, id, chV2.Title, chV2.Position, now, newRevision, chV2.Content)
					_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/chapters/%s.html", userId, manuscriptId, id), replData, "text/html; charset=utf-8")
				} else {
					// Delete
					_, errExec := tx.Exec(`
						UPDATE chapters
						   SET title = NULL, content = NULL, position = NULL,
						       deleted_at = ?, last_modified = ?, revision = ?
						 WHERE user_id = ? AND manuscript_id = ? AND id = ?
					`, now, now, newRevision, userId, manuscriptId, id)
					if errExec != nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						json.NewEncoder(w).Encode(map[string]string{"error": errExec.Error()})
						return
					}

					// Purge chapter collab residues
					_, _ = tx.Exec("DELETE FROM ydocs WHERE name = ?", fmt.Sprintf("%s/%s:%s", url.QueryEscape(userId), manuscriptId, id))
					_, _ = tx.Exec("DELETE FROM ydocs WHERE name = ?", fmt.Sprintf("%s:%s", manuscriptId, id))
					_, _ = tx.Exec(`
						DELETE FROM chapter_pre_collab
						 WHERE user_id = ? AND manuscript_id = ? AND chapter_id = ?
					`, userId, manuscriptId, id)

					// Replica chapter tombstone
					replData := replica.SerializeChapterTombstone(userId, manuscriptId, id, now, newRevision)
					_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/chapters/%s.html", userId, manuscriptId, id), replData, "text/html; charset=utf-8")
				}

				_, _ = db.RecordChange(tx, userId, "chapter", &manuscriptId, id, hdr.Operation, newRevision, now)
				_, _ = db.TouchManuscriptForChapterChange(tx, userId, manuscriptId, now)

			} else if hdr.Entity == "profile" {
				var chV2 V2ProfileUpsert
				_ = json.Unmarshal(rawCh, &chV2)

				_, errExec := tx.Exec(`
					INSERT INTO profiles (user_id, data, last_modified, revision)
					VALUES (?, ?, ?, ?)
					ON CONFLICT(user_id) DO UPDATE SET
						data          = excluded.data,
						last_modified = excluded.last_modified,
						revision      = excluded.revision
				`, userId, chV2.Data, now, newRevision)
				if errExec != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": errExec.Error()})
					return
				}

				_, _ = db.RecordChange(tx, userId, "profile", nil, "profile", "upsert", newRevision, now)

				replData, _ := replica.SerializeProfile(userId, chV2.Data, now, newRevision)
				_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("profiles/%s", userId), replData, "application/json")
			}

			results = append(results, V2Result{
				Entity:       hdr.Entity,
				ID:           id,
				ManuscriptID: manuscriptId,
				Status:       "accepted",
				Revision:     newRevision,
			})
		}
	}

	if errCommit := tx.Commit(); errCommit != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to commit sync v2 transaction"})
		return
	}

	// Normalize revisions for conflicts (and any child mutations that touched parents)
	for idx, rVal := range results {
		currRev, currVal, _ := currentV2Record(h.database, userId, rVal.Entity, rVal.ID, rVal.ManuscriptID)
		results[idx].Revision = currRev
		if rVal.Status == "conflict" {
			results[idx].Current = currVal
		}
	}

	// Pull everything newer than req.Cursor
	pageSize := 1000
	var currentMaxCursor int64
	_ = h.database.QueryRow("SELECT COALESCE(MAX(seq), 0) AS seq FROM change_log WHERE user_id = ?", userId).Scan(&currentMaxCursor)

	pullCursor := req.Cursor
	if historyResetRequired {
		pullCursor = 0
	}

	rows, errQuery := h.database.Query(`
		SELECT seq, entity, manuscript_id, record_id
		  FROM change_log
		 WHERE user_id = ? AND seq > ?
		 ORDER BY seq ASC
		 LIMIT ?
	`, userId, pullCursor, pageSize+1)

	fetchedLogRows := make([]struct {
		seq          int64
		entity       string
		manuscriptID sql.NullString
		recordID     string
	}, 0, pageSize+1)

	if errQuery == nil {
		defer rows.Close()
		for rows.Next() {
			var r struct {
				seq          int64
				entity       string
				manuscriptID sql.NullString
				recordID     string
			}
			if errScan := rows.Scan(&r.seq, &r.entity, &r.manuscriptID, &r.recordID); errScan == nil {
				fetchedLogRows = append(fetchedLogRows, r)
			}
		}
	}

	hasMore := len(fetchedLogRows) > pageSize
	logRows := fetchedLogRows
	if hasMore {
		logRows = fetchedLogRows[:pageSize]
	}

	type latestKey struct {
		entity   string
		msID     string
		recordID string
	}
	latestMap := make(map[latestKey]int64)
	for _, row := range logRows {
		k := latestKey{entity: row.entity, msID: row.manuscriptID.String, recordID: row.recordID}
		latestMap[k] = row.seq
	}

	changes := make([]interface{}, 0, len(logRows))
	// To maintain order, iterate logRows but only add when it's the latest seq
	seen := make(map[latestKey]bool)
	for _, row := range logRows {
		k := latestKey{entity: row.entity, msID: row.manuscriptID.String, recordID: row.recordID}
		if latestMap[k] == row.seq && !seen[k] {
			seen[k] = true
			_, currVal, errRec := currentV2Record(h.database, userId, row.entity, row.recordID, row.manuscriptID.String)
			if errRec == nil && currVal != nil {
				changes = append(changes, currVal)
			}
		}
	}

	cursor := currentMaxCursor
	if len(logRows) > 0 {
		cursor = logRows[len(logRows)-1].seq
	}

	resp := SyncV2Response{
		Epoch:   transactionEpoch,
		Cursor:  cursor,
		Results: results,
		Changes: changes,
		HasMore: hasMore,
	}
	if historyResetRequired {
		resp.Reset = true
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// syncV2Bootstrap lets a newly authenticated browser adopt the current change
// cursor after its authoritative library/settings reads have completed. It
// avoids replaying the entire historical change log merely to learn a cursor.
func (h *SyncHandler) syncV2Bootstrap(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	if userId == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	epoch, err := db.GetSyncHistoryEpoch(h.database)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Epoch read failed"})
		return
	}
	var cursor int64
	if err := h.database.QueryRow("SELECT COALESCE(MAX(seq), 0) FROM change_log WHERE user_id = ?", userId).Scan(&cursor); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Cursor read failed"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"epoch": epoch, "cursor": cursor})
}
