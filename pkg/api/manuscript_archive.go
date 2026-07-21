package api

import (
	"archive/zip"
	"compress/flate"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"chronicle-server/pkg/auth"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"
	"chronicle-server/pkg/replica"
)

const (
	manuscriptArchiveFormat            = "ink.chronicler.manuscripts"
	manuscriptArchiveVersion           = 1
	manuscriptArchiveMime              = "application/vnd.chronicler.manuscripts+zip"
	maxManuscriptArchiveBytes    int64 = 256 * 1024 * 1024
	maxManuscriptArchiveExpanded int64 = 2 * 1024 * 1024 * 1024
	maxArchivedManuscriptBytes   int64 = 512 * 1024 * 1024
	maxArchiveManifestBytes      int64 = 4 * 1024 * 1024
	maxArchiveEntries                  = 20000
)

type manuscriptArchiveManifest struct {
	Format          string                  `json:"format"`
	Version         int                     `json:"version"`
	CreatedAt       string                  `json:"createdAt"`
	Compression     string                  `json:"compression"`
	ManuscriptCount int                     `json:"manuscriptCount"`
	Manuscripts     []manuscriptArchiveItem `json:"manuscripts"`
}

type manuscriptArchiveItem struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Path             string `json:"path"`
	CoverPath        string `json:"coverPath,omitempty"`
	CoverContentType string `json:"coverContentType,omitempty"`
}

type archivedManuscript struct {
	item      manuscriptArchiveItem
	record    *db.ManuscriptRecord
	cover     []byte
	coverMime string
	coverExt  string
	sourceID  string
}

type manuscriptArchiveImportItem struct {
	SourceID     string `json:"sourceId"`
	ID           string `json:"id"`
	Title        string `json:"title"`
	Copied       bool   `json:"copied"`
	IDReassigned bool   `json:"idReassigned"`
	TitleRenamed bool   `json:"titleRenamed"`
}

type manuscriptArchiveImportResult struct {
	Imported      int                           `json:"imported"`
	Renamed       int                           `json:"renamed"`
	IDsReassigned int                           `json:"idsReassigned"`
	Covers        int                           `json:"covers"`
	Atomic        bool                          `json:"atomic"`
	FormatVersion int                           `json:"formatVersion"`
	Compression   string                        `json:"compression"`
	Log           []string                      `json:"log"`
	Manuscripts   []manuscriptArchiveImportItem `json:"manuscripts"`
}

type manuscriptArchiveErrorResponse struct {
	Error      string   `json:"error"`
	Code       string   `json:"code"`
	Stage      string   `json:"stage"`
	Detail     string   `json:"detail,omitempty"`
	RolledBack bool     `json:"rolledBack"`
	Imported   int      `json:"imported"`
	Retryable  bool     `json:"retryable"`
	Log        []string `json:"log"`
}

type ManuscriptArchiveHandler struct {
	cfg      *config.Config
	database *sql.DB
}

func NewManuscriptArchiveHandler(cfg *config.Config, database *sql.DB) *ManuscriptArchiveHandler {
	return &ManuscriptArchiveHandler{cfg: cfg, database: database}
}

func archiveError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func archiveImportError(w http.ResponseWriter, status int, response manuscriptArchiveErrorResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func archiveFilename() string {
	return fmt.Sprintf("chronicler-manuscripts-%s.chron", time.Now().Format("2006-01-02"))
}

func writeArchiveEntry(zw *zip.Writer, name string, method uint16, body []byte) error {
	header := &zip.FileHeader{Name: name, Method: method}
	header.SetModTime(time.Now().UTC())
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func coverReference(record *db.ManuscriptRecord) string {
	if record == nil || record.Metadata.ExtraFields == nil {
		return ""
	}
	name, _ := record.Metadata.ExtraFields["coverArt"].(string)
	if strings.ContainsAny(name, `/\\`) {
		return ""
	}
	return name
}

func (h *ManuscriptArchiveHandler) writeArchive(out io.Writer, userID string) error {
	list, err := db.ListManuscripts(h.database, userID)
	if err != nil {
		return err
	}

	manifest := manuscriptArchiveManifest{
		Format:          manuscriptArchiveFormat,
		Version:         manuscriptArchiveVersion,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		Compression:     "zip-deflate",
		ManuscriptCount: len(list),
		Manuscripts:     make([]manuscriptArchiveItem, 0, len(list)),
	}
	payloads := make([]archivedManuscript, 0, len(list))
	for index, metadata := range list {
		record, loadErr := db.LoadManuscript(h.database, userID, metadata.ID)
		if loadErr != nil {
			return loadErr
		}
		if record == nil {
			continue
		}
		item := manuscriptArchiveItem{
			ID:    record.Metadata.ID,
			Title: record.Metadata.Title,
			Path:  fmt.Sprintf("manuscripts/%06d.json", index+1),
		}
		payload := archivedManuscript{item: item, record: record}

		if coverName := coverReference(record); coverName != "" {
			key := fmt.Sprintf("covers/%s/%s", userID, coverName)
			var content []byte
			var contentType string
			if queryErr := h.database.QueryRow(
				"SELECT content, content_type FROM storage_blobs WHERE key = ?", key,
			).Scan(&content, &contentType); queryErr == nil {
				_, ext, ok := sniffImage(content)
				if ok {
					item.CoverPath = fmt.Sprintf("covers/%06d.%s", index+1, ext)
					item.CoverContentType = contentType
					payload.cover = content
					payload.item = item
				}
			} else if queryErr != sql.ErrNoRows {
				return queryErr
			}
		}
		manifest.Manuscripts = append(manifest.Manuscripts, item)
		payloads = append(payloads, payload)
	}
	manifest.ManuscriptCount = len(payloads)

	zw := zip.NewWriter(out)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		// Level 6 is the mature default balance for prose: nearly level-9 size
		// without making multi-million-word exports feel like an archival job.
		return flate.NewWriter(w, flate.DefaultCompression)
	})
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err = writeArchiveEntry(zw, "manifest.json", zip.Deflate, append(manifestJSON, '\n')); err != nil {
		return err
	}
	for _, payload := range payloads {
		body, marshalErr := json.Marshal(payload.record)
		if marshalErr != nil {
			return marshalErr
		}
		if err = writeArchiveEntry(zw, payload.item.Path, zip.Deflate, append(body, '\n')); err != nil {
			return err
		}
		if len(payload.cover) > 0 {
			// PNG/JPEG/WebP data is already compressed; storing it avoids wasted CPU.
			if err = writeArchiveEntry(zw, payload.item.CoverPath, zip.Store, payload.cover); err != nil {
				return err
			}
		}
	}
	return zw.Close()
}

func (h *ManuscriptArchiveHandler) GetExport(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	if userID == "" {
		archiveError(w, http.StatusUnauthorized, "Not authenticated")
		return
	}
	tmp, err := os.CreateTemp(h.cfg.DataDir, "manuscript-export-*.chron")
	if err != nil {
		archiveError(w, http.StatusInternalServerError, "Could not create the manuscript archive")
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err = h.writeArchive(tmp, userID); err != nil {
		tmp.Close()
		archiveError(w, http.StatusInternalServerError, "Could not export manuscripts: "+err.Error())
		return
	}
	if err = tmp.Close(); err != nil {
		archiveError(w, http.StatusInternalServerError, "Could not finish the manuscript archive")
		return
	}
	file, err := os.Open(tmpName)
	if err != nil {
		archiveError(w, http.StatusInternalServerError, "Could not read the manuscript archive")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		archiveError(w, http.StatusInternalServerError, "Could not inspect the manuscript archive")
		return
	}
	w.Header().Set("Content-Type", manuscriptArchiveMime)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, archiveFilename()))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, archiveFilename(), info.ModTime(), file)
}

func validArchivePath(name string) bool {
	return name != "" && !strings.Contains(name, "\\") && !strings.HasPrefix(name, "/") && path.Clean(name) == name && name != "." && !strings.HasPrefix(name, "../")
}

func readArchiveFile(file *zip.File, maxBytes int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(maxBytes) {
		return nil, fmt.Errorf("%s is too large", file.Name)
	}
	r, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%s is too large", file.Name)
	}
	return body, nil
}

func decodeArchive(tempPath string) ([]archivedManuscript, error) {
	zr, err := zip.OpenReader(tempPath)
	if err != nil {
		return nil, errors.New("not a valid .chron ZIP archive")
	}
	defer zr.Close()
	if len(zr.File) == 0 || len(zr.File) > maxArchiveEntries {
		return nil, errors.New("the archive has an invalid number of files")
	}
	files := make(map[string]*zip.File, len(zr.File))
	var expanded uint64
	for _, file := range zr.File {
		if !validArchivePath(file.Name) {
			return nil, fmt.Errorf("the archive contains an unsafe path %q", file.Name)
		}
		if _, duplicate := files[file.Name]; duplicate {
			return nil, fmt.Errorf("the archive contains duplicate path %q", file.Name)
		}
		files[file.Name] = file
		expanded += file.UncompressedSize64
		if expanded > uint64(maxManuscriptArchiveExpanded) {
			return nil, errors.New("the expanded archive is too large")
		}
	}
	manifestFile := files["manifest.json"]
	if manifestFile == nil {
		return nil, errors.New("the archive is missing manifest.json")
	}
	manifestBytes, err := readArchiveFile(manifestFile, maxArchiveManifestBytes)
	if err != nil {
		return nil, err
	}
	var manifest manuscriptArchiveManifest
	if err = json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, errors.New("the archive manifest is invalid")
	}
	if manifest.Format != manuscriptArchiveFormat || manifest.Version != manuscriptArchiveVersion {
		return nil, fmt.Errorf("unsupported .chron format or version: archive is %q v%d; this Chronicler release accepts %q v%d only", manifest.Format, manifest.Version, manuscriptArchiveFormat, manuscriptArchiveVersion)
	}
	if manifest.Compression != "zip-deflate" {
		return nil, fmt.Errorf("unsupported .chron compression %q", manifest.Compression)
	}
	if manifest.ManuscriptCount != len(manifest.Manuscripts) || len(manifest.Manuscripts) > maxArchiveEntries-1 {
		return nil, errors.New("the archive manuscript count is inconsistent")
	}

	seenIDs := make(map[string]bool, len(manifest.Manuscripts))
	seenPaths := make(map[string]bool, len(manifest.Manuscripts)*2)
	decoded := make([]archivedManuscript, 0, len(manifest.Manuscripts))
	for _, item := range manifest.Manuscripts {
		if item.ID == "" || seenIDs[item.ID] || seenPaths[item.Path] || !strings.HasPrefix(item.Path, "manuscripts/") {
			return nil, errors.New("the archive contains duplicate or invalid manuscript references")
		}
		seenIDs[item.ID] = true
		seenPaths[item.Path] = true
		file := files[item.Path]
		if file == nil {
			return nil, fmt.Errorf("the archive is missing %s", item.Path)
		}
		body, readErr := readArchiveFile(file, maxArchivedManuscriptBytes)
		if readErr != nil {
			return nil, readErr
		}
		var record db.ManuscriptRecord
		if err = json.Unmarshal(body, &record); err != nil || record.Metadata.ID == "" || record.Metadata.ID != item.ID {
			return nil, fmt.Errorf("%s is not a valid manuscript", item.Path)
		}
		seenChapterIDs := make(map[string]bool, len(record.Chapters))
		for chapterIndex, chapter := range record.Chapters {
			if strings.TrimSpace(chapter.ID) == "" {
				return nil, fmt.Errorf("%s has an empty chapter id at position %d", item.Path, chapterIndex+1)
			}
			if seenChapterIDs[chapter.ID] {
				return nil, fmt.Errorf("%s contains duplicate chapter id %q", item.Path, chapter.ID)
			}
			seenChapterIDs[chapter.ID] = true
		}
		payload := archivedManuscript{item: item, record: &record, sourceID: item.ID}
		if item.CoverPath != "" {
			if seenPaths[item.CoverPath] || !strings.HasPrefix(item.CoverPath, "covers/") {
				return nil, errors.New("the archive contains a duplicate or invalid cover reference")
			}
			seenPaths[item.CoverPath] = true
			coverFile := files[item.CoverPath]
			if coverFile == nil {
				return nil, fmt.Errorf("the archive is missing %s", item.CoverPath)
			}
			payload.cover, err = readArchiveFile(coverFile, maxCoverBytes)
			if err != nil {
				return nil, err
			}
			payload.coverMime, payload.coverExt, _ = sniffImage(payload.cover)
			if payload.coverMime == "" {
				return nil, fmt.Errorf("%s is not a supported cover image", item.CoverPath)
			}
		}
		decoded = append(decoded, payload)
	}
	return decoded, nil
}

var portableManuscriptID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func randomArchiveToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func manuscriptExists(database replica.Queryable, userID string, id string) (bool, error) {
	var one int
	err := database.QueryRow("SELECT 1 FROM manuscripts WHERE user_id = ? AND id = ?", userID, id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func allocateImportedManuscriptID(database replica.Queryable, userID, preferred string, reserved map[string]bool) (string, bool, error) {
	if portableManuscriptID.MatchString(preferred) && !reserved[preferred] {
		exists, err := manuscriptExists(database, userID, preferred)
		if err != nil {
			return "", false, err
		}
		if !exists {
			reserved[preferred] = true
			return preferred, false, nil
		}
	}
	for attempt := 0; attempt < 20; attempt++ {
		token, err := randomArchiveToken(9)
		if err != nil {
			return "", false, err
		}
		candidate := "imported-" + token
		if reserved[candidate] {
			continue
		}
		exists, err := manuscriptExists(database, userID, candidate)
		if err != nil {
			return "", false, err
		}
		if !exists {
			reserved[candidate] = true
			return candidate, true, nil
		}
	}
	return "", false, errors.New("could not allocate a manuscript id")
}

func importedCopyTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return "Imported manuscript"
	}
	return title + " (Imported copy)"
}

func existingImportTitles(tx *sql.Tx, userID string) (map[string]int, error) {
	rows, err := tx.Query("SELECT data FROM manuscripts WHERE user_id = ? AND deleted_at IS NULL", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	titles := make(map[string]int)
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var metadata db.ManuscriptMetadata
		if err = json.Unmarshal([]byte(raw), &metadata); err != nil {
			return nil, fmt.Errorf("existing manuscript metadata is invalid: %w", err)
		}
		titles[strings.ToLower(strings.TrimSpace(metadata.Title))]++
	}
	return titles, rows.Err()
}

func allocateImportedTitle(title string, used map[string]int) (string, bool) {
	base := strings.TrimSpace(title)
	if base == "" {
		base = "Imported manuscript"
	}
	key := strings.ToLower(base)
	if used[key] == 0 {
		used[key] = 1
		return base, false
	}
	for copyNumber := 1; ; copyNumber++ {
		candidate := importedCopyTitle(base)
		if copyNumber > 1 {
			candidate = fmt.Sprintf("%s (Imported copy %d)", base, copyNumber)
		}
		candidateKey := strings.ToLower(candidate)
		if used[candidateKey] == 0 {
			used[candidateKey] = 1
			return candidate, true
		}
	}
}

func storeImportedCover(tx *sql.Tx, userID, filename, mime string, content []byte) error {
	key := fmt.Sprintf("covers/%s/%s", userID, filename)
	generation, err := replica.NextStorageGeneration(tx, key)
	if err != nil {
		return err
	}
	checksum := replica.Sha256(content)
	now := time.Now().UnixNano() / int64(time.Millisecond)
	if _, err = tx.Exec(`
		INSERT INTO storage_blobs (key, content, content_type, generation, checksum, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, key, content, mime, generation, checksum, now); err != nil {
		return err
	}
	if err = replica.EnqueueAtGeneration(tx, replica.PortableReplicaKey(key), "put", generation, content, mime, checksum); err != nil {
		return err
	}
	return nil
}

func (h *ManuscriptArchiveHandler) importArchive(userID string, archived []archivedManuscript) (*manuscriptArchiveImportResult, error) {
	result := &manuscriptArchiveImportResult{
		Atomic: true, FormatVersion: manuscriptArchiveVersion, Compression: "zip-deflate",
		Log: []string{
			fmt.Sprintf("Validated .chron format v%d and %d manuscript payload(s).", manuscriptArchiveVersion, len(archived)),
			"Opened one atomic SQLite transaction for manuscripts, chapters, covers, change history, and replica jobs.",
		},
		Manuscripts: make([]manuscriptArchiveImportItem, 0, len(archived)),
	}
	tx, err := h.database.Begin()
	if err != nil {
		return nil, fmt.Errorf("open atomic database transaction: %w", err)
	}
	defer tx.Rollback()

	usedTitles, err := existingImportTitles(tx, userID)
	if err != nil {
		return nil, fmt.Errorf("inspect destination manuscript titles: %w", err)
	}
	reserved := make(map[string]bool, len(archived))
	for index := range archived {
		payload := &archived[index]
		destID, idReassigned, err := allocateImportedManuscriptID(tx, userID, payload.sourceID, reserved)
		if err != nil {
			return nil, fmt.Errorf("allocate destination id for archive manuscript %d (%q): %w", index+1, payload.sourceID, err)
		}
		payload.record.Metadata.ID = destID
		payload.record.Metadata.Revision = 0
		var titleRenamed bool
		payload.record.Metadata.Title, titleRenamed = allocateImportedTitle(payload.record.Metadata.Title, usedTitles)
		for chapterIndex := range payload.record.Chapters {
			payload.record.Chapters[chapterIndex].Revision = 0
		}
		if payload.record.Metadata.ExtraFields == nil {
			payload.record.Metadata.ExtraFields = make(map[string]interface{})
		}
		delete(payload.record.Metadata.ExtraFields, "coverArt")
		var coverFilename string
		if len(payload.cover) > 0 {
			token, tokenErr := randomArchiveToken(6)
			if tokenErr != nil {
				return nil, fmt.Errorf("allocate cover filename for manuscript %q: %w", destID, tokenErr)
			}
			coverFilename = fmt.Sprintf("%s.%s.%s", destID, token, payload.coverExt)
			payload.record.Metadata.ExtraFields["coverArt"] = coverFilename
		}

		if saveErr := db.InsertImportedManuscript(tx, userID, payload.record, time.Now().UnixMilli()); saveErr != nil {
			return nil, fmt.Errorf("store archive manuscript %d of %d (%q): %w", index+1, len(archived), payload.sourceID, saveErr)
		}
		if coverFilename != "" {
			if err = storeImportedCover(tx, userID, coverFilename, payload.coverMime, payload.cover); err != nil {
				return nil, fmt.Errorf("store cover for archive manuscript %d of %d (%q): %w", index+1, len(archived), payload.sourceID, err)
			}
			result.Covers++
		}
		result.Imported++
		if idReassigned {
			result.IDsReassigned++
		}
		if titleRenamed {
			result.Renamed++
		}
		result.Log = append(result.Log, fmt.Sprintf(
			"Prepared manuscript %d/%d: %q (destination id %q, %d chapter(s), cover: %t, id reassigned: %t, title renamed: %t).",
			index+1, len(archived), payload.record.Metadata.Title, destID, len(payload.record.Chapters), coverFilename != "", idReassigned, titleRenamed,
		))
		result.Manuscripts = append(result.Manuscripts, manuscriptArchiveImportItem{
			SourceID: payload.sourceID, ID: destID, Title: payload.record.Metadata.Title,
			Copied: idReassigned || titleRenamed, IDReassigned: idReassigned, TitleRenamed: titleRenamed,
		})
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit atomic database transaction: %w", err)
	}
	result.Log = append(result.Log, fmt.Sprintf(
		"Committed successfully: %d manuscript(s), %d cover(s), %d reassigned id(s), and %d renamed title(s). No existing manuscript was overwritten.",
		result.Imported, result.Covers, result.IDsReassigned, result.Renamed,
	))
	return result, nil
}

func (h *ManuscriptArchiveHandler) PostImport(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	if userID == "" {
		archiveError(w, http.StatusUnauthorized, "Not authenticated")
		return
	}
	tmp, err := os.CreateTemp(h.cfg.DataDir, "manuscript-import-*.chron")
	if err != nil {
		archiveImportError(w, http.StatusInternalServerError, manuscriptArchiveErrorResponse{
			Error: "The .chron upload could not be staged.", Code: "staging_failed", Stage: "upload",
			Detail: err.Error(), RolledBack: true, Imported: 0, Retryable: true,
			Log: []string{"Could not create a protected temporary archive file.", "The database transaction never started; no data was changed."},
		})
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	reader := http.MaxBytesReader(w, r.Body, maxManuscriptArchiveBytes)
	written, copyErr := io.Copy(tmp, reader)
	closeErr := tmp.Close()
	if copyErr != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(copyErr, &tooLarge) {
			archiveImportError(w, http.StatusRequestEntityTooLarge, manuscriptArchiveErrorResponse{
				Error: "The .chron archive exceeds the 256 MB compressed upload limit.", Code: "archive_too_large", Stage: "upload",
				Detail: fmt.Sprintf("Received more than %d bytes; use a smaller archive or split the library.", maxManuscriptArchiveBytes), RolledBack: true, Imported: 0,
				Log: []string{"Stopped reading when the compressed-size safety limit was exceeded.", "Validation and the database transaction did not start; no data was changed."},
			})
		} else {
			archiveImportError(w, http.StatusBadRequest, manuscriptArchiveErrorResponse{
				Error: "The .chron upload could not be read completely.", Code: "upload_read_failed", Stage: "upload",
				Detail: copyErr.Error(), RolledBack: true, Imported: 0, Retryable: true,
				Log: []string{"The upload ended or failed before staging completed.", "Validation and the database transaction did not start; no data was changed."},
			})
		}
		return
	}
	if closeErr != nil {
		archiveImportError(w, http.StatusInternalServerError, manuscriptArchiveErrorResponse{
			Error: "The staged .chron upload could not be finalized.", Code: "staging_failed", Stage: "upload",
			Detail: closeErr.Error(), RolledBack: true, Imported: 0, Retryable: true,
			Log: []string{fmt.Sprintf("Received %d byte(s), but closing the temporary file failed.", written), "The database transaction did not start; no data was changed."},
		})
		return
	}
	if written == 0 {
		archiveImportError(w, http.StatusBadRequest, manuscriptArchiveErrorResponse{
			Error: "Choose a non-empty .chron archive to import.", Code: "empty_archive", Stage: "upload",
			Detail: "The request body contained zero bytes.", RolledBack: true, Imported: 0,
			Log: []string{"No archive data was received.", "Validation and the database transaction did not start; no data was changed."},
		})
		return
	}
	decoded, err := decodeArchive(tmpName)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_archive"
		stage := "validation"
		if strings.Contains(err.Error(), "unsupported .chron format or version") {
			status = http.StatusUnprocessableEntity
			code = "unsupported_version"
			stage = "manifest"
		} else if strings.Contains(err.Error(), "compression") {
			status = http.StatusUnprocessableEntity
			code = "unsupported_compression"
			stage = "manifest"
		}
		archiveImportError(w, status, manuscriptArchiveErrorResponse{
			Error: "The .chron archive could not be imported.", Code: code, Stage: stage,
			Detail: err.Error(), RolledBack: true, Imported: 0, Retryable: false,
			Log: []string{
				fmt.Sprintf("Received and staged %d byte(s).", written),
				"Archive validation failed before the database transaction began.",
				"No manuscripts, chapters, covers, change history, or replica jobs were written.",
			},
		})
		return
	}
	result, err := h.importArchive(userID, decoded)
	if err != nil {
		archiveImportError(w, http.StatusInternalServerError, manuscriptArchiveErrorResponse{
			Error: "The .chron import failed and was not applied.", Code: "atomic_import_failed", Stage: "database",
			Detail: err.Error(), RolledBack: true, Imported: 0, Retryable: true,
			Log: []string{
				fmt.Sprintf("Validated .chron format v%d with %d manuscript payload(s).", manuscriptArchiveVersion, len(decoded)),
				"A database, cover-storage, or replica-queue operation failed inside the atomic transaction.",
				"Rolled back the complete transaction: zero manuscripts from this import were retained.",
			},
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
