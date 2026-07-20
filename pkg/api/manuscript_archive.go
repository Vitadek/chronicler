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
	item       manuscriptArchiveItem
	record     *db.ManuscriptRecord
	cover      []byte
	coverMime  string
	coverExt   string
	sourceID   string
	destID     string
	destTitle  string
	wasRenamed bool
}

type manuscriptArchiveImportItem struct {
	SourceID string `json:"sourceId"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Copied   bool   `json:"copied"`
}

type manuscriptArchiveImportResult struct {
	Imported    int                           `json:"imported"`
	Renamed     int                           `json:"renamed"`
	Covers      int                           `json:"covers"`
	Manuscripts []manuscriptArchiveImportItem `json:"manuscripts"`
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
		return nil, fmt.Errorf("unsupported .chron format or version (%q v%d)", manifest.Format, manifest.Version)
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

func manuscriptExists(database *sql.DB, userID string, id string) (bool, error) {
	var one int
	err := database.QueryRow("SELECT 1 FROM manuscripts WHERE user_id = ? AND id = ?", userID, id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func allocateImportedManuscriptID(database *sql.DB, userID, preferred string, reserved map[string]bool) (string, bool, error) {
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

func storeImportedCover(database *sql.DB, userID, filename, mime string, content []byte) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	return tx.Commit()
}

func (h *ManuscriptArchiveHandler) importArchive(userID string, archived []archivedManuscript) (*manuscriptArchiveImportResult, error) {
	result := &manuscriptArchiveImportResult{Manuscripts: make([]manuscriptArchiveImportItem, 0, len(archived))}
	reserved := make(map[string]bool, len(archived))
	for index := range archived {
		payload := &archived[index]
		destID, copied, err := allocateImportedManuscriptID(h.database, userID, payload.sourceID, reserved)
		if err != nil {
			return nil, err
		}
		payload.destID = destID
		payload.wasRenamed = copied
		payload.record.Metadata.ID = destID
		payload.record.Metadata.Revision = 0
		if copied {
			payload.record.Metadata.Title = importedCopyTitle(payload.record.Metadata.Title)
		}
		payload.destTitle = payload.record.Metadata.Title
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
				return nil, tokenErr
			}
			coverFilename = fmt.Sprintf("%s.%s.%s", destID, token, payload.coverExt)
			payload.record.Metadata.ExtraFields["coverArt"] = coverFilename
		}

		saved, saveErr := db.SaveLegacyManuscript(h.database, userID, payload.record, true)
		if saveErr != nil {
			return nil, saveErr
		}
		if len(saved.Conflicts) > 0 {
			return nil, errors.New("a manuscript was created concurrently; import can be retried safely")
		}
		if coverFilename != "" {
			if err = storeImportedCover(h.database, userID, coverFilename, payload.coverMime, payload.cover); err != nil {
				return nil, fmt.Errorf("manuscript imported but its cover could not be stored: %w", err)
			}
			result.Covers++
		}
		result.Imported++
		if copied {
			result.Renamed++
		}
		result.Manuscripts = append(result.Manuscripts, manuscriptArchiveImportItem{
			SourceID: payload.sourceID,
			ID:       destID,
			Title:    payload.destTitle,
			Copied:   copied,
		})
	}
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
		archiveError(w, http.StatusInternalServerError, "Could not stage the manuscript archive")
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
			archiveError(w, http.StatusRequestEntityTooLarge, "The .chron archive is larger than 256 MB")
		} else {
			archiveError(w, http.StatusBadRequest, "Could not read the .chron archive")
		}
		return
	}
	if closeErr != nil {
		archiveError(w, http.StatusInternalServerError, "Could not stage the manuscript archive")
		return
	}
	if written == 0 {
		archiveError(w, http.StatusBadRequest, "Choose a .chron archive to import")
		return
	}
	decoded, err := decodeArchive(tmpName)
	if err != nil {
		archiveError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.importArchive(userID, decoded)
	if err != nil {
		archiveError(w, http.StatusInternalServerError, "Could not import manuscripts: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
