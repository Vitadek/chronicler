package replica

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type Queryable interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	QueryRow(query string, args ...interface{}) *sql.Row
	Query(query string, args ...interface{}) (*sql.Rows, error)
}

func Sha256(content []byte) string {
	h := sha256.New()
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

func Segment(value string) string {
	return url.QueryEscape(value)
}

func PortableReplicaKey(key string) string {
	if strings.HasPrefix(key, "v1/") {
		return key
	}

	parts := strings.Split(key, "/")
	if len(parts) < 2 {
		return key
	}

	namespace := parts[0]
	userId := parts[1]
	rest := parts[2:]

	if namespace == "covers" && len(rest) > 0 {
		return fmt.Sprintf("v1/users/%s/covers/%s", Segment(userId), strings.Join(rest, "/"))
	}
	if namespace == "settings" && len(rest) == 0 {
		return fmt.Sprintf("v1/users/%s/settings.json", Segment(userId))
	}
	if namespace == "profiles" && len(rest) == 0 {
		return fmt.Sprintf("v1/users/%s/profile.json", Segment(userId))
	}
	if namespace == "manuscripts" && len(rest) > 0 {
		mapped := make([]string, len(rest))
		copy(mapped, rest)
		if mapped[len(mapped)-1] == "manuscript.json" {
			mapped[len(mapped)-1] = "metadata.json"
		}
		return fmt.Sprintf("v1/users/%s/manuscripts/%s", Segment(userId), strings.Join(mapped, "/"))
	}

	return key
}

func NextStorageGeneration(q Queryable, key string) (int, error) {
	replicaKey := PortableReplicaKey(key)
	var gen int
	err := q.QueryRow(`
		INSERT INTO storage_replica_generations(key, generation)
		VALUES (?, COALESCE((
			SELECT generation + 1 FROM storage_replica_manifest WHERE key = ?
		), 1))
		ON CONFLICT(key) DO UPDATE SET generation = MAX(
			storage_replica_generations.generation + 1,
			COALESCE((SELECT generation + 1 FROM storage_replica_manifest WHERE key = ?), 1)
		)
		RETURNING generation
	`, replicaKey, replicaKey, replicaKey).Scan(&gen)
	return gen, err
}

func EnqueueAtGeneration(q Queryable, key string, operation string, generation int, payload []byte, contentType string, checksum string) error {
	now := time.Now().UnixNano() / int64(time.Millisecond)

	var payloadVal interface{} = nil
	if payload != nil {
		payloadVal = payload
	}
	var contentTypeVal interface{} = nil
	if contentType != "" {
		contentTypeVal = contentType
	}
	var checksumVal interface{} = nil
	if checksum != "" {
		checksumVal = checksum
	}

	_, err := q.Exec(`
		INSERT INTO storage_replica_manifest(
			key, operation, payload, content_type, checksum, generation, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			operation = excluded.operation,
			payload = excluded.payload,
			content_type = excluded.content_type,
			checksum = excluded.checksum,
			generation = excluded.generation,
			updated_at = excluded.updated_at
		WHERE excluded.generation > storage_replica_manifest.generation
		   OR (
			   excluded.generation = storage_replica_manifest.generation
			   AND excluded.operation = storage_replica_manifest.operation
			   AND COALESCE(excluded.checksum, '') = COALESCE(storage_replica_manifest.checksum, '')
		   )
	`, key, operation, payloadVal, contentTypeVal, checksumVal, generation, now)
	if err != nil {
		return err
	}

	// We insert into outbox only if storage is configured for nextcloud/s3.
	// Since outbox queue processing starts dynamically, we insert into the queue.
	_, err = q.Exec(`
		INSERT INTO storage_replication_outbox(
			key, operation, payload, content_type, checksum, generation,
			attempts, next_attempt_at, last_attempt_at, last_error,
			dead_letter, created_at
		) VALUES (?, ?, ?, ?, ?, ?, 0, 0, NULL, NULL, 0, ?)
		ON CONFLICT(key) DO UPDATE SET
			operation = excluded.operation,
			payload = excluded.payload,
			content_type = excluded.content_type,
			checksum = excluded.checksum,
			generation = excluded.generation,
			attempts = 0,
			next_attempt_at = 0,
			last_attempt_at = NULL,
			last_error = NULL,
			dead_letter = 0,
			created_at = excluded.created_at
		WHERE excluded.generation > storage_replication_outbox.generation
		   OR (
			   excluded.generation = storage_replication_outbox.generation
			   AND excluded.operation = storage_replication_outbox.operation
			   AND COALESCE(excluded.checksum, '') = COALESCE(storage_replication_outbox.checksum, '')
		   )
	`, key, operation, payloadVal, contentTypeVal, checksumVal, generation, now)
	return err
}

func EnqueueReplicaPut(q Queryable, key string, content []byte, contentType string) error {
	replicaKey := PortableReplicaKey(key)
	gen, err := NextStorageGeneration(q, key)
	if err != nil {
		return err
	}
	checksum := Sha256(content)
	return EnqueueAtGeneration(q, replicaKey, "put", gen, content, contentType, checksum)
}

func EnqueueReplicaDelete(q Queryable, key string) error {
	replicaKey := PortableReplicaKey(key)
	gen, err := NextStorageGeneration(q, key)
	if err != nil {
		return err
	}
	return EnqueueAtGeneration(q, replicaKey, "delete", gen, nil, "", "")
}
