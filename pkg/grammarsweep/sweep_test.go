package grammarsweep

import (
	"database/sql"
	"testing"
	"time"

	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"
	"chronicle-server/pkg/grammar"
)

func testSweeperDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.InitDB(t.TempDir())
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// insertChapter writes a live chapter directly via SQL — db.AllChapters
// deliberately has no per-user create path (see its doc comment), so tests
// populate the table the same way the real app's save flow eventually does.
func insertChapter(t *testing.T, database *sql.DB, id, content string) {
	t.Helper()
	_, err := database.Exec(`
		INSERT INTO chapters (user_id, manuscript_id, id, title, content, position, last_modified, revision)
		VALUES ('u1', 'm1', ?, 'Chapter', ?, 0, 0, 1)
	`, id, content)
	if err != nil {
		t.Fatalf("insertChapter(%s): %v", id, err)
	}
}

// testIdleThreshold stands in for config.GrammarConfig.SweepIdleThresholdMs
// in tests — an arbitrary value; what matters is idleMs() reporting more or
// less than it.
const testIdleThreshold int64 = 1000

func testDictSweeper(t *testing.T, database *sql.DB) *Sweeper {
	t.Helper()
	dict, err := grammar.Load()
	if err != nil {
		t.Fatalf("grammar.Load: %v", err)
	}
	return &Sweeper{
		cfg:      &config.Config{Grammar: config.GrammarConfig{SweepIdleThresholdMs: testIdleThreshold}},
		database: database,
		dict:     dict,
		stopChan: make(chan struct{}),
		idleMs:   func() int64 { return testIdleThreshold + 1 }, // always "idle" unless overridden
		sleep:    func(time.Duration) {},                        // don't actually wait in tests
	}
}

func TestRunPass_WarmsCacheForEveryParagraph(t *testing.T) {
	database := testSweeperDB(t)
	insertChapter(t, database, "ch1", "<p>She quikly walked to the door and looked around.</p><p>Another perfectly fine sentence here.</p>")

	testDictSweeper(t, database).runPass()

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM grammar_check_cache WHERE engine = ?`, grammar.EngineNative).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 cached paragraphs (native engine, LT unconfigured), got %d", count)
	}
}

func TestRunPass_AbortsImmediatelyOnActivity(t *testing.T) {
	database := testSweeperDB(t)
	// Three chapters, one long paragraph each, so we can tell how far the
	// pass got before stopping.
	insertChapter(t, database, "ch1", "<p>The first chapter has one long paragraph here.</p>")
	insertChapter(t, database, "ch2", "<p>The second chapter has one long paragraph here.</p>")
	insertChapter(t, database, "ch3", "<p>The third chapter has one long paragraph here.</p>")

	s := testDictSweeper(t, database)
	// runPass checks idleMs() BEFORE each paragraph — idle for the first
	// check (let paragraph 1 through), busy for every check after (abort
	// before paragraph 2).
	checksSoFar := 0
	s.idleMs = func() int64 {
		checksSoFar++
		if checksSoFar == 1 {
			return testIdleThreshold + 1
		}
		return 0
	}

	s.runPass()

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM grammar_check_cache`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the pass to stop after exactly 1 paragraph once activity resumed, cached %d", count)
	}
}

func TestRunPass_ResumingIsCheapForAlreadyCachedParagraphs(t *testing.T) {
	database := testSweeperDB(t)
	insertChapter(t, database, "ch1", "<p>A perfectly ordinary sentence to check twice.</p>")

	s := testDictSweeper(t, database)
	s.runPass()
	s.runPass() // resume: should be a no-op cache-wise, not a duplicate insert

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM grammar_check_cache`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one cache row after two passes over unchanged text, got %d", count)
	}
}

func TestStart_NoOpWhenBackgroundSweepDisabled(t *testing.T) {
	database := testSweeperDB(t)
	s := testDictSweeper(t, database)
	s.cfg = &config.Config{Grammar: config.GrammarConfig{BackgroundSweep: false}}

	s.Start() // must not panic or start a goroutine that touches the DB
	s.Close()

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM grammar_check_cache`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no sweep activity when disabled, got %d cached rows", count)
	}
}
