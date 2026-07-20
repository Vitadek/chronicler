package db

import (
	"database/sql"

	"github.com/google/uuid"
)

const SyncHistoryEpochKey = "sync:history-epoch:v2"

// Executor is satisfied by both *sql.DB and *sql.Tx, so callers already inside
// a transaction can pass it in rather than reaching for a second connection.
type Executor interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

// GetSyncHistoryEpoch reads the sync epoch, creating it on first use.
//
// Takes an Executor, not *sql.DB, because that lazy INSERT is a WRITE. A caller
// that has already opened a transaction MUST pass its *sql.Tx: with
// BEGIN IMMEDIATE the transaction holds the write lock, so writing through a
// separate pooled connection deadlocks the request against itself until
// busy_timeout expires. (Seen as a 500 "Epoch read failed" on /api/sync/v2.)
func GetSyncHistoryEpoch(database Executor) (string, error) {
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
