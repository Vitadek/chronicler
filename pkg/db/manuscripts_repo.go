package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"chronicle-server/pkg/replica"
)

type ChapterRecord struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Content      string `json:"content"`
	LastModified int64  `json:"lastModified"`
	Revision     int    `json:"revision"`
}

type ManuscriptMetadata struct {
	ID           string                 `json:"id"`
	Title        string                 `json:"title"`
	Author       string                 `json:"author"`
	LastModified int64                  `json:"lastModified"`
	Revision     int                    `json:"revision"`
	ExtraFields  map[string]interface{} `json:"-"` // holds other custom metadata fields
}

// Custom json marshalling to flatten ExtraFields
func (m ManuscriptMetadata) MarshalJSON() ([]byte, error) {
	type Alias ManuscriptMetadata
	base, err := json.Marshal(Alias(m))
	if err != nil {
		return nil, err
	}
	if len(m.ExtraFields) == 0 {
		return base, nil
	}

	var merged map[string]interface{}
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range m.ExtraFields {
		if k != "id" && k != "title" && k != "author" && k != "lastModified" && k != "revision" {
			merged[k] = v
		}
	}
	return json.Marshal(merged)
}

func (m *ManuscriptMetadata) UnmarshalJSON(data []byte) error {
	type Alias ManuscriptMetadata
	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*m = ManuscriptMetadata(alias)

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	m.ExtraFields = make(map[string]interface{})
	for k, v := range raw {
		if k != "id" && k != "title" && k != "author" && k != "lastModified" && k != "revision" {
			m.ExtraFields[k] = v
		}
	}
	return nil
}

type ManuscriptRecord struct {
	Metadata ManuscriptMetadata `json:"metadata"`
	Chapters []ChapterRecord    `json:"chapters"`
}

type RecordConflict struct {
	Entity           string `json:"entity"` // "manuscript" | "chapter"
	ID               string `json:"id"`
	ManuscriptID     string `json:"manuscriptId,omitempty"`
	ExpectedRevision *int   `json:"expectedRevision,omitempty"`
	CurrentRevision  int    `json:"currentRevision"`
	Reason           string `json:"reason"` // "stale-revision" | "stale-timestamp" | "deleted" | "already-exists"
}

type SaveResult struct {
	Manuscript *ManuscriptRecord `json:"manuscript"`
	Conflicts  []RecordConflict  `json:"conflicts"`
}

func manuscriptTombstoneData(id string) string {
	return fmt.Sprintf(`{"id":"%s"}`, id)
}

func scopedCollabDocumentName(userId string, manuscriptId string, chapterId string) string {
	return fmt.Sprintf("%s/%s:%s", url.QueryEscape(userId), manuscriptId, chapterId)
}

func PurgeChapterCollaborationResidue(q replica.Queryable, userId string, manuscriptId string, chapterId string) {
	_, _ = q.Exec("DELETE FROM ydocs WHERE name = ?", scopedCollabDocumentName(userId, manuscriptId, chapterId))
	_, _ = q.Exec("DELETE FROM ydocs WHERE name = ?", fmt.Sprintf("%s:%s", manuscriptId, chapterId))
	_, _ = q.Exec(`
		DELETE FROM chapter_pre_collab
		 WHERE user_id = ? AND manuscript_id = ? AND chapter_id = ?
	`, userId, manuscriptId, chapterId)
}

func PurgeManuscriptCollaborationResidue(q replica.Queryable, userId string, manuscriptId string) {
	scopedPrefix := fmt.Sprintf("%s/%s:", url.QueryEscape(userId), manuscriptId)
	_, _ = q.Exec("DELETE FROM ydocs WHERE substr(name, 1, ?) = ?", len(scopedPrefix), scopedPrefix)

	legacyPrefix := fmt.Sprintf("%s:", manuscriptId)
	_, _ = q.Exec("DELETE FROM ydocs WHERE substr(name, 1, ?) = ?", len(legacyPrefix), legacyPrefix)

	_, _ = q.Exec("DELETE FROM chapter_pre_collab WHERE user_id = ? AND manuscript_id = ?", userId, manuscriptId)
}

func RecordChange(q replica.Queryable, userId string, entity string, manuscriptId *string, recordId string, operation string, revision int, changedAt int64) (int64, error) {
	var msIDVal interface{} = nil
	if manuscriptId != nil && *manuscriptId != "" {
		msIDVal = *manuscriptId
	}
	res, err := q.Exec(`
		INSERT INTO change_log (user_id, entity, manuscript_id, record_id, operation, revision, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, userId, entity, msIDVal, recordId, operation, revision, changedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func TouchManuscriptForChapterChange(q replica.Queryable, userId string, manuscriptId string, changedAt int64) (int, error) {
	var rawData string
	var lastModified int64
	var revision int
	var deletedAt sql.NullInt64

	err := q.QueryRow(`
		SELECT data, last_modified, revision, deleted_at FROM manuscripts
		WHERE user_id = ? AND id = ?
	`, userId, manuscriptId).Scan(&rawData, &lastModified, &revision, &deletedAt)

	if err != nil {
		return 0, err
	}
	if deletedAt.Valid {
		return 0, nil // Manuscript is deleted, do nothing
	}

	newRevision := revision + 1
	effectiveChangedAt := lastModified
	if changedAt > effectiveChangedAt {
		effectiveChangedAt = changedAt
	}

	var metadata map[string]interface{}
	if errJson := json.Unmarshal([]byte(rawData), &metadata); errJson != nil {
		return 0, errJson
	}
	metadata["lastModified"] = effectiveChangedAt

	dataBytes, _ := json.Marshal(metadata)
	dataStr := string(dataBytes)

	_, err = q.Exec(`
		UPDATE manuscripts SET data = ?, last_modified = ?, revision = ?
		WHERE user_id = ? AND id = ? AND deleted_at IS NULL
	`, dataStr, effectiveChangedAt, newRevision, userId, manuscriptId)
	if err != nil {
		return 0, err
	}

	_, _ = RecordChange(q, userId, "manuscript", nil, manuscriptId, "upsert", newRevision, effectiveChangedAt)

	// Enqueue S3/Nextcloud replica snapshot
	replData, errRepl := replica.SerializeManuscript(userId, manuscriptId, effectiveChangedAt, newRevision, dataStr)
	if errRepl == nil {
		_ = replica.EnqueueReplicaPut(q, fmt.Sprintf("manuscripts/%s/%s/manuscript.json", userId, manuscriptId), replData, "application/json")
	}

	return newRevision, nil
}

func LoadManuscript(database *sql.DB, userId string, id string) (*ManuscriptRecord, error) {
	var rawData string
	var lastModified int64
	var revision int
	var deletedAt sql.NullInt64

	err := database.QueryRow(`
		SELECT data, last_modified, revision, deleted_at
		  FROM manuscripts
		 WHERE user_id = ? AND id = ?
	`, userId, id).Scan(&rawData, &lastModified, &revision, &deletedAt)

	if err == sql.ErrNoRows || (err == nil && deletedAt.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, errQuery := database.Query(`
		SELECT id, title, content, position, last_modified, revision
		  FROM chapters
		 WHERE user_id = ? AND manuscript_id = ? AND deleted_at IS NULL
		 ORDER BY position ASC, last_modified ASC
	`, userId, id)
	if errQuery != nil {
		return nil, errQuery
	}
	defer rows.Close()

	var chapters []ChapterRecord
	for rows.Next() {
		var row struct {
			ID           string
			Title        sql.NullString
			Content      sql.NullString
			Position     sql.NullInt64
			LastModified int64
			Revision     int
		}
		if errScan := rows.Scan(&row.ID, &row.Title, &row.Content, &row.Position, &row.LastModified, &row.Revision); errScan != nil {
			return nil, errScan
		}
		chapters = append(chapters, ChapterRecord{
			ID:           row.ID,
			Title:        row.Title.String,
			Content:      row.Content.String,
			LastModified: row.LastModified,
			Revision:     row.Revision,
		})
	}

	var metadata ManuscriptMetadata
	if errUnmarshal := json.Unmarshal([]byte(rawData), &metadata); errUnmarshal != nil {
		return nil, errUnmarshal
	}

	metadata.ID = id
	metadata.LastModified = lastModified
	metadata.Revision = revision

	return &ManuscriptRecord{
		Metadata: metadata,
		Chapters: chapters,
	}, nil
}

func ListManuscripts(database *sql.DB, userId string) ([]ManuscriptMetadata, error) {
	rows, err := database.Query(`
		SELECT id, data, last_modified, revision
		  FROM manuscripts
		 WHERE user_id = ? AND deleted_at IS NULL
		 ORDER BY last_modified DESC
	`, userId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ManuscriptMetadata
	for rows.Next() {
		var id string
		var rawData string
		var lastModified int64
		var revision int

		if errScan := rows.Scan(&id, &rawData, &lastModified, &revision); errScan != nil {
			return nil, errScan
		}

		var m ManuscriptMetadata
		if errUnmarshal := json.Unmarshal([]byte(rawData), &m); errUnmarshal == nil {
			m.ID = id
			m.LastModified = lastModified
			m.Revision = revision
			list = append(list, m)
		}
	}
	return list, nil
}

func metadataForStorage(metadata ManuscriptMetadata) map[string]interface{} {
	clean := make(map[string]interface{})
	for k, v := range metadata.ExtraFields {
		clean[k] = v
	}
	clean["title"] = metadata.Title
	clean["author"] = metadata.Author
	return clean
}

func SaveLegacyManuscript(database *sql.DB, userId string, manuscript *ManuscriptRecord, createOnly bool) (*SaveResult, error) {
	manuscriptId := manuscript.Metadata.ID
	var conflicts []RecordConflict
	now := time.Now().UnixNano() / int64(time.Millisecond)

	tx, err := database.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var rawData string
	var lastModified int64
	var revision int
	var deletedAt sql.NullInt64

	errGet := tx.QueryRow(`
		SELECT data, last_modified, revision, deleted_at
		  FROM manuscripts
		 WHERE user_id = ? AND id = ?
	`, userId, manuscriptId).Scan(&rawData, &lastModified, &revision, &deletedAt)

	storedMetadata := metadataForStorage(manuscript.Metadata)
	storedDataBytes, _ := json.Marshal(storedMetadata)
	storedDataStr := string(storedDataBytes)

	if errGet == sql.ErrNoRows {
		_, errInsert := tx.Exec(`
			INSERT INTO manuscripts (user_id, id, data, last_modified, deleted_at, revision)
			VALUES (?, ?, ?, ?, NULL, 1)
		`, userId, manuscriptId, storedDataStr, now)
		if errInsert != nil {
			return nil, errInsert
		}
		_, _ = RecordChange(tx, userId, "manuscript", nil, manuscriptId, "upsert", 1, now)

		// Replica enqueue
		replData, _ := replica.SerializeManuscript(userId, manuscriptId, now, 1, storedDataStr)
		_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/manuscript.json", userId, manuscriptId), replData, "application/json")

	} else if errGet != nil {
		return nil, errGet
	} else if createOnly && !deletedAt.Valid {
		conflicts = append(conflicts, RecordConflict{
			Entity:          "manuscript",
			ID:              manuscriptId,
			CurrentRevision: revision,
			Reason:          "already-exists",
		})
	} else if deletedAt.Valid {
		conflicts = append(conflicts, RecordConflict{
			Entity:           "manuscript",
			ID:               manuscriptId,
			ExpectedRevision: &manuscript.Metadata.Revision,
			CurrentRevision:  revision,
			Reason:           "deleted",
		})
	} else {
		// Compare metadata payload equality
		var currDecoded map[string]interface{}
		_ = json.Unmarshal([]byte(rawData), &currDecoded)
		currDecodedBytes, _ := json.Marshal(currDecoded)
		
		identical := string(currDecodedBytes) == storedDataStr
		expected := manuscript.Metadata.Revision
		revisionMatches := expected == 0 || expected == revision
		legacyFresh := expected != 0 || manuscript.Metadata.LastModified > lastModified

		if identical {
			// No-op
		} else if !revisionMatches {
			conflicts = append(conflicts, RecordConflict{
				Entity:           "manuscript",
				ID:               manuscriptId,
				ExpectedRevision: &expected,
				CurrentRevision:  revision,
				Reason:           "stale-revision",
			})
		} else if !legacyFresh {
			conflicts = append(conflicts, RecordConflict{
				Entity:          "manuscript",
				ID:              manuscriptId,
				CurrentRevision: revision,
				Reason:          "stale-timestamp",
			})
		} else {
			newRevision := revision + 1
			_, errUpdate := tx.Exec(`
				UPDATE manuscripts
				   SET data = ?, last_modified = ?, deleted_at = NULL, revision = ?
				 WHERE user_id = ? AND id = ?
			`, storedDataStr, now, newRevision, userId, manuscriptId)
			if errUpdate != nil {
				return nil, errUpdate
			}
			_, _ = RecordChange(tx, userId, "manuscript", nil, manuscriptId, "upsert", newRevision, now)

			// Replica enqueue
			replData, _ := replica.SerializeManuscript(userId, manuscriptId, now, newRevision, storedDataStr)
			_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/manuscript.json", userId, manuscriptId), replData, "application/json")
		}
	}

	if (createOnly && errGet == nil) || (errGet == nil && deletedAt.Valid) {
		// Don't modify child records if creating duplicate or parent is deleted
		tx.Commit()
		current, _ := LoadManuscript(database, userId, manuscriptId)
		return &SaveResult{Manuscript: current, Conflicts: conflicts}, nil
	}

	// Process chapters
	chapterMutated := false
	for position, chapter := range manuscript.Chapters {
		var cTitle, cContent sql.NullString
		var cPosition sql.NullInt64
		var cLastModified int64
		var cDeletedAt sql.NullInt64
		var cRevision int

		errC := tx.QueryRow(`
			SELECT title, content, position, last_modified, deleted_at, revision
			  FROM chapters
			 WHERE user_id = ? AND manuscript_id = ? AND id = ?
		`, userId, manuscriptId, chapter.ID).Scan(&cTitle, &cContent, &cPosition, &cLastModified, &cDeletedAt, &cRevision)

		if errC == sql.ErrNoRows {
			_, errInsert := tx.Exec(`
				INSERT INTO chapters (user_id, manuscript_id, id, title, content, position, last_modified, deleted_at, revision)
				VALUES (?, ?, ?, ?, ?, ?, ?, NULL, 1)
			`, userId, manuscriptId, chapter.ID, chapter.Title, chapter.Content, position, now)
			if errInsert != nil {
				return nil, errInsert
			}
			_, _ = RecordChange(tx, userId, "chapter", &manuscriptId, chapter.ID, "upsert", 1, now)

			// Replica enqueue
			replData := replica.SerializeChapter(userId, manuscriptId, chapter.ID, chapter.Title, position, now, 1, chapter.Content)
			_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/chapters/%s.html", userId, manuscriptId, chapter.ID), replData, "text/html; charset=utf-8")

			chapterMutated = true
		} else if errC != nil {
			return nil, errC
		} else if cDeletedAt.Valid {
			conflicts = append(conflicts, RecordConflict{
				Entity:           "chapter",
				ID:               chapter.ID,
				ManuscriptID:     manuscriptId,
				ExpectedRevision: &chapter.Revision,
				CurrentRevision:  cRevision,
				Reason:           "deleted",
			})
		} else {
			identical := cTitle.String == chapter.Title && cContent.String == chapter.Content && cPosition.Int64 == int64(position)
			expected := chapter.Revision
			revisionMatches := expected == 0 || expected == cRevision
			legacyFresh := expected != 0 || chapter.LastModified > cLastModified

			if identical {
				// No-op
			} else if !revisionMatches {
				conflicts = append(conflicts, RecordConflict{
					Entity:           "chapter",
					ID:               chapter.ID,
					ManuscriptID:     manuscriptId,
					ExpectedRevision: &expected,
					CurrentRevision:  cRevision,
					Reason:           "stale-revision",
				})
			} else if !legacyFresh {
				conflicts = append(conflicts, RecordConflict{
					Entity:          "chapter",
					ID:              chapter.ID,
					ManuscriptID:    manuscriptId,
					CurrentRevision: cRevision,
					Reason:          "stale-timestamp",
				})
			} else {
				newRevision := cRevision + 1
				_, errUpdate := tx.Exec(`
					UPDATE chapters
					   SET title = ?, content = ?, position = ?, last_modified = ?, deleted_at = NULL, revision = ?
					 WHERE user_id = ? AND manuscript_id = ? AND id = ?
				`, chapter.Title, chapter.Content, position, now, newRevision, userId, manuscriptId, chapter.ID)
				if errUpdate != nil {
					return nil, errUpdate
				}
				_, _ = RecordChange(tx, userId, "chapter", &manuscriptId, chapter.ID, "upsert", newRevision, now)

				// Replica enqueue
				replData := replica.SerializeChapter(userId, manuscriptId, chapter.ID, chapter.Title, position, now, newRevision, chapter.Content)
				_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/chapters/%s.html", userId, manuscriptId, chapter.ID), replData, "text/html; charset=utf-8")

				chapterMutated = true
			}
		}
	}

	if chapterMutated {
		_, _ = TouchManuscriptForChapterChange(tx, userId, manuscriptId, now)
	}

	if errTxCommit := tx.Commit(); errTxCommit != nil {
		return nil, errTxCommit
	}

	// Normalize conflict versions
	for idx, conflict := range conflicts {
		var rev int
		if conflict.Entity == "manuscript" {
			_ = database.QueryRow("SELECT revision FROM manuscripts WHERE user_id = ? AND id = ?", userId, conflict.ID).Scan(&rev)
		} else {
			_ = database.QueryRow("SELECT revision FROM chapters WHERE user_id = ? AND manuscript_id = ? AND id = ?", userId, conflict.ManuscriptID, conflict.ID).Scan(&rev)
		}
		conflicts[idx].CurrentRevision = rev
	}

	current, errLoad := LoadManuscript(database, userId, manuscriptId)
	if errLoad != nil {
		return nil, errLoad
	}
	if current == nil && len(conflicts) == 0 {
		return nil, errors.New("Manuscript disappeared during save")
	}

	return &SaveResult{Manuscript: current, Conflicts: conflicts}, nil
}

func DeleteChapter(database *sql.DB, userId string, manuscriptId string, chapterId string, baseRevision *int) (bool, int, *int, int, error) {
	tx, err := database.Begin()
	if err != nil {
		return false, 0, nil, 0, err
	}
	defer tx.Rollback()

	var revision int
	var deletedAt sql.NullInt64
	errQuery := tx.QueryRow(`
		SELECT revision, deleted_at FROM chapters
		WHERE user_id = ? AND manuscript_id = ? AND id = ?
	`, userId, manuscriptId, chapterId).Scan(&revision, &deletedAt)

	if errQuery == sql.ErrNoRows {
		return false, 0, nil, 0, nil
	} else if errQuery != nil {
		return false, 0, nil, 0, errQuery
	}

	if deletedAt.Valid {
		_, _ = tx.Exec(`
			UPDATE chapters SET title = NULL, content = NULL, position = NULL
			WHERE user_id = ? AND manuscript_id = ? AND id = ?
		`, userId, manuscriptId, chapterId)
		PurgeChapterCollaborationResidue(tx, userId, manuscriptId, chapterId)

		var parentRevision int
		_ = tx.QueryRow("SELECT revision FROM manuscripts WHERE user_id = ? AND id = ?", userId, manuscriptId).Scan(&parentRevision)

		tx.Commit()
		return true, revision, &parentRevision, revision, nil
	}

	if baseRevision != nil && *baseRevision != revision {
		return false, 0, nil, revision, nil
	}

	newRevision := revision + 1
	now := time.Now().UnixNano() / int64(time.Millisecond)

	_, errUpdate := tx.Exec(`
		UPDATE chapters
		   SET title = NULL, content = NULL, position = NULL,
		       deleted_at = ?, last_modified = ?, revision = ?
		 WHERE user_id = ? AND manuscript_id = ? AND id = ?
	`, now, now, newRevision, userId, manuscriptId, chapterId)
	if errUpdate != nil {
		return false, 0, nil, 0, errUpdate
	}

	PurgeChapterCollaborationResidue(tx, userId, manuscriptId, chapterId)
	_, _ = RecordChange(tx, userId, "chapter", &manuscriptId, chapterId, "delete", newRevision, now)

	// Replica enqueue tombstone
	replData := replica.SerializeChapterTombstone(userId, manuscriptId, chapterId, now, newRevision)
	_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/chapters/%s.html", userId, manuscriptId, chapterId), replData, "text/html; charset=utf-8")

	manuscriptRevision, _ := TouchManuscriptForChapterChange(tx, userId, manuscriptId, now)

	if errCommit := tx.Commit(); errCommit != nil {
		return false, 0, nil, 0, errCommit
	}

	var finalMSRevision *int
	if manuscriptRevision > 0 {
		finalMSRevision = &manuscriptRevision
	}

	return true, newRevision, finalMSRevision, newRevision, nil
}

func DeleteManuscript(database *sql.DB, userId string, id string, baseRevision *int) (bool, int, int, error) {
	tx, err := database.Begin()
	if err != nil {
		return false, 0, 0, err
	}
	defer tx.Rollback()

	var revision int
	var deletedAt sql.NullInt64
	errQuery := tx.QueryRow(`
		SELECT revision, deleted_at FROM manuscripts WHERE user_id = ? AND id = ?
	`, userId, id).Scan(&revision, &deletedAt)

	if errQuery == sql.ErrNoRows {
		return false, 0, 0, nil
	} else if errQuery != nil {
		return false, 0, 0, errQuery
	}

	if deletedAt.Valid {
		_, _ = tx.Exec("UPDATE manuscripts SET data = ? WHERE user_id = ? AND id = ?", manuscriptTombstoneData(id), userId, id)
		_, _ = tx.Exec(`
			UPDATE chapters SET title = NULL, content = NULL, position = NULL
			WHERE user_id = ? AND manuscript_id = ? AND deleted_at IS NOT NULL
		`, userId, id)
		PurgeManuscriptCollaborationResidue(tx, userId, id)

		tx.Commit()
		return true, revision, revision, nil
	}

	if baseRevision != nil && *baseRevision != revision {
		return false, 0, revision, nil
	}

	now := time.Now().UnixNano() / int64(time.Millisecond)
	newRevision := revision + 1

	_, errUpdate := tx.Exec(`
		UPDATE manuscripts
		   SET data = ?, deleted_at = ?, last_modified = ?, revision = ?
		 WHERE user_id = ? AND id = ?
	`, manuscriptTombstoneData(id), now, now, newRevision, userId, id)
	if errUpdate != nil {
		return false, 0, 0, errUpdate
	}

	PurgeManuscriptCollaborationResidue(tx, userId, id)
	_, _ = RecordChange(tx, userId, "manuscript", nil, id, "delete", newRevision, now)

	// Replica enqueue tombstone
	replData := replica.SerializeManuscriptTombstone(userId, id, now, newRevision)
	_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/manuscript.json", userId, id), replData, "application/json")

	// Covers are opaque blobs, not sync records, so the tombstone above says
	// nothing about them — without this they outlive the manuscript both
	// locally and in the remote replica. Done inside this transaction so a
	// crash can never leave the manuscript deleted while its cover is still
	// published. Mirrors the Node server (portableReplica.ts's
	// deleteLocalBlobsByPrefix alongside the parent tombstone).
	_ = replica.EnqueueCoverDeletes(tx, userId, id)

	// Tombstone all live children
	rows, errQueryChapters := tx.Query(`
		SELECT id, revision FROM chapters
		WHERE user_id = ? AND manuscript_id = ? AND deleted_at IS NULL
	`, userId, id)
	if errQueryChapters == nil {
		type chEntry struct {
			id  string
			rev int
		}
		var list []chEntry
		for rows.Next() {
			var entry chEntry
			if errScan := rows.Scan(&entry.id, &entry.rev); errScan == nil {
				list = append(list, entry)
			}
		}
		rows.Close()

		tombstoneStmt, errStmt := tx.Prepare(`
			UPDATE chapters
			   SET title = NULL, content = NULL, position = NULL,
			       deleted_at = ?, last_modified = ?, revision = ?
			 WHERE user_id = ? AND manuscript_id = ? AND id = ?
		`)
		if errStmt == nil {
			defer tombstoneStmt.Close()
			for _, ch := range list {
				chNewRev := ch.rev + 1
				_, _ = tombstoneStmt.Exec(now, now, chNewRev, userId, id, ch.id)
				_, _ = RecordChange(tx, userId, "chapter", &id, ch.id, "delete", chNewRev, now)

				// Replica enqueue chapter tombstone
				chReplData := replica.SerializeChapterTombstone(userId, id, ch.id, now, chNewRev)
				_ = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/chapters/%s.html", userId, id, ch.id), chReplData, "text/html; charset=utf-8")
			}
		}
	}

	if errCommit := tx.Commit(); errCommit != nil {
		return false, 0, 0, errCommit
	}

	return true, newRevision, newRevision, nil
}
