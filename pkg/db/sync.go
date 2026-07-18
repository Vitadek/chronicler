package db

import (
	"database/sql"

	"github.com/google/uuid"
)

const SyncHistoryEpochKey = "sync:history-epoch:v2"

func GetSyncHistoryEpoch(database *sql.DB) (string, error) {
	var v string
	err := database.QueryRow("SELECT v FROM kv WHERE k = ?", SyncHistoryEpochKey).Scan(&v)
	if err == nil {
		return v, nil
	} else if err != sql.ErrNoRows {
		return "", err
	}

	created := uuid.New().String()
	_, err = database.Exec("INSERT OR IGNORE INTO kv(k, v, expires_at) VALUES (?, ?, NULL)", SyncHistoryEpochKey, created)
	if err != nil {
		return "", err
	}

	err = database.QueryRow("SELECT v FROM kv WHERE k = ?", SyncHistoryEpochKey).Scan(&v)
	return v, err
}

func RotateSyncHistoryEpoch(database *sql.DB) (string, error) {
	next := uuid.New().String()
	_, err := database.Exec(`
		INSERT INTO kv(k, v, expires_at) VALUES (?, ?, NULL)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v, expires_at = NULL
	`, SyncHistoryEpochKey, next)
	if err != nil {
		return "", err
	}
	return next, nil
}
