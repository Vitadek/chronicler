package grammar

import (
	"database/sql"
	"errors"
	"testing"

	"chronicle-server/pkg/db"
)

func testCacheDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.InitDB(t.TempDir())
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestCheckCached_MissComputesAndPersists(t *testing.T) {
	database := testCacheDB(t)
	calls := 0
	compute := func() ([]Hit, error) {
		calls++
		return []Hit{{Start: 0, End: 3, Kind: KindMisspelling, Message: "x"}}, nil
	}

	hits, err := CheckCached(database, "teh cat", "native", compute)
	if err != nil {
		t.Fatalf("CheckCached: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected compute to run once on a miss, ran %d times", calls)
	}
	if len(hits) != 1 || hits[0].Message != "x" {
		t.Fatalf("unexpected hits: %+v", hits)
	}
}

func TestCheckCached_HitSkipsCompute(t *testing.T) {
	database := testCacheDB(t)
	calls := 0
	compute := func() ([]Hit, error) {
		calls++
		return []Hit{{Start: 0, End: 3, Kind: KindMisspelling, Message: "x"}}, nil
	}

	if _, err := CheckCached(database, "teh cat", "native", compute); err != nil {
		t.Fatalf("first CheckCached: %v", err)
	}
	hits, err := CheckCached(database, "teh cat", "native", compute)
	if err != nil {
		t.Fatalf("second CheckCached: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected compute to run once total (second call should hit cache), ran %d times", calls)
	}
	if len(hits) != 1 || hits[0].Message != "x" {
		t.Fatalf("unexpected cached hits: %+v", hits)
	}
}

func TestCheckCached_DifferentEnginesDoNotShareEntries(t *testing.T) {
	database := testCacheDB(t)
	nativeCalls, ltCalls := 0, 0

	if _, err := CheckCached(database, "she go", "native", func() ([]Hit, error) {
		nativeCalls++
		return nil, nil
	}); err != nil {
		t.Fatalf("native CheckCached: %v", err)
	}
	if _, err := CheckCached(database, "she go", "languagetool", func() ([]Hit, error) {
		ltCalls++
		return []Hit{{Kind: KindGrammar, Message: "agreement"}}, nil
	}); err != nil {
		t.Fatalf("languagetool CheckCached: %v", err)
	}

	if nativeCalls != 1 || ltCalls != 1 {
		t.Fatalf("expected both engines to compute independently for the same text, got native=%d lt=%d", nativeCalls, ltCalls)
	}
}

func TestCheckCached_ComputeErrorIsNotCached(t *testing.T) {
	database := testCacheDB(t)
	calls := 0
	failThenSucceed := func() ([]Hit, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("boom")
		}
		return []Hit{{Kind: KindGrammar, Message: "ok"}}, nil
	}

	if _, err := CheckCached(database, "text", "native", failThenSucceed); err == nil {
		t.Fatal("expected the first call's error to propagate")
	}
	hits, err := CheckCached(database, "text", "native", failThenSucceed)
	if err != nil {
		t.Fatalf("second CheckCached: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected a failed compute not to be cached, so the second call recomputes; calls=%d", calls)
	}
	if len(hits) != 1 || hits[0].Message != "ok" {
		t.Fatalf("unexpected hits: %+v", hits)
	}
}

func TestCheckCached_NilDatabaseSkipsCache(t *testing.T) {
	calls := 0
	compute := func() ([]Hit, error) {
		calls++
		return []Hit{{Kind: KindGrammar, Message: "ok"}}, nil
	}
	if _, err := CheckCached(nil, "text", "native", compute); err != nil {
		t.Fatalf("CheckCached with nil db: %v", err)
	}
	if _, err := CheckCached(nil, "text", "native", compute); err != nil {
		t.Fatalf("CheckCached with nil db (2nd): %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected compute to run every time with no db, ran %d times", calls)
	}
}
