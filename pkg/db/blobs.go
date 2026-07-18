package db

import (
	"database/sql"
	"time"

	"chronicle-server/pkg/replica"
)

func GetBlob(database *sql.DB, key string) ([]byte, string, error) {
	var content []byte
	var contentType sql.NullString
	err := database.QueryRow("SELECT content, content_type FROM storage_blobs WHERE key = ?", key).Scan(&content, &contentType)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return content, contentType.String, nil
}

func PutBlob(database *sql.DB, key string, content []byte, contentType string) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	gen, errGen := replica.NextStorageGeneration(tx, key)
	if errGen != nil {
		return errGen
	}

	checksum := replica.Sha256(content)
	now := time.Now().UnixNano() / int64(time.Millisecond)

	var ctVal interface{} = nil
	if contentType != "" {
		ctVal = contentType
	}

	_, err = tx.Exec(`
		INSERT INTO storage_blobs (key, content, content_type, checksum, generation, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			content = excluded.content,
			content_type = excluded.content_type,
			checksum = excluded.checksum,
			generation = excluded.generation,
			updated_at = excluded.updated_at
	`, key, content, ctVal, checksum, gen, now)
	if err != nil {
		return err
	}

	err = replica.EnqueueAtGeneration(tx, replica.PortableReplicaKey(key), "put", gen, content, contentType, checksum)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func DeleteBlob(database *sql.DB, key string) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	gen, errGen := replica.NextStorageGeneration(tx, key)
	if errGen != nil {
		return errGen
	}

	_, err = tx.Exec("DELETE FROM storage_blobs WHERE key = ?", key)
	if err != nil {
		return err
	}

	err = replica.EnqueueAtGeneration(tx, replica.PortableReplicaKey(key), "delete", gen, nil, "", "")
	if err != nil {
		return err
	}

	return tx.Commit()
}

func DeleteBlobsByPrefix(database *sql.DB, prefix string) (int, error) {
	tx, err := database.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	rows, errQuery := tx.Query("SELECT key FROM storage_blobs WHERE key LIKE ?", prefix+"%")
	if errQuery != nil {
		return 0, errQuery
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		if errScan := rows.Scan(&k); errScan == nil {
			keys = append(keys, k)
		}
	}
	rows.Close()

	for _, k := range keys {
		gen, errGen := replica.NextStorageGeneration(tx, k)
		if errGen != nil {
			return 0, errGen
		}
		_, err = tx.Exec("DELETE FROM storage_blobs WHERE key = ?", k)
		if err != nil {
			return 0, err
		}
		err = replica.EnqueueAtGeneration(tx, replica.PortableReplicaKey(k), "delete", gen, nil, "", "")
		if err != nil {
			return 0, err
		}
	}

	err = tx.Commit()
	if err != nil {
		return 0, err
	}
	return len(keys), nil
}
