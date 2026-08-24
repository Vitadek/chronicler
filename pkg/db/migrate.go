package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

type Migration struct {
	Name string
	Up   func(tx *sql.Tx) error
}

func RunMigrations(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name        TEXT PRIMARY KEY,
			applied_at  INTEGER NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	rows, err := db.Query("SELECT name FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("failed to query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		applied[name] = true
	}

	for _, m := range migrations {
		if applied[m.Name] {
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			return err
		}

		if err := m.Up(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s failed: %w", m.Name, err)
		}

		_, err = tx.Exec("INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)", m.Name, time.Now().UnixNano()/int64(time.Millisecond))
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to record migration %s: %w", m.Name, err)
		}

		if err := tx.Commit(); err != nil {
			return err
		}
		fmt.Printf("[db] applied migration %s\n", m.Name)
	}

	return nil
}

var migrations = []Migration{
	{
		Name: "001_init",
		Up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS users (
					id            TEXT PRIMARY KEY,
					email         TEXT UNIQUE,
					display_name  TEXT,
					created_at    INTEGER NOT NULL
				);

				CREATE TABLE IF NOT EXISTS sessions (
					token             TEXT PRIMARY KEY,
					user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					nc_access_token   TEXT,
					nc_refresh_token  TEXT,
					nc_expires_at     INTEGER,
					expires_at        INTEGER NOT NULL,
					created_at        INTEGER NOT NULL
				);
				CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

				CREATE TABLE IF NOT EXISTS manuscripts (
					user_id        TEXT NOT NULL,
					id             TEXT NOT NULL,
					data           TEXT NOT NULL,
					last_modified  INTEGER NOT NULL,
					deleted_at     INTEGER,
					PRIMARY KEY (user_id, id)
				);
				CREATE INDEX IF NOT EXISTS idx_manuscripts_sync
					ON manuscripts(user_id, last_modified);

				CREATE TABLE IF NOT EXISTS chapters (
					user_id        TEXT NOT NULL,
					manuscript_id  TEXT NOT NULL,
					id             TEXT NOT NULL,
					title          TEXT,
					content        TEXT,
					position       INTEGER,
					last_modified  INTEGER NOT NULL,
					deleted_at     INTEGER,
					PRIMARY KEY (user_id, manuscript_id, id)
				);
				CREATE INDEX IF NOT EXISTS idx_chapters_sync
					ON chapters(user_id, last_modified);

				CREATE TABLE IF NOT EXISTS profiles (
					user_id        TEXT PRIMARY KEY,
					data           TEXT NOT NULL,
					last_modified  INTEGER NOT NULL
				);

				CREATE TABLE IF NOT EXISTS kv (
					k          TEXT PRIMARY KEY,
					v          TEXT NOT NULL,
					expires_at INTEGER
				);
			`)
			return err
		},
	},
	{
		Name: "002_external_identity",
		Up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				ALTER TABLE users ADD COLUMN external_provider TEXT;
				ALTER TABLE users ADD COLUMN external_issuer   TEXT;
				ALTER TABLE users ADD COLUMN external_id       TEXT;
				ALTER TABLE users ADD COLUMN nc_user_id        TEXT;
				ALTER TABLE users ADD COLUMN nc_url            TEXT;
				CREATE UNIQUE INDEX IF NOT EXISTS idx_users_external
					ON users(external_provider, external_issuer, external_id)
					WHERE external_id IS NOT NULL;
			`)
			return err
		},
	},
	{
		Name: "003_plugin_system",
		Up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS plugin_states (
					user_id        TEXT NOT NULL,
					id             TEXT NOT NULL,
					plugin_id      TEXT NOT NULL,
					manuscript_id  TEXT,
					enabled        INTEGER DEFAULT 1,
					state          TEXT NOT NULL DEFAULT '{}',
					last_modified  INTEGER NOT NULL,
					PRIMARY KEY (user_id, id),
					FOREIGN KEY(manuscript_id) REFERENCES manuscripts(id) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_plugin_states_sync 
					ON plugin_states(user_id, last_modified);
			`)
			return err
		},
	},
	{
		Name: "004_fix_plugin_states_fk",
		Up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				DROP TABLE IF EXISTS plugin_states;
				CREATE TABLE plugin_states (
					user_id        TEXT NOT NULL,
					id             TEXT NOT NULL,
					plugin_id      TEXT NOT NULL,
					manuscript_id  TEXT,
					enabled        INTEGER DEFAULT 1,
					state          TEXT NOT NULL DEFAULT '{}',
					last_modified  INTEGER NOT NULL,
					PRIMARY KEY (user_id, id),
					FOREIGN KEY(user_id, manuscript_id) REFERENCES manuscripts(user_id, id) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_plugin_states_sync 
					ON plugin_states(user_id, last_modified);
			`)
			return err
		},
	},
	{
		Name: "005_collab_ydocs",
		Up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS ydocs (
					name        TEXT PRIMARY KEY,
					data        BLOB NOT NULL,
					updated_at  INTEGER NOT NULL
				);
			`)
			return err
		},
	},
	{
		Name: "006_chapter_pre_collab",
		Up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS chapter_pre_collab (
					user_id        TEXT NOT NULL,
					manuscript_id  TEXT NOT NULL,
					chapter_id     TEXT NOT NULL,
					content        TEXT NOT NULL,
					backed_up_at   INTEGER NOT NULL,
					PRIMARY KEY (user_id, manuscript_id, chapter_id)
				);
			`)
			return err
		},
	},
	{
		Name: "007_record_revisions",
		Up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				ALTER TABLE manuscripts ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
				ALTER TABLE chapters    ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
				ALTER TABLE profiles    ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;

				CREATE TABLE change_log (
					seq            INTEGER PRIMARY KEY AUTOINCREMENT,
					user_id        TEXT NOT NULL,
					entity         TEXT NOT NULL,
					manuscript_id  TEXT,
					record_id      TEXT NOT NULL,
					operation      TEXT NOT NULL,
					revision       INTEGER NOT NULL,
					changed_at     INTEGER NOT NULL
				);
				CREATE INDEX idx_change_log_user_seq
					ON change_log(user_id, seq);
			`)
			if err != nil {
				return err
			}

			now := time.Now().UnixNano() / int64(time.Millisecond)
			_, err = tx.Exec(`
				INSERT INTO change_log
					(user_id, entity, manuscript_id, record_id, operation, revision, changed_at)
				SELECT user_id, 'manuscript', NULL, id,
					   CASE WHEN deleted_at IS NULL THEN 'upsert' ELSE 'delete' END,
					   revision, ?
				  FROM manuscripts
			`, now)
			if err != nil {
				return err
			}

			_, err = tx.Exec(`
				INSERT INTO change_log
					(user_id, entity, manuscript_id, record_id, operation, revision, changed_at)
				SELECT user_id, 'chapter', manuscript_id, id,
					   CASE WHEN deleted_at IS NULL THEN 'upsert' ELSE 'delete' END,
					   revision, ?
				  FROM chapters
			`, now)
			if err != nil {
				return err
			}

			_, err = tx.Exec(`
				INSERT INTO change_log
					(user_id, entity, manuscript_id, record_id, operation, revision, changed_at)
				SELECT user_id, 'profile', NULL, 'profile', 'upsert', revision, ?
				  FROM profiles
			`, now)
			return err
		},
	},
	{
		Name: "008_scrub_tombstone_payloads",
		Up: func(tx *sql.Tx) error {
			return scrubRetainedTombstonePayloads(tx)
		},
	},
	{
		Name: "storage_001_blob_store",
		Up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS storage_blobs (
					key           TEXT PRIMARY KEY,
					content       BLOB NOT NULL,
					content_type  TEXT,
					checksum      TEXT NOT NULL,
					generation    INTEGER NOT NULL CHECK (generation > 0),
					updated_at    INTEGER NOT NULL
				);

				CREATE TABLE IF NOT EXISTS storage_replica_generations (
					key         TEXT PRIMARY KEY,
					generation  INTEGER NOT NULL CHECK (generation > 0)
				);

				CREATE TABLE IF NOT EXISTS storage_replica_manifest (
					key           TEXT PRIMARY KEY,
					operation     TEXT NOT NULL CHECK (operation IN ('put', 'delete')),
					payload       BLOB,
					content_type  TEXT,
					checksum      TEXT,
					generation    INTEGER NOT NULL CHECK (generation > 0),
					updated_at    INTEGER NOT NULL
				);

				CREATE TABLE IF NOT EXISTS storage_replication_outbox (
					key              TEXT PRIMARY KEY,
					operation        TEXT NOT NULL CHECK (operation IN ('put', 'delete')),
					payload          BLOB,
					content_type     TEXT,
					checksum         TEXT,
					generation       INTEGER NOT NULL CHECK (generation > 0),
					attempts         INTEGER NOT NULL DEFAULT 0,
					next_attempt_at  INTEGER NOT NULL DEFAULT 0,
					last_attempt_at  INTEGER,
					last_error       TEXT,
					dead_letter      INTEGER NOT NULL DEFAULT 0 CHECK (dead_letter IN (0, 1)),
					created_at       INTEGER NOT NULL
				);
				CREATE INDEX IF NOT EXISTS idx_storage_outbox_due
					ON storage_replication_outbox(dead_letter, next_attempt_at, created_at);

				CREATE TABLE IF NOT EXISTS storage_replication_state (
					id               INTEGER PRIMARY KEY CHECK (id = 1),
					initialized_at   INTEGER,
					last_attempt_at  INTEGER,
					last_success_at  INTEGER,
					last_error       TEXT
				);
				INSERT OR IGNORE INTO storage_replication_state(id) VALUES (1);
			`)
			return err
		},
	},
	{
		Name: "grammar_001_check_cache",
		Up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS grammar_check_cache (
					text_hash   TEXT NOT NULL,
					engine      TEXT NOT NULL,
					hits_json   TEXT NOT NULL,
					computed_at INTEGER NOT NULL,
					PRIMARY KEY (text_hash, engine)
				);
			`)
			return err
		},
	},
	{
		Name: "009_chapters_position_idx",
		Up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				CREATE INDEX IF NOT EXISTS idx_chapters_manuscript_pos
					ON chapters(user_id, manuscript_id, deleted_at, position, last_modified);
			`)
			return err
		},
	},
}

type DeletedManuscript struct {
	UserID    string `db:"user_id"`
	ID        string `db:"id"`
	DeletedAt int64  `db:"deleted_at"`
}

type LiveChapter struct {
	ID           string `db:"id"`
	LastModified int64  `db:"last_modified"`
	Revision     int    `db:"revision"`
}

type DeletedChapter struct {
	UserID       string `db:"user_id"`
	ManuscriptID string `db:"manuscript_id"`
	ID           string `db:"id"`
}

func scrubRetainedTombstonePayloads(tx *sql.Tx) error {
	rows, err := tx.Query("SELECT user_id, id, deleted_at FROM manuscripts WHERE deleted_at IS NOT NULL")
	if err != nil {
		return err
	}
	defer rows.Close()

	var deletedManuscripts []DeletedManuscript
	for rows.Next() {
		var dm DeletedManuscript
		if err := rows.Scan(&dm.UserID, &dm.ID, &dm.DeletedAt); err != nil {
			return err
		}
		deletedManuscripts = append(deletedManuscripts, dm)
	}
	rows.Close()

	// Prepared statements
	scrubManuscript, err := tx.Prepare("UPDATE manuscripts SET data = ? WHERE user_id = ? AND id = ? AND data <> ?")
	if err != nil {
		return err
	}
	defer scrubManuscript.Close()

	liveChildren, err := tx.Prepare("SELECT id, last_modified, revision FROM chapters WHERE user_id = ? AND manuscript_id = ? AND deleted_at IS NULL")
	if err != nil {
		return err
	}
	defer liveChildren.Close()

	tombstoneChild, err := tx.Prepare(`
		UPDATE chapters
		   SET title = NULL, content = NULL, position = NULL,
			   last_modified = ?, deleted_at = ?, revision = ?
		 WHERE user_id = ? AND manuscript_id = ? AND id = ? AND deleted_at IS NULL
	`)
	if err != nil {
		return err
	}
	defer tombstoneChild.Close()

	logChildDelete, err := tx.Prepare(`
		INSERT INTO change_log(
			user_id, entity, manuscript_id, record_id, operation, revision, changed_at
		) VALUES (?, 'chapter', ?, ?, 'delete', ?, ?)
	`)
	if err != nil {
		return err
	}
	defer logChildDelete.Close()

	deleteYdoc, err := tx.Prepare("DELETE FROM ydocs WHERE name = ?")
	if err != nil {
		return err
	}
	defer deleteYdoc.Close()

	deleteYdocPrefix, err := tx.Prepare("DELETE FROM ydocs WHERE substr(name, 1, ?) = ?")
	if err != nil {
		return err
	}
	defer deleteYdocPrefix.Close()

	for _, dm := range deletedManuscripts {
		minimalMap := map[string]string{"id": dm.ID}
		minimalBytes, _ := json.Marshal(minimalMap)
		minimalStr := string(minimalBytes)

		_, err = scrubManuscript.Exec(minimalStr, dm.UserID, dm.ID, minimalStr)
		if err != nil {
			return err
		}

		cRows, err := liveChildren.Query(dm.UserID, dm.ID)
		if err != nil {
			return err
		}

		var children []LiveChapter
		for cRows.Next() {
			var lc LiveChapter
			if err := cRows.Scan(&lc.ID, &lc.LastModified, &lc.Revision); err != nil {
				cRows.Close()
				return err
			}
			children = append(children, lc)
		}
		cRows.Close()

		for _, child := range children {
			changedAt := dm.DeletedAt
			if child.LastModified > changedAt {
				changedAt = child.LastModified
			}
			newRevision := child.Revision + 1

			res, err := tombstoneChild.Exec(changedAt, changedAt, newRevision, dm.UserID, dm.ID, child.ID)
			if err != nil {
				return err
			}

			affected, err := res.RowsAffected()
			if err != nil {
				return err
			}

			if affected == 1 {
				_, err = logChildDelete.Exec(dm.UserID, dm.ID, child.ID, newRevision, changedAt)
				if err != nil {
					return err
				}
			}
		}

		scopedPrefix := fmt.Sprintf("%s/%s:", url.QueryEscape(dm.UserID), dm.ID)
		_, err = deleteYdocPrefix.Exec(len(scopedPrefix), scopedPrefix)
		if err != nil {
			return err
		}

		legacyPrefix := fmt.Sprintf("%s:", dm.ID)
		_, err = deleteYdocPrefix.Exec(len(legacyPrefix), legacyPrefix)
		if err != nil {
			return err
		}
	}

	// Scrub deleted chapters ydocs
	chRows, err := tx.Query("SELECT user_id, manuscript_id, id FROM chapters WHERE deleted_at IS NOT NULL")
	if err != nil {
		return err
	}
	defer chRows.Close()

	var deletedChapters []DeletedChapter
	for chRows.Next() {
		var dc DeletedChapter
		if err := chRows.Scan(&dc.UserID, &dc.ManuscriptID, &dc.ID); err != nil {
			return err
		}
		deletedChapters = append(deletedChapters, dc)
	}
	chRows.Close()

	for _, dc := range deletedChapters {
		name := fmt.Sprintf("%s/%s:%s", url.QueryEscape(dc.UserID), dc.ManuscriptID, dc.ID)
		_, err = deleteYdoc.Exec(name)
		if err != nil {
			return err
		}

		legacyName := fmt.Sprintf("%s:%s", dc.ManuscriptID, dc.ID)
		_, err = deleteYdoc.Exec(legacyName)
		if err != nil {
			return err
		}
	}

	_, err = tx.Exec(`
		UPDATE chapters
		   SET title = NULL, content = NULL, position = NULL
		 WHERE deleted_at IS NOT NULL
		   AND (title IS NOT NULL OR content IS NOT NULL OR position IS NOT NULL);

		DELETE FROM chapter_pre_collab
		 WHERE EXISTS (
		   SELECT 1 FROM manuscripts m
			WHERE m.user_id = chapter_pre_collab.user_id
			  AND m.id = chapter_pre_collab.manuscript_id
			  AND m.deleted_at IS NOT NULL
		 ) OR EXISTS (
		   SELECT 1 FROM chapters c
			WHERE c.user_id = chapter_pre_collab.user_id
			  AND c.manuscript_id = chapter_pre_collab.manuscript_id
			  AND c.id = chapter_pre_collab.chapter_id
			  AND c.deleted_at IS NOT NULL
		 );
	`)
	return err
}
