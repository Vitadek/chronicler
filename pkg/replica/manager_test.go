package replica

import (
	"context"
	"database/sql"
	"testing"

	"chronicle-server/pkg/config"

	_ "modernc.org/sqlite"
)

type mockProvider struct {
	puts    map[string][]byte
	deleted map[string]bool
}

func (p *mockProvider) Name() string { return "mock" }
func (p *mockProvider) Initialize(ctx context.Context) error { return nil }
func (p *mockProvider) Put(ctx context.Context, key string, content []byte, opts ReplicaPutOptions) error {
	p.puts[key] = content
	return nil
}
func (p *mockProvider) Head(ctx context.Context, key string) (*ReplicaObjectMetadata, error) {
	if _, ok := p.puts[key]; ok {
		return &ReplicaObjectMetadata{Key: key}, nil
	}
	return nil, nil
}
func (p *mockProvider) Get(ctx context.Context, key string) ([]byte, error) {
	if val, ok := p.puts[key]; ok {
		return val, nil
	}
	return nil, nil
}
func (p *mockProvider) Delete(ctx context.Context, key string) error {
	p.deleted[key] = true
	delete(p.puts, key)
	return nil
}
func (p *mockProvider) List(ctx context.Context, prefix string) ([]ReplicaObjectMetadata, error) {
	var list []ReplicaObjectMetadata
	for k := range p.puts {
		list = append(list, ReplicaObjectMetadata{Key: k})
	}
	return list, nil
}
func (p *mockProvider) Close() error { return nil }

func initTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)

	// Create necessary replica and schema tables
	statements := []string{
		`CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT, expires_at INTEGER)`,
		`CREATE TABLE manuscripts (user_id TEXT, id TEXT, data TEXT, last_modified INTEGER, deleted_at INTEGER, revision INTEGER, PRIMARY KEY(user_id, id))`,
		`CREATE TABLE chapters (user_id TEXT, manuscript_id TEXT, id TEXT, title TEXT, content TEXT, position INTEGER, last_modified INTEGER, deleted_at INTEGER, revision INTEGER, PRIMARY KEY(user_id, manuscript_id, id))`,
		`CREATE TABLE profiles (user_id TEXT PRIMARY KEY, data TEXT, last_modified INTEGER, revision INTEGER)`,
		`CREATE TABLE sync_changes (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id TEXT, entity TEXT, manuscript_id TEXT, record_id TEXT, operation TEXT, revision INTEGER, changed_at INTEGER)`,
		`CREATE TABLE storage_blobs (key TEXT PRIMARY KEY, content BLOB NOT NULL, content_type TEXT, checksum TEXT NOT NULL, generation INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE storage_replica_generations (key TEXT PRIMARY KEY, generation INTEGER NOT NULL)`,
		`CREATE TABLE storage_replica_manifest (key TEXT PRIMARY KEY, operation TEXT NOT NULL, payload BLOB, content_type TEXT, checksum TEXT, generation INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE storage_replication_outbox (key TEXT PRIMARY KEY, operation TEXT NOT NULL, payload BLOB, content_type TEXT, checksum TEXT, generation INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL DEFAULT 0, last_attempt_at INTEGER, last_error TEXT, dead_letter INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)`,
		`CREATE TABLE storage_replication_state (id INTEGER PRIMARY KEY CHECK (id = 1), initialized_at INTEGER, last_attempt_at INTEGER, last_success_at INTEGER, last_error TEXT)`,
		`INSERT INTO storage_replication_state (id, initialized_at) VALUES (1, NULL)`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("migration statement failed: %v\nstmt: %s", err, stmt)
		}
	}

	return db
}

func TestBackoff(t *testing.T) {
	for attempt := 1; attempt <= 10; attempt++ {
		val := backoffMs(attempt)
		if val < 1000 {
			t.Errorf("backoff too small for attempt %d: %d", attempt, val)
		}
		if val > 360000 {
			t.Errorf("backoff too large for attempt %d: %d", attempt, val)
		}
	}
}

func TestReconciliationAndSeeding(t *testing.T) {
	db := initTestDB(t)
	defer db.Close()

	cfg := &config.Config{}
	cfg.Storage.Replica = "s3"
	cfg.Storage.MaxAttempts = 5
	cfg.Storage.RetryIntervalMs = 50

	mgr, err := NewManager(cfg, db)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Substitute provider with mock
	mockProv := &mockProvider{
		puts:    make(map[string][]byte),
		deleted: make(map[string]bool),
	}
	mgr.provider = mockProv

	// Insert mock database entities
	_, err = db.Exec(`INSERT INTO manuscripts(user_id, id, data, last_modified, deleted_at, revision) VALUES (?, ?, ?, ?, NULL, ?)`,
		"user1", "manuscript1", `{"title":"Hello"}`, 1000, 1)
	if err != nil {
		t.Fatalf("failed to insert manuscript: %v", err)
	}

	_, err = db.Exec(`INSERT INTO chapters(user_id, manuscript_id, id, title, content, position, last_modified, deleted_at, revision) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		"user1", "manuscript1", "chapter1", "Chapter One", "Prose body", 0, 1000, 1)
	if err != nil {
		t.Fatalf("failed to insert chapter: %v", err)
	}

	// Seed Portable Manifest
	t.Log("Calling SeedPortableDatabaseManifest...")
	checked, enqueued, err := mgr.SeedPortableDatabaseManifest()
	if err != nil {
		t.Fatalf("SeedPortableDatabaseManifest failed: %v", err)
	}
	t.Logf("SeedPortableDatabaseManifest done: checked=%d enqueued=%d", checked, enqueued)

	if checked != 2 {
		t.Errorf("expected 2 checked entities, got %d", checked)
	}
	if enqueued != 2 {
		t.Errorf("expected 2 enqueued entities, got %d", enqueued)
	}

	// Verify entries exist in storage_replica_manifest
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM storage_replica_manifest").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query replica manifest: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 manifest entries, got %d", count)
	}

	// Reconcile replica target
	t.Log("Calling ReconcileReplicaTarget...")
	changed, seeded := mgr.ReconcileReplicaTarget()
	t.Logf("ReconcileReplicaTarget done: changed=%t seeded=%d", changed, seeded)
	if !changed {
		t.Errorf("expected target to change on first reconciliation")
	}
	if seeded != 2 {
		t.Errorf("expected 2 replica manifest entries to seed outbox, got %d", seeded)
	}

	// Process due replica outbox queue
	t.Log("Calling ProcessDue...")
	err = mgr.ProcessDue(10)
	t.Log("ProcessDue done")
	if err != nil {
		t.Fatalf("ProcessDue failed: %v", err)
	}

	// Verify mock provider puts
	if len(mockProv.puts) != 2 {
		t.Errorf("expected mock provider to receive 2 objects, got %d", len(mockProv.puts))
	}

	// Verify outbox is cleared
	err = db.QueryRow("SELECT COUNT(*) FROM storage_replication_outbox").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query outbox: %v", err)
	}
	if count != 0 {
		t.Errorf("expected outbox to be empty after sync, got %d", count)
	}
}
