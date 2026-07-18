package replica

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type RestoreManuscript struct {
	Key    string                      `json:"key"`
	Record PortableLiveManuscriptRecord `json:"record"`
}

type RestoreChapter struct {
	Key      string                      `json:"key"`
	Metadata PortableLiveChapterMetadata `json:"metadata"`
	Content  string                      `json:"content"`
	Deleted  bool                        `json:"deleted"`
}

type RestoreProfile struct {
	Key    string                `json:"key"`
	Record PortableProfileRecord `json:"record"`
}

type RestoreBlobRecord struct {
	RemoteKey   string `json:"remoteKey"`
	LocalKey    string `json:"localKey"`
	UserID      string `json:"userId"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Content     []byte `json:"-"`
}

type RestorePlan struct {
	Manuscripts []RestoreManuscript `json:"manuscripts"`
	Chapters    []RestoreChapter    `json:"chapters"`
	Profiles    []RestoreProfile    `json:"profiles"`
	Blobs       []RestoreBlobRecord `json:"blobs"`
	Ignored     []string            `json:"ignored"`
}

type RestoreApplyResult struct {
	CascadedChapters int `json:"cascadedChapters"`
	SkippedCovers    int `json:"skippedCovers"`
}

func decodeSegment(val string) string {
	decoded, err := url.QueryUnescape(val)
	if err != nil {
		return val
	}
	return decoded
}

func ParsePortableChapter(contentBytes []byte) (*PortableLiveChapterMetadata, string, bool, error) {
	marker := []byte("<body data-chronicle-content>\n")
	markerAt := bytes.Index(contentBytes, marker)
	if markerAt < 0 {
		return nil, "", false, errors.New("portable chapter is missing its content marker")
	}

	header := string(contentBytes[:markerAt])
	re := regexp.MustCompile(`data-chronicle-record="([A-Za-z0-9_-]+)"`)
	matches := re.FindStringSubmatch(header)
	if len(matches) < 2 {
		return nil, "", false, errors.New("portable chapter is missing its record metadata")
	}

	decodedBytes, err := base64.RawURLEncoding.DecodeString(matches[1])
	if err != nil {
		return nil, "", false, fmt.Errorf("failed to decode base64url record metadata: %w", err)
	}

	// Read envelope kind
	var kindCheck struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(decodedBytes, &kindCheck)

	if kindCheck.Kind == "chapter-tombstone" {
		var tomb PortableChapterTombstone
		if err := json.Unmarshal(decodedBytes, &tomb); err != nil {
			return nil, "", false, err
		}
		meta := PortableLiveChapterMetadata{
			SchemaVersion: tomb.SchemaVersion,
			Kind:          tomb.Kind,
			UserID:        tomb.UserID,
			ManuscriptID:  tomb.ManuscriptID,
			ID:            tomb.ID,
			Revision:      tomb.Revision,
			ContentBytes:  0,
		}
		return &meta, "", true, nil
	}

	var metadata PortableLiveChapterMetadata
	if err := json.Unmarshal(decodedBytes, &metadata); err != nil {
		return nil, "", false, fmt.Errorf("failed to unmarshal chapter metadata: %w", err)
	}

	contentStart := markerAt + len(marker)
	contentEnd := contentStart + metadata.ContentBytes
	suffix := []byte("\n</body>\n</html>\n")

	if contentEnd > len(contentBytes) {
		return nil, "", false, errors.New("portable chapter content is truncated")
	}

	if !bytes.Equal(contentBytes[contentEnd:], suffix) {
		return nil, "", false, errors.New("portable chapter is missing proper envelope suffix")
	}

	bodyProse := string(contentBytes[contentStart:contentEnd])
	return &metadata, bodyProse, false, nil
}

func PartitionRestoreBlobsForTombstones(manuscripts []RestoreManuscript, blobs []RestoreBlobRecord) ([]RestoreBlobRecord, []RestoreBlobRecord) {
	var deletedPrefixes []string
	for _, m := range manuscripts {
		if m.Record.Kind == "manuscript-tombstone" {
			deletedPrefixes = append(deletedPrefixes, fmt.Sprintf("covers/%s/%s.", m.Record.UserID, m.Record.ID))
		}
	}

	var accepted []RestoreBlobRecord
	var rejected []RestoreBlobRecord

	for _, b := range blobs {
		tombstoned := false
		for _, prefix := range deletedPrefixes {
			if strings.HasPrefix(b.LocalKey, prefix) {
				tombstoned = true
				break
			}
		}
		if tombstoned {
			rejected = append(rejected, b)
		} else {
			accepted = append(accepted, b)
		}
	}
	return accepted, rejected
}

func (m *Manager) BuildRestorePlan(ctx context.Context, userFilter string) (*RestorePlan, error) {
	if m.provider == nil {
		return nil, errors.New("remote replication is disabled")
	}

	objects, err := m.provider.List(ctx, "v1/users/")
	if err != nil {
		return nil, err
	}

	plan := &RestorePlan{
		Manuscripts: []RestoreManuscript{},
		Chapters:    []RestoreChapter{},
		Profiles:    []RestoreProfile{},
		Blobs:       []RestoreBlobRecord{},
		Ignored:     []string{},
	}

	manuscriptsRegex := regexp.MustCompile(`^v1/users/([^/]+)/manuscripts/([^/]+)/metadata\.json$`)
	chaptersRegex := regexp.MustCompile(`^v1/users/([^/]+)/manuscripts/([^/]+)/chapters/([^/]+)\.html$`)
	profileRegex := regexp.MustCompile(`^v1/users/([^/]+)/profile\.json$`)
	coversRegex := regexp.MustCompile(`^v1/users/([^/]+)/covers/([^/]+)$`)
	settingsRegex := regexp.MustCompile(`^v1/users/([^/]+)/settings\.json$`)

	seen := make(map[string]bool)

	for _, obj := range objects {
		key := obj.Key
		if seen[key] {
			return nil, fmt.Errorf("replica returned duplicate key: %s", key)
		}
		seen[key] = true

		// 1. Manuscript metadata
		if match := manuscriptsRegex.FindStringSubmatch(key); len(match) == 3 {
			pathUser := decodeSegment(match[1])
			pathId := decodeSegment(match[2])
			if userFilter != "" && pathUser != userFilter {
				continue
			}

			bytesVal, errGet := m.provider.Get(ctx, key)
			if errGet != nil {
				return nil, errGet
			}
			if len(bytesVal) > 100000 {
				return nil, fmt.Errorf("manuscript metadata is too large: %s", key)
			}

			var rec PortableLiveManuscriptRecord
			if errUn := json.Unmarshal(bytesVal, &rec); errUn != nil {
				return nil, fmt.Errorf("failed to unmarshal manuscript JSON: %w", errUn)
			}

			if rec.UserID != pathUser || rec.ID != pathId {
				return nil, fmt.Errorf("manuscript path and payload identity disagree: %s", key)
			}

			plan.Manuscripts = append(plan.Manuscripts, RestoreManuscript{Key: key, Record: rec})
			continue
		}

		// 2. Chapter metadata + content
		if match := chaptersRegex.FindStringSubmatch(key); len(match) == 4 {
			pathUser := decodeSegment(match[1])
			pathManuscript := decodeSegment(match[2])
			pathChapter := decodeSegment(match[3])
			if userFilter != "" && pathUser != userFilter {
				continue
			}

			bytesVal, errGet := m.provider.Get(ctx, key)
			if errGet != nil {
				return nil, errGet
			}
			if len(bytesVal) > 5100000 {
				return nil, fmt.Errorf("chapter object is too large: %s", key)
			}

			meta, prose, deleted, errParse := ParsePortableChapter(bytesVal)
			if errParse != nil {
				return nil, fmt.Errorf("failed to parse chapter html: %w", errParse)
			}

			if meta.UserID != pathUser || meta.ManuscriptID != pathManuscript || meta.ID != pathChapter {
				return nil, fmt.Errorf("chapter path and payload metadata disagree: %s", key)
			}

			plan.Chapters = append(plan.Chapters, RestoreChapter{
				Key:      key,
				Metadata: *meta,
				Content:  prose,
				Deleted:  deleted,
			})
			continue
		}

		// 3. Profile record
		if match := profileRegex.FindStringSubmatch(key); len(match) == 2 {
			pathUser := decodeSegment(match[1])
			if userFilter != "" && pathUser != userFilter {
				continue
			}

			bytesVal, errGet := m.provider.Get(ctx, key)
			if errGet != nil {
				return nil, errGet
			}
			if len(bytesVal) > 100000 {
				return nil, fmt.Errorf("profile object is too large: %s", key)
			}

			var rec PortableProfileRecord
			if errUn := json.Unmarshal(bytesVal, &rec); errUn != nil {
				return nil, fmt.Errorf("failed to unmarshal profile JSON: %w", errUn)
			}

			if rec.UserID != pathUser {
				return nil, fmt.Errorf("profile path and payload identity disagree: %s", key)
			}

			plan.Profiles = append(plan.Profiles, RestoreProfile{Key: key, Record: rec})
			continue
		}

		// 4. Cover blobs
		if match := coversRegex.FindStringSubmatch(key); len(match) == 3 {
			userId := decodeSegment(match[1])
			if userFilter != "" && userId != userFilter {
				continue
			}

			size := int64(0)
			if obj.Size != nil {
				size = *obj.Size
			}
			if size > 8*1024*1024 {
				return nil, fmt.Errorf("cover is too large: %s", key)
			}

			filename := decodeSegment(match[2])
			if !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(filename) {
				return nil, fmt.Errorf("invalid cover filename: %s", key)
			}

			ct := "image/jpeg"
			ext := strings.ToLower(filepathExtension(filename))
			if ext == "png" {
				ct = "image/png"
			} else if ext == "webp" {
				ct = "image/webp"
			}

			plan.Blobs = append(plan.Blobs, RestoreBlobRecord{
				RemoteKey:   key,
				LocalKey:    fmt.Sprintf("covers/%s/%s", userId, filename),
				UserID:      userId,
				ContentType: ct,
				Size:        size,
			})
			continue
		}

		// 5. Settings
		if match := settingsRegex.FindStringSubmatch(key); len(match) == 2 {
			userId := decodeSegment(match[1])
			if userFilter != "" && userId != userFilter {
				continue
			}

			size := int64(0)
			if obj.Size != nil {
				size = *obj.Size
			}
			if size > 128*1024 {
				return nil, fmt.Errorf("settings object is too large: %s", key)
			}

			plan.Blobs = append(plan.Blobs, RestoreBlobRecord{
				RemoteKey:   key,
				LocalKey:    fmt.Sprintf("settings/%s", userId),
				UserID:      userId,
				ContentType: "application/json",
				Size:        size,
			})
			continue
		}

		plan.Ignored = append(plan.Ignored, key)
	}

	// Validate child chapters have active manuscript parent
	manuscriptKinds := make(map[string]string)
	for _, m := range plan.Manuscripts {
		manuscriptKinds[m.Record.UserID+"\x00"+m.Record.ID] = m.Record.Kind
	}

	for _, ch := range plan.Chapters {
		parentKey := ch.Metadata.UserID + "\x00" + ch.Metadata.ManuscriptID
		parentKind, exists := manuscriptKinds[parentKey]
		if !exists {
			return nil, fmt.Errorf("chapter has no replicated manuscript metadata: %s", ch.Key)
		}
		if parentKind == "manuscript-tombstone" && !ch.Deleted {
			return nil, fmt.Errorf("live chapter belongs to a deleted manuscript: %s", ch.Key)
		}
	}

	acceptedBlobs, rejectedBlobs := PartitionRestoreBlobsForTombstones(plan.Manuscripts, plan.Blobs)
	plan.Blobs = acceptedBlobs
	for _, r := range rejectedBlobs {
		plan.Ignored = append(plan.Ignored, r.RemoteKey)
	}

	return plan, nil
}

func filepathExtension(filename string) string {
	parts := strings.Split(filename, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}

func (m *Manager) ApplyRestorePlan(plan *RestorePlan) (*RestoreApplyResult, error) {
	restoreVisibilityAt := time.Now().UnixNano()/int64(time.Millisecond) + 1
	acceptedBlobs, _ := PartitionRestoreBlobsForTombstones(plan.Manuscripts, plan.Blobs)

	tx, err := m.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 1. Insert or ignore users
	userIds := make(map[string]bool)
	for _, m := range plan.Manuscripts {
		userIds[m.Record.UserID] = true
	}
	for _, c := range plan.Chapters {
		userIds[c.Metadata.UserID] = true
	}
	for _, p := range plan.Profiles {
		userIds[p.Record.UserID] = true
	}
	for _, b := range acceptedBlobs {
		userIds[b.UserID] = true
	}

	nowMs := time.Now().UnixNano() / int64(time.Millisecond)
	for uid := range userIds {
		_, _ = tx.Exec("INSERT OR IGNORE INTO users(id, display_name, created_at) VALUES (?, ?, ?)", uid, "Restored User", nowMs)
	}

	// 2. Manuscripts upserts
	for _, ms := range plan.Manuscripts {
		var localLastModified int64
		var localRevision int
		errScan := tx.QueryRow("SELECT last_modified, revision FROM manuscripts WHERE user_id = ? AND id = ?", ms.Record.UserID, ms.Record.ID).Scan(&localLastModified, &localRevision)
		
		revision := ms.Record.Revision
		if errScan == nil {
			if ms.Record.Revision > localRevision {
				revision = ms.Record.Revision + 1
			} else {
				revision = localRevision + 1
			}
		}

		deleted := ms.Record.Kind == "manuscript-tombstone"
		sourceChangedAt := ms.Record.LastModified
		if deleted {
			sourceChangedAt = ms.Record.DeletedAt
		}

		changedAt := restoreVisibilityAt
		if sourceChangedAt > changedAt {
			changedAt = sourceChangedAt
		}
		if localLastModified > changedAt {
			changedAt = localLastModified
		}

		var dataStr string
		if deleted {
			tombData, _ := json.Marshal(map[string]string{"id": ms.Record.ID})
			dataStr = string(tombData)
		} else {
			metaBytes, _ := json.Marshal(ms.Record.Metadata)
			dataStr = string(metaBytes)
		}

		var delAt interface{} = nil
		if deleted {
			delAt = changedAt
		}

		_, errExec := tx.Exec(`
			INSERT INTO manuscripts(user_id, id, data, last_modified, deleted_at, revision)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, id) DO UPDATE SET
				data = excluded.data,
				last_modified = excluded.last_modified,
				deleted_at = excluded.deleted_at,
				revision = excluded.revision
		`, ms.Record.UserID, ms.Record.ID, dataStr, changedAt, delAt, revision)
		if errExec != nil {
			return nil, errExec
		}

		op := "upsert"
		if deleted {
			op = "delete"
		}
		_ = recordChange(tx, ms.Record.UserID, "manuscript", nil, ms.Record.ID, op, revision, changedAt)

		if deleted {
			purgeManuscriptCollab(tx, ms.Record.UserID, ms.Record.ID)
		}
	}

	// 3. Cascade chapters if manuscript deleted
	plannedChapterIds := make(map[string]bool)
	for _, ch := range plan.Chapters {
		plannedChapterIds[ch.Metadata.UserID+"\x00"+ch.Metadata.ManuscriptID+"\x00"+ch.Metadata.ID] = true
	}

	cascadedChapters := 0
	for _, ms := range plan.Manuscripts {
		if ms.Record.Kind != "manuscript-tombstone" {
			continue
		}

		rows, errQuery := tx.Query("SELECT id, last_modified, revision FROM chapters WHERE user_id = ? AND manuscript_id = ? AND deleted_at IS NULL", ms.Record.UserID, ms.Record.ID)
		if errQuery == nil {
			type childInfo struct {
				id           string
				lastModified int64
				revision     int
			}
			var children []childInfo
			for rows.Next() {
				var child childInfo
				if errScan := rows.Scan(&child.id, &child.lastModified, &child.revision); errScan == nil {
					children = append(children, child)
				}
			}
			rows.Close()

			for _, child := range children {
				chkKey := ms.Record.UserID + "\x00" + ms.Record.ID + "\x00" + child.id
				if plannedChapterIds[chkKey] {
					continue
				}

				changedAt := restoreVisibilityAt
				if ms.Record.DeletedAt > changedAt {
					changedAt = ms.Record.DeletedAt
				}
				if child.lastModified > changedAt {
					changedAt = child.lastModified
				}

				revision := child.revision + 1
				_, errCascade := tx.Exec(`
					UPDATE chapters
					   SET title = NULL, content = NULL, position = NULL,
					       last_modified = ?, deleted_at = ?, revision = ?
					 WHERE user_id = ? AND manuscript_id = ? AND id = ? AND deleted_at IS NULL
				`, changedAt, changedAt, revision, ms.Record.UserID, ms.Record.ID, child.id)
				if errCascade == nil {
					cascadedChapters++
					purgeChapterCollab(tx, ms.Record.UserID, ms.Record.ID, child.id)
					_ = recordChange(tx, ms.Record.UserID, "chapter", &ms.Record.ID, child.id, "delete", revision, changedAt)
				}
			}
		}
	}

	// 4. Chapters upserts
	for _, ch := range plan.Chapters {
		var localLastModified int64
		var localRevision int
		errScan := tx.QueryRow("SELECT last_modified, revision FROM chapters WHERE user_id = ? AND manuscript_id = ? AND id = ?", ch.Metadata.UserID, ch.Metadata.ManuscriptID, ch.Metadata.ID).Scan(&localLastModified, &localRevision)

		revision := ch.Metadata.Revision
		if errScan == nil {
			if ch.Metadata.Revision > localRevision {
				revision = ch.Metadata.Revision + 1
			} else {
				revision = localRevision + 1
			}
		}

		sourceChangedAt := ch.Metadata.LastModified
		if ch.Deleted {
			sourceChangedAt = ch.Metadata.LastModified // deletedAt stored there in meta struct mapping
		}

		changedAt := restoreVisibilityAt
		if sourceChangedAt > changedAt {
			changedAt = sourceChangedAt
		}
		if localLastModified > changedAt {
			changedAt = localLastModified
		}

		var titleVal interface{} = nil
		var contentVal interface{} = nil
		var positionVal interface{} = nil
		var delAt interface{} = nil

		if !ch.Deleted {
			titleVal = ch.Metadata.Title
			contentVal = ch.Content
			positionVal = ch.Metadata.Position
		} else {
			delAt = changedAt
		}

		_, errExec := tx.Exec(`
			INSERT INTO chapters(user_id, manuscript_id, id, title, content, position, last_modified, deleted_at, revision)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, manuscript_id, id) DO UPDATE SET
				title = excluded.title,
				content = excluded.content,
				position = excluded.position,
				last_modified = excluded.last_modified,
				deleted_at = excluded.deleted_at,
				revision = excluded.revision
		`, ch.Metadata.UserID, ch.Metadata.ManuscriptID, ch.Metadata.ID, titleVal, contentVal, positionVal, changedAt, delAt, revision)
		if errExec != nil {
			return nil, errExec
		}

		op := "upsert"
		if ch.Deleted {
			op = "delete"
		}
		_ = recordChange(tx, ch.Metadata.UserID, "chapter", &ch.Metadata.ManuscriptID, ch.Metadata.ID, op, revision, changedAt)

		if ch.Deleted {
			purgeChapterCollab(tx, ch.Metadata.UserID, ch.Metadata.ManuscriptID, ch.Metadata.ID)
		}
	}

	// 5. Profiles upserts
	for _, pr := range plan.Profiles {
		var localLastModified int64
		var localRevision int
		errScan := tx.QueryRow("SELECT last_modified, revision FROM profiles WHERE user_id = ?", pr.Record.UserID).Scan(&localLastModified, &localRevision)

		revision := pr.Record.Revision
		if errScan == nil {
			if pr.Record.Revision > localRevision {
				revision = pr.Record.Revision + 1
			} else {
				revision = localRevision + 1
			}
		}

		changedAt := restoreVisibilityAt
		if pr.Record.LastModified > changedAt {
			changedAt = pr.Record.LastModified
		}
		if localLastModified > changedAt {
			changedAt = localLastModified
		}

		prBytes, _ := json.Marshal(pr.Record.Profile)

		_, errExec := tx.Exec(`
			INSERT INTO profiles(user_id, data, last_modified, revision)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(user_id) DO UPDATE SET
				data = excluded.data,
				last_modified = excluded.last_modified,
				revision = excluded.revision
		`, pr.Record.UserID, string(prBytes), changedAt, revision)
		if errExec != nil {
			return nil, errExec
		}

		_ = recordChange(tx, pr.Record.UserID, "profile", nil, "profile", "upsert", revision, changedAt)
	}

	// 6. Write local blobs
	for _, blob := range acceptedBlobs {
		if blob.Content == nil {
			return nil, fmt.Errorf("restore blob was not hydrated: %s", blob.RemoteKey)
		}

		gen, errGen := NextStorageGeneration(tx, blob.LocalKey)
		if errGen != nil {
			return nil, errGen
		}
		checksum := Sha256(blob.Content)
		now := time.Now().UnixNano() / int64(time.Millisecond)

		_, errPut := tx.Exec(`
			INSERT INTO storage_blobs (key, content, content_type, generation, checksum, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				content = excluded.content,
				content_type = excluded.content_type,
				generation = excluded.generation,
				checksum = excluded.checksum,
				updated_at = excluded.updated_at
		`, blob.LocalKey, blob.Content, blob.ContentType, gen, checksum, now)
		if errPut != nil {
			return nil, errPut
		}

		errReplicate := EnqueueAtGeneration(tx, PortableReplicaKey(blob.LocalKey), "put", gen, blob.Content, blob.ContentType, checksum)
		if errReplicate != nil {
			return nil, errReplicate
		}
	}

	// 7. Rotate sync history epoch
	if errEpoch := rotateSyncHistoryEpoch(tx); errEpoch != nil {
		return nil, errEpoch
	}

	if errCommit := tx.Commit(); errCommit != nil {
		return nil, errCommit
	}

	return &RestoreApplyResult{
		CascadedChapters: cascadedChapters,
		SkippedCovers:    len(plan.Blobs) - len(acceptedBlobs),
	}, nil
}

func recordChange(q Queryable, userId string, entity string, manuscriptId *string, recordId string, operation string, revision int, changedAt int64) error {
	var mId interface{} = nil
	if manuscriptId != nil && *manuscriptId != "" {
		mId = *manuscriptId
	}
	_, err := q.Exec(`
		INSERT INTO sync_changes (user_id, entity, manuscript_id, record_id, operation, revision, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, userId, entity, mId, recordId, operation, revision, changedAt)
	return err
}

func purgeChapterCollab(q Queryable, userId string, manuscriptId string, chapterId string) {
	name1 := fmt.Sprintf("%s/%s:%s", url.QueryEscape(userId), manuscriptId, chapterId)
	name2 := fmt.Sprintf("%s:%s", manuscriptId, chapterId)
	_, _ = q.Exec("DELETE FROM ydocs WHERE name = ?", name1)
	_, _ = q.Exec("DELETE FROM ydocs WHERE name = ?", name2)
	_, _ = q.Exec(`
		DELETE FROM chapter_pre_collab
		 WHERE user_id = ? AND manuscript_id = ? AND chapter_id = ?
	`, userId, manuscriptId, chapterId)
}

func purgeManuscriptCollab(q Queryable, userId string, manuscriptId string) {
	scopedPrefix := fmt.Sprintf("%s/%s:", url.QueryEscape(userId), manuscriptId)
	_, _ = q.Exec("DELETE FROM ydocs WHERE substr(name, 1, ?) = ?", len(scopedPrefix), scopedPrefix)

	legacyPrefix := fmt.Sprintf("%s:", manuscriptId)
	_, _ = q.Exec("DELETE FROM ydocs WHERE substr(name, 1, ?) = ?", len(legacyPrefix), legacyPrefix)

	_, _ = q.Exec("DELETE FROM chapter_pre_collab WHERE user_id = ? AND manuscript_id = ?", userId, manuscriptId)
}

func rotateSyncHistoryEpoch(q Queryable) error {
	next := fmt.Sprintf("%d", time.Now().UnixNano())
	_, err := q.Exec(`
		INSERT INTO kv(k, v, expires_at) VALUES ('sync:history-epoch:v2', ?, NULL)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v, expires_at = NULL
	`, next)
	return err
}
