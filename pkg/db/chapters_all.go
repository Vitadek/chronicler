package db

import "database/sql"

// AllChapters lists every live chapter across every user's library, content
// included. Every other chapter query in this package is user-scoped
// (LoadManuscript, etc.) because it serves a request from a specific
// authenticated user; this one deliberately isn't — it backs the background
// grammar-check sweep (pkg/grammarsweep), which warms the cache for the
// whole server regardless of who wrote what. Modeled on the same
// no-user_id-filter shape pkg/replica/manager.go already uses for full-DB
// replication.
func AllChapters(database *sql.DB) ([]ChapterRecord, error) {
	rows, err := database.Query(`
		SELECT id, content
		  FROM chapters
		 WHERE deleted_at IS NULL AND content IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chapters []ChapterRecord
	for rows.Next() {
		var row struct {
			ID      string
			Content sql.NullString
		}
		if errScan := rows.Scan(&row.ID, &row.Content); errScan != nil {
			return nil, errScan
		}
		chapters = append(chapters, ChapterRecord{ID: row.ID, Content: row.Content.String})
	}
	return chapters, rows.Err()
}
