package grammar

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Engine names, shared between the live check path (pkg/api/grammar.go) and
// the background sweep (pkg/grammarsweep) so their cache keys line up.
const (
	EngineNative       = "native"
	EngineLanguagetool = "languagetool"
)

// CheckCached wraps a grammar check with a persistent, content-addressed
// cache keyed by (sha256 of the submitted text, engine name). It exists so
// two different callers checking byte-identical text — a live request and
// the background sweep (pkg/grammarsweep) that ran earlier while the server
// was idle — share one result instead of redoing the work. Keying on the
// engine name, not just the text, matters: the same paragraph checked by
// "native" and "languagetool" legitimately has two different answers.
//
// Any cache read/write error degrades to calling compute() directly and
// skipping the cache — a broken cache must never break linting.
func CheckCached(database *sql.DB, text, engine string, compute func() ([]Hit, error)) ([]Hit, error) {
	if database == nil {
		return compute()
	}

	hash := sha256.Sum256([]byte(text))
	key := hex.EncodeToString(hash[:])

	var hitsJSON string
	err := database.QueryRow(
		`SELECT hits_json FROM grammar_check_cache WHERE text_hash = ? AND engine = ?`,
		key, engine,
	).Scan(&hitsJSON)
	if err == nil {
		var hits []Hit
		if json.Unmarshal([]byte(hitsJSON), &hits) == nil {
			return hits, nil
		}
		// Corrupt row — fall through and recompute rather than failing the request.
	} else if err != sql.ErrNoRows {
		return compute()
	}

	hits, err := compute()
	if err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(hits)
	if err == nil {
		now := time.Now().UnixNano() / int64(time.Millisecond)
		_, _ = database.Exec(
			`INSERT OR REPLACE INTO grammar_check_cache (text_hash, engine, hits_json, computed_at) VALUES (?, ?, ?, ?)`,
			key, engine, string(encoded), now,
		)
	}

	return hits, nil
}
