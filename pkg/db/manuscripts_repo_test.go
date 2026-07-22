package db

import (
	"database/sql"
	"testing"
)

func intPtr(v int) *int { return &v }

func TestPartialPayloadLeavesAbsentChaptersUntouched(t *testing.T) {
	database, err := InitDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	const manuscriptId = "ms-1"
	full := &ManuscriptRecord{
		Metadata: ManuscriptMetadata{ID: manuscriptId, Title: "Book", Author: "Author"},
		Chapters: []ChapterRecord{
			{ID: "ch-1", Title: "One", Content: "<p>one</p>"},
			{ID: "ch-2", Title: "Two", Content: "<p>two</p>"},
			{ID: "ch-3", Title: "Three", Content: "<p>three</p>"},
		},
	}
	result, err := SaveLegacyManuscript(database, LocalUserID, full, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts on initial save: %#v", result.Conflicts)
	}

	loaded, err := LoadManuscript(database, LocalUserID, manuscriptId)
	if err != nil {
		t.Fatal(err)
	}
	ch1Rev := loaded.Chapters[0].Revision
	ch3Rev := loaded.Chapters[2].Revision

	// Partial payload: only chapter 2, with an explicit Position so it isn't
	// renumbered to array index 0.
	partial := &ManuscriptRecord{
		Metadata: ManuscriptMetadata{ID: manuscriptId, Title: "Book", Author: "Author", Revision: loaded.Metadata.Revision},
		Chapters: []ChapterRecord{
			{ID: "ch-2", Title: "Two (revised)", Content: "<p>two revised</p>", Revision: loaded.Chapters[1].Revision, Position: intPtr(1)},
		},
	}
	result, err = SaveLegacyManuscript(database, LocalUserID, partial, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts on partial save: %#v", result.Conflicts)
	}

	after, err := LoadManuscript(database, LocalUserID, manuscriptId)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Chapters) != 3 {
		t.Fatalf("expected 3 chapters, got %d", len(after.Chapters))
	}
	// Order is by position ASC, so ch-1, ch-2, ch-3 still.
	if after.Chapters[0].ID != "ch-1" || after.Chapters[0].Content != "<p>one</p>" || after.Chapters[0].Revision != ch1Rev {
		t.Fatalf("chapter 1 was touched by a payload that omitted it: %#v", after.Chapters[0])
	}
	if after.Chapters[2].ID != "ch-3" || after.Chapters[2].Content != "<p>three</p>" || after.Chapters[2].Revision != ch3Rev {
		t.Fatalf("chapter 3 was touched by a payload that omitted it: %#v", after.Chapters[2])
	}
	if after.Chapters[1].ID != "ch-2" || after.Chapters[1].Content != "<p>two revised</p>" {
		t.Fatalf("chapter 2 was not updated: %#v", after.Chapters[1])
	}
	if after.Chapters[1].Revision != loaded.Chapters[1].Revision+1 {
		t.Fatalf("chapter 2 revision should bump exactly once: got %d, want %d", after.Chapters[1].Revision, loaded.Chapters[1].Revision+1)
	}
}

func TestNilPositionFallsBackToArrayIndex(t *testing.T) {
	database, err := InitDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	const manuscriptId = "ms-2"
	full := &ManuscriptRecord{
		Metadata: ManuscriptMetadata{ID: manuscriptId, Title: "Book", Author: "Author"},
		Chapters: []ChapterRecord{
			{ID: "ch-1", Title: "One", Content: "<p>one</p>"},
			{ID: "ch-2", Title: "Two", Content: "<p>two</p>"},
		},
	}
	if _, err := SaveLegacyManuscript(database, LocalUserID, full, true); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadManuscript(database, LocalUserID, manuscriptId)
	if err != nil {
		t.Fatal(err)
	}

	// Re-save the identical full manuscript, no Position set anywhere — this
	// must be a total no-op (identical short-circuit), matching pre-change
	// behavior exactly.
	resave := &ManuscriptRecord{
		Metadata: ManuscriptMetadata{ID: manuscriptId, Title: "Book", Author: "Author", Revision: loaded.Metadata.Revision},
		Chapters: []ChapterRecord{
			{ID: "ch-1", Title: "One", Content: "<p>one</p>", Revision: loaded.Chapters[0].Revision},
			{ID: "ch-2", Title: "Two", Content: "<p>two</p>", Revision: loaded.Chapters[1].Revision},
		},
	}
	result, err := SaveLegacyManuscript(database, LocalUserID, resave, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %#v", result.Conflicts)
	}

	after, err := LoadManuscript(database, LocalUserID, manuscriptId)
	if err != nil {
		t.Fatal(err)
	}
	if after.Chapters[0].Revision != loaded.Chapters[0].Revision || after.Chapters[1].Revision != loaded.Chapters[1].Revision {
		t.Fatalf("identical no-Position resave must not bump revisions: before=%d/%d after=%d/%d",
			loaded.Chapters[0].Revision, loaded.Chapters[1].Revision, after.Chapters[0].Revision, after.Chapters[1].Revision)
	}
}

func TestPartialPayloadStaleRevisionConflictDoesNotBlockSibling(t *testing.T) {
	database, err := InitDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	const manuscriptId = "ms-3"
	full := &ManuscriptRecord{
		Metadata: ManuscriptMetadata{ID: manuscriptId, Title: "Book", Author: "Author"},
		Chapters: []ChapterRecord{
			{ID: "ch-1", Title: "One", Content: "<p>one</p>"},
			{ID: "ch-2", Title: "Two", Content: "<p>two</p>"},
		},
	}
	if _, err := SaveLegacyManuscript(database, LocalUserID, full, true); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManuscript(database, LocalUserID, manuscriptId)
	if err != nil {
		t.Fatal(err)
	}

	partial := &ManuscriptRecord{
		Metadata: ManuscriptMetadata{ID: manuscriptId, Title: "Book", Author: "Author", Revision: loaded.Metadata.Revision},
		Chapters: []ChapterRecord{
			// Stale revision on purpose.
			{ID: "ch-1", Title: "One (stale writer)", Content: "<p>stale</p>", Revision: loaded.Chapters[0].Revision + 99, Position: intPtr(0)},
			// Correct revision — should still apply even though ch-1 conflicts.
			{ID: "ch-2", Title: "Two (revised)", Content: "<p>two revised</p>", Revision: loaded.Chapters[1].Revision, Position: intPtr(1)},
		},
	}
	result, err := SaveLegacyManuscript(database, LocalUserID, partial, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].ID != "ch-1" || result.Conflicts[0].Reason != "stale-revision" {
		t.Fatalf("expected exactly one stale-revision conflict on ch-1, got: %#v", result.Conflicts)
	}

	after, err := LoadManuscript(database, LocalUserID, manuscriptId)
	if err != nil {
		t.Fatal(err)
	}
	if after.Chapters[0].Content != "<p>one</p>" {
		t.Fatalf("ch-1 (conflicted) must be unchanged, got %q", after.Chapters[0].Content)
	}
	if after.Chapters[1].Content != "<p>two revised</p>" {
		t.Fatalf("ch-2 (valid revision, same payload) should have applied, got %q", after.Chapters[1].Content)
	}
}

func TestPartialSaveRecordsChangeLogAndOutboxOnlyForMutatedChapter(t *testing.T) {
	database, err := InitDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	const manuscriptId = "ms-4"
	full := &ManuscriptRecord{
		Metadata: ManuscriptMetadata{ID: manuscriptId, Title: "Book", Author: "Author"},
		Chapters: []ChapterRecord{
			{ID: "ch-1", Title: "One", Content: "<p>one</p>"},
			{ID: "ch-2", Title: "Two", Content: "<p>two</p>"},
		},
	}
	if _, err := SaveLegacyManuscript(database, LocalUserID, full, true); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManuscript(database, LocalUserID, manuscriptId)
	if err != nil {
		t.Fatal(err)
	}

	// Both storage_replica_manifest and storage_replication_outbox are keyed
	// by replica path (PRIMARY KEY) and upserted via EnqueueAtGeneration
	// (pkg/replica/outbox.go) — re-enqueuing an untouched chapter's path
	// updates its row in place rather than adding a new row, so row COUNT
	// doesn't distinguish "touched" from "untouched". Compare per-key
	// checksums instead: ch-1's must be unchanged, ch-2's must differ.
	checksumFor := func(chapterId string) string {
		t.Helper()
		var checksum sql.NullString
		if err := database.QueryRow(
			`SELECT checksum FROM storage_replication_outbox WHERE key LIKE ?`, "%chapters/"+chapterId+".html",
		).Scan(&checksum); err != nil {
			t.Fatal(err)
		}
		return checksum.String
	}

	changeLogCountFor := func(chapterId string) int {
		t.Helper()
		var count int
		if err := database.QueryRow(
			`SELECT COUNT(*) FROM change_log WHERE entity = 'chapter' AND record_id = ?`, chapterId,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	// Baseline: the initial full save already created one change_log row per
	// chapter (chapter creation). The partial save must add exactly one more
	// for ch-2 (the mutated chapter) and zero more for ch-1 (omitted).
	ch1ChangeRowsBefore := changeLogCountFor("ch-1")
	ch2ChangeRowsBefore := changeLogCountFor("ch-2")
	ch1ChecksumBefore := checksumFor("ch-1")
	ch2ChecksumBefore := checksumFor("ch-2")
	if ch1ChecksumBefore == "" || ch2ChecksumBefore == "" {
		t.Fatal("expected both chapters to have an outbox row with a checksum after the initial full save")
	}

	partial := &ManuscriptRecord{
		Metadata: ManuscriptMetadata{ID: manuscriptId, Title: "Book", Author: "Author", Revision: loaded.Metadata.Revision},
		Chapters: []ChapterRecord{
			{ID: "ch-2", Title: "Two (revised)", Content: "<p>two revised</p>", Revision: loaded.Chapters[1].Revision, Position: intPtr(1)},
		},
	}
	if _, err := SaveLegacyManuscript(database, LocalUserID, partial, false); err != nil {
		t.Fatal(err)
	}

	if got := changeLogCountFor("ch-2"); got != ch2ChangeRowsBefore+1 {
		t.Fatalf("expected exactly 1 NEW change_log row for the mutated chapter ch-2: before=%d after=%d", ch2ChangeRowsBefore, got)
	}
	if got := changeLogCountFor("ch-1"); got != ch1ChangeRowsBefore {
		t.Fatalf("chapter ch-1 was omitted from the payload; it must not gain a new change_log row: before=%d after=%d", ch1ChangeRowsBefore, got)
	}

	if got := checksumFor("ch-1"); got != ch1ChecksumBefore {
		t.Fatalf("ch-1 was omitted from the payload; its replica checksum must not change: before=%q after=%q", ch1ChecksumBefore, got)
	}
	if got := checksumFor("ch-2"); got == ch2ChecksumBefore {
		t.Fatalf("ch-2 content changed; its replica checksum must change: still %q", got)
	}
}

func TestPositionOnlyChangeBumpsRevision(t *testing.T) {
	database, err := InitDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	const manuscriptId = "ms-5"
	full := &ManuscriptRecord{
		Metadata: ManuscriptMetadata{ID: manuscriptId, Title: "Book", Author: "Author"},
		Chapters: []ChapterRecord{
			{ID: "ch-1", Title: "One", Content: "<p>one</p>"},
			{ID: "ch-2", Title: "Two", Content: "<p>two</p>"},
		},
	}
	if _, err := SaveLegacyManuscript(database, LocalUserID, full, true); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManuscript(database, LocalUserID, manuscriptId)
	if err != nil {
		t.Fatal(err)
	}

	// Same title/content, only the position differs (a reorder).
	reorder := &ManuscriptRecord{
		Metadata: ManuscriptMetadata{ID: manuscriptId, Title: "Book", Author: "Author", Revision: loaded.Metadata.Revision},
		Chapters: []ChapterRecord{
			{ID: "ch-1", Title: "One", Content: "<p>one</p>", Revision: loaded.Chapters[0].Revision, Position: intPtr(1)},
		},
	}
	result, err := SaveLegacyManuscript(database, LocalUserID, reorder, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %#v", result.Conflicts)
	}

	after, err := LoadManuscript(database, LocalUserID, manuscriptId)
	if err != nil {
		t.Fatal(err)
	}
	var ch1 *ChapterRecord
	for i := range after.Chapters {
		if after.Chapters[i].ID == "ch-1" {
			ch1 = &after.Chapters[i]
		}
	}
	if ch1 == nil {
		t.Fatal("ch-1 missing after reorder")
	}
	if ch1.Revision != loaded.Chapters[0].Revision+1 {
		t.Fatalf("position-only change must bump revision: got %d, want %d", ch1.Revision, loaded.Chapters[0].Revision+1)
	}
}
