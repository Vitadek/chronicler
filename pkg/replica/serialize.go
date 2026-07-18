package replica

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
)

type PortableLiveManuscriptRecord struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Kind          string                 `json:"kind"`
	UserID        string                 `json:"userId"`
	ID            string                 `json:"id"`
	Revision      int                    `json:"revision"`
	LastModified  int64                  `json:"lastModified"`
	DeletedAt     int64                  `json:"deletedAt,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

type PortableManuscriptTombstone struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	UserID        string `json:"userId"`
	ID            string `json:"id"`
	Revision      int    `json:"revision"`
	DeletedAt     int64  `json:"deletedAt"`
}

type PortableLiveChapterMetadata struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	UserID        string `json:"userId"`
	ManuscriptID  string `json:"manuscriptId"`
	ID            string `json:"id"`
	Title         string `json:"title"`
	Position      int    `json:"position"`
	Revision      int    `json:"revision"`
	LastModified  int64  `json:"lastModified"`
	ContentBytes  int    `json:"contentBytes"`
}

type PortableChapterTombstone struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	UserID        string `json:"userId"`
	ManuscriptID  string `json:"manuscriptId"`
	ID            string `json:"id"`
	Revision      int    `json:"revision"`
	DeletedAt     int64  `json:"deletedAt"`
	ContentBytes  int    `json:"contentBytes"` // Always 0
}

func stableJson(val interface{}) []byte {
	bytes, _ := json.MarshalIndent(val, "", "  ")
	return append(bytes, '\n')
}

func SerializeManuscript(userId string, id string, lastModified int64, revision int, metadataJson string) ([]byte, error) {
	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(metadataJson), &metadata); err != nil {
		return nil, err
	}

	record := PortableLiveManuscriptRecord{
		SchemaVersion: 1,
		Kind:          "manuscript",
		UserID:        userId,
		ID:            id,
		Revision:      revision,
		LastModified:  lastModified,
		Metadata:      metadata,
	}

	return stableJson(record), nil
}

func SerializeManuscriptTombstone(userId string, id string, deletedAt int64, revision int) []byte {
	record := PortableManuscriptTombstone{
		SchemaVersion: 1,
		Kind:          "manuscript-tombstone",
		UserID:        userId,
		ID:            id,
		Revision:      revision,
		DeletedAt:     deletedAt,
	}

	return stableJson(record)
}

func serializeChapterEnvelope(record interface{}, content []byte, title string) []byte {
	recordBytes, _ := json.Marshal(record)
	encoded := base64.RawURLEncoding.EncodeToString(recordBytes)

	envelope := fmt.Sprintf("<!doctype html>\n"+
		"<html lang=\"en\" data-chronicle-record=\"%s\">\n"+
		"<head>\n"+
		"  <meta charset=\"utf-8\">\n"+
		"  <title>%s</title>\n"+
		"</head>\n"+
		"<body data-chronicle-content>\n",
		encoded, html.EscapeString(title))

	result := append([]byte(envelope), content...)
	result = append(result, []byte("\n</body>\n</html>\n")...)
	return result
}

func SerializeChapter(userId string, manuscriptId string, id string, title string, position int, lastModified int64, revision int, content string) []byte {
	contentBytes := []byte(content)
	record := PortableLiveChapterMetadata{
		SchemaVersion: 1,
		Kind:          "chapter",
		UserID:        userId,
		ManuscriptID:  manuscriptId,
		ID:            id,
		Title:         title,
		Position:      position,
		Revision:      revision,
		LastModified:  lastModified,
		ContentBytes:  len(contentBytes),
	}

	return serializeChapterEnvelope(record, contentBytes, title)
}

type PortableProfileRecord struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Kind          string                 `json:"kind"`
	UserID        string                 `json:"userId"`
	Revision      int                    `json:"revision"`
	LastModified  int64                  `json:"lastModified"`
	Profile       map[string]interface{} `json:"profile"`
}

func SerializeChapterTombstone(userId string, manuscriptId string, id string, deletedAt int64, revision int) []byte {
	record := PortableChapterTombstone{
		SchemaVersion: 1,
		Kind:          "chapter-tombstone",
		UserID:        userId,
		ManuscriptID:  manuscriptId,
		ID:            id,
		Revision:      revision,
		DeletedAt:     deletedAt,
		ContentBytes:  0,
	}

	return serializeChapterEnvelope(record, nil, "")
}

func SerializeProfile(userId string, profileJson string, lastModified int64, revision int) ([]byte, error) {
	var profile map[string]interface{}
	if err := json.Unmarshal([]byte(profileJson), &profile); err != nil {
		return nil, err
	}

	record := PortableProfileRecord{
		SchemaVersion: 1,
		Kind:          "profile",
		UserID:        userId,
		Revision:      revision,
		LastModified:  lastModified,
		Profile:       profile,
	}

	return stableJson(record), nil
}
