package replica

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"chronicle-server/pkg/config"
)

type Manager struct {
	cfg      *config.Config
	db       *sql.DB
	provider ReplicaProvider
	keyLocks map[string]chan struct{}
	mu       sync.Mutex
	stopChan chan struct{}
}

// Replica work is deliberately bounded. A restore or target reconciliation can
// enqueue hundreds of objects at once; opening one S3/WebDAV request per row
// creates a connection burst and can make every request time out together.
// Four workers keep the queue off the request path while providing ample
// throughput for small manuscript objects.
const processDueConcurrency = 4

func NewManager(cfg *config.Config, db *sql.DB) (*Manager, error) {
	var provider ReplicaProvider
	var err error

	switch cfg.Storage.Replica {
	case "s3":
		provider, err = NewS3Provider(cfg)
	case "nextcloud":
		provider, err = NewNextcloudProvider(cfg)
	}
	if err != nil {
		return nil, err
	}

	return &Manager{
		cfg:      cfg,
		db:       db,
		provider: provider,
		keyLocks: make(map[string]chan struct{}),
		stopChan: make(chan struct{}),
	}, nil
}

func (m *Manager) Start() {
	if m.provider == nil {
		return
	}

	go func() {
		// Wait short moment for DB bootstrap
		time.Sleep(100 * time.Millisecond)

		// Proactively initialize remote provider connection
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		m.recordInitialize(m.provider.Initialize(ctx))
		cancel()

		// Initial due jobs drain
		_ = m.ProcessDue(20)

		interval := time.Duration(m.cfg.Storage.RetryIntervalMs) * time.Millisecond
		if interval <= 0 {
			interval = 5000 * time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				_ = m.ProcessDue(20)
			case <-m.stopChan:
				return
			}
		}
	}()
}

func (m *Manager) Close() error {
	close(m.stopChan)
	if m.provider != nil {
		return m.provider.Close()
	}
	return nil
}

// recordInitialize persists the outcome of a provider Initialize call.
//
// GetStatus treats a NULL initialized_at as "degraded", so without this the
// replica could never report healthy no matter how well it was working —
// /readyz stayed degraded forever and `initialized` was always false. The Node
// server did the equivalent (`updateState({ initializedAt: Date.now(),
// lastError: null })` in HybridManager) and this port simply never wrote the
// column. Caught by tests/formal, whose readiness test waits for
// replica.state === 'healthy'.
func (m *Manager) recordInitialize(err error) {
	if err != nil {
		_, _ = m.db.Exec(`
			UPDATE storage_replication_state
			SET last_attempt_at = ?, last_error = ?
			WHERE id = 1
		`, time.Now().UnixMilli(), err.Error())
		return
	}
	_, _ = m.db.Exec(`
		UPDATE storage_replication_state
		SET initialized_at = COALESCE(initialized_at, ?), last_error = NULL
		WHERE id = 1
	`, time.Now().UnixMilli())
}

func (m *Manager) GetStatus() map[string]interface{} {
	providerName := "none"
	state := "disabled"
	var lastAttemptAt *int64
	var lastSuccessAt *int64
	var lastError *string

	if m.provider != nil {
		providerName = m.provider.Name()
		state = "healthy"

		var initializedAtVal sql.NullInt64
		var lastAttemptAtVal sql.NullInt64
		var lastSuccessAtVal sql.NullInt64
		var lastErrorVal sql.NullString

		err := m.db.QueryRow(`
			SELECT initialized_at, last_attempt_at, last_success_at, last_error
			FROM storage_replication_state WHERE id = 1
		`).Scan(&initializedAtVal, &lastAttemptAtVal, &lastSuccessAtVal, &lastErrorVal)

		if err == nil {
			if lastAttemptAtVal.Valid {
				t := lastAttemptAtVal.Int64
				lastAttemptAt = &t
			}
			if lastSuccessAtVal.Valid {
				t := lastSuccessAtVal.Int64
				lastSuccessAt = &t
			}
			if lastErrorVal.Valid && lastErrorVal.String != "" {
				s := lastErrorVal.String
				lastError = &s
				state = "degraded"
			}
			if !initializedAtVal.Valid {
				state = "degraded"
			}
		}
	}

	var pending int
	var deadLetters int
	_ = m.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN dead_letter = 1 THEN 1 ELSE 0 END), 0)
		FROM storage_replication_outbox
	`).Scan(&pending, &deadLetters)

	if deadLetters > 0 {
		state = "degraded"
	}

	return map[string]interface{}{
		"provider":      providerName,
		"state":         state,
		"initialized":   m.provider == nil || state == "healthy",
		"pending":       pending,
		"deadLetters":   deadLetters,
		"lastAttemptAt": lastAttemptAt,
		"lastSuccessAt": lastSuccessAt,
		"lastError":     lastError,
	}
}

func backoffMs(failedAttempts int) int64 {
	shift := failedAttempts - 1
	if shift > 8 {
		shift = 8
	}
	base := 1000 * (1 << shift)
	if base > 300000 {
		base = 300000
	}

	jitterMax := int(float64(base) * 0.2)
	jitter := 0
	if jitterMax > 1 {
		jitter = rand.Intn(jitterMax)
	}
	return int64(base + jitter)
}

type outboxJob struct {
	Key         string
	Operation   string
	Payload     []byte
	ContentType string
	Checksum    string
	Generation  int
	Attempts    int
}

func (m *Manager) getJob(key string) (*outboxJob, error) {
	var job outboxJob
	var payload []byte
	var ct sql.NullString
	var checksum sql.NullString

	err := m.db.QueryRow(`
		SELECT key, operation, payload, content_type, checksum, generation, attempts
		FROM storage_replication_outbox WHERE key = ?
	`, key).Scan(&job.Key, &job.Operation, &payload, &ct, &checksum, &job.Generation, &job.Attempts)

	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	job.Payload = payload
	if ct.Valid {
		job.ContentType = ct.String
	}
	if checksum.Valid {
		job.Checksum = checksum.String
	}
	return &job, nil
}

func (m *Manager) syncJob(ctx context.Context, job *outboxJob) error {
	attemptedAt := time.Now().UnixNano() / int64(time.Millisecond)

	_, _ = m.db.Exec(`
		UPDATE storage_replication_outbox
		SET last_attempt_at = ?
		WHERE key = ? AND generation = ?
	`, attemptedAt, job.Key, job.Generation)

	_, _ = m.db.Exec(`
		UPDATE storage_replication_state
		SET last_attempt_at = ?
		WHERE id = 1
	`, attemptedAt)

	var syncErr error
	if job.Operation == "put" {
		if job.Payload == nil {
			syncErr = fmt.Errorf("replica PUT %s has no payload", job.Key)
		} else {
			syncErr = m.provider.Put(ctx, job.Key, job.Payload, ReplicaPutOptions{
				ContentType: job.ContentType,
				Checksum:    job.Checksum,
				Generation:  job.Generation,
			})
		}
	} else {
		syncErr = m.provider.Delete(ctx, job.Key)
	}

	if syncErr == nil {
		_, _ = m.db.Exec(`
			DELETE FROM storage_replication_outbox
			WHERE key = ? AND generation = ?
		`, job.Key, job.Generation)

		now := time.Now().UnixNano() / int64(time.Millisecond)
		_, _ = m.db.Exec(`
			UPDATE storage_replication_state
			SET last_success_at = ?, last_error = NULL
			WHERE id = 1
		`, now)
		return nil
	}

	// Calculate backoff and update attempts
	attempts := job.Attempts + 1
	deadLetter := 0
	if attempts >= m.cfg.Storage.MaxAttempts {
		deadLetter = 1
	}

	nextAttemptAt := attemptedAt + backoffMs(attempts)
	errStr := syncErr.Error()
	log.Printf("[replica] %s generation %d failed (attempt %d/%d): %v", job.Key, job.Generation, attempts, m.cfg.Storage.MaxAttempts, syncErr)

	_, _ = m.db.Exec(`
		UPDATE storage_replication_outbox SET
			attempts = ?,
			next_attempt_at = ?,
			last_attempt_at = ?,
			last_error = ?,
			dead_letter = ?
		WHERE key = ? AND generation = ?
	`, attempts, nextAttemptAt, attemptedAt, errStr, deadLetter, job.Key, job.Generation)

	_, _ = m.db.Exec(`
		UPDATE storage_replication_state SET
			last_attempt_at = ?,
			last_error = ?
		WHERE id = 1
	`, attemptedAt, errStr)

	return syncErr
}

func (m *Manager) lockKey(key string) (chan struct{}, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ch, exists := m.keyLocks[key]; exists {
		return ch, false
	}
	ch := make(chan struct{})
	m.keyLocks[key] = ch
	return ch, true
}

func (m *Manager) unlockKey(key string, ch chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	close(ch)
	delete(m.keyLocks, key)
}

func (m *Manager) ScheduleSyncKey(key string) {
	if m.provider == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = m.SyncKey(ctx, key)
	}()
}

func (m *Manager) SyncKey(ctx context.Context, key string) error {
	ch, locked := m.lockKey(key)
	if !locked {
		// Already being processed: wait for completion
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
	defer func() {
		m.unlockKey(key, ch)
	}()

	for {
		job, err := m.getJob(key)
		if err != nil {
			return err
		}
		if job == nil || job.Attempts >= m.cfg.Storage.MaxAttempts {
			return nil
		}
		// If next_attempt_at is in future, wait or skip
		now := time.Now().UnixNano() / int64(time.Millisecond)
		var nextAttemptAt sql.NullInt64
		_ = m.db.QueryRow("SELECT next_attempt_at FROM storage_replication_outbox WHERE key = ?", key).Scan(&nextAttemptAt)
		if nextAttemptAt.Valid && nextAttemptAt.Int64 > now {
			return nil
		}

		if err := m.syncJob(ctx, job); err != nil {
			return err
		}
	}
}

func (m *Manager) ProcessDue(limit int) error {
	if m.provider == nil {
		return nil
	}

	now := time.Now().UnixNano() / int64(time.Millisecond)
	rows, err := m.db.Query(`
		SELECT key FROM storage_replication_outbox
		WHERE dead_letter = 0 AND next_attempt_at <= ?
		ORDER BY created_at
		LIMIT ?
	`, now, limit)
	if err != nil {
		return err
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err == nil {
			keys = append(keys, k)
		}
	}
	rows.Close()

	workerCount := processDueConcurrency
	if len(keys) < workerCount {
		workerCount = len(keys)
	}
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range jobs {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				_ = m.SyncKey(ctx, key)
				cancel()
			}
		}()
	}
	for _, key := range keys {
		jobs <- key
	}
	close(jobs)
	wg.Wait()
	return nil
}

func (m *Manager) SeedPortableDatabaseManifest() (int, int, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	checked := 0
	enqueued := 0

	// 1. Manuscripts
	mRows, err := tx.Query("SELECT user_id, id, data, last_modified, deleted_at, revision FROM manuscripts")
	if err != nil {
		return 0, 0, fmt.Errorf("query manuscripts failed: %w", err)
	}
	defer mRows.Close()
	type mRow struct {
		userID       string
		id           string
		data         string
		lastModified int64
		deletedAt    sql.NullInt64
		revision     int
	}
	var mList []mRow
	for mRows.Next() {
		var r mRow
		if errScan := mRows.Scan(&r.userID, &r.id, &r.data, &r.lastModified, &r.deletedAt, &r.revision); errScan != nil {
			return 0, 0, fmt.Errorf("scan manuscript failed: %w", errScan)
		}
		mList = append(mList, r)
	}
	mRows.Close()

	for _, row := range mList {
		checked++
		key := fmt.Sprintf("v1/users/%s/manuscripts/%s/metadata.json", url.QueryEscape(row.userID), url.QueryEscape(row.id))
		var changed bool
		var errPut error

		if row.deletedAt.Valid {
			bytes := SerializeManuscriptTombstone(row.userID, row.id, row.deletedAt.Int64, row.revision)
			changed, errPut = enqueuePutIfChanged(tx, key, bytes, "application/json")
			// Cover cleanup lives in the delete path itself (pkg/db
			// DeleteManuscript -> EnqueueCoverDeletes), not here: this walk
			// only runs at startup/target-change, far too late for a
			// manuscript deleted while the server is running.
		} else {
			bytes, errSer := SerializeManuscript(row.userID, row.id, row.lastModified, row.revision, row.data)
			if errSer != nil {
				return 0, 0, fmt.Errorf("serialize manuscript failed: %w", errSer)
			}
			changed, errPut = enqueuePutIfChanged(tx, key, bytes, "application/json")
		}
		if errPut != nil {
			return 0, 0, fmt.Errorf("enqueue manuscript failed: %w", errPut)
		}
		if changed {
			enqueued++
		}
	}

	// 2. Chapters
	cRows, err := tx.Query("SELECT user_id, manuscript_id, id, title, content, position, last_modified, deleted_at, revision FROM chapters")
	if err != nil {
		return 0, 0, fmt.Errorf("query chapters failed: %w", err)
	}
	defer cRows.Close()
	type cRow struct {
		userID       string
		manuscriptID string
		id           string
		title        sql.NullString
		content      sql.NullString
		position     sql.NullInt64
		lastModified int64
		deletedAt    sql.NullInt64
		revision     int
	}
	var cList []cRow
	for cRows.Next() {
		var r cRow
		if errScan := cRows.Scan(&r.userID, &r.manuscriptID, &r.id, &r.title, &r.content, &r.position, &r.lastModified, &r.deletedAt, &r.revision); errScan != nil {
			return 0, 0, fmt.Errorf("scan chapter failed: %w", errScan)
		}
		cList = append(cList, r)
	}
	cRows.Close()

	for _, row := range cList {
		checked++
		key := fmt.Sprintf("v1/users/%s/manuscripts/%s/chapters/%s.html", url.QueryEscape(row.userID), url.QueryEscape(row.manuscriptID), url.QueryEscape(row.id))
		var changed bool
		var errPut error

		if row.deletedAt.Valid {
			bytes := SerializeChapterTombstone(row.userID, row.manuscriptID, row.id, row.deletedAt.Int64, row.revision)
			changed, errPut = enqueuePutIfChanged(tx, key, bytes, "text/html; charset=utf-8")
		} else {
			title := ""
			if row.title.Valid {
				title = row.title.String
			}
			content := ""
			if row.content.Valid {
				content = row.content.String
			}
			position := 0
			if row.position.Valid {
				position = int(row.position.Int64)
			}
			bytes := SerializeChapter(row.userID, row.manuscriptID, row.id, title, position, row.lastModified, row.revision, content)
			changed, errPut = enqueuePutIfChanged(tx, key, bytes, "text/html; charset=utf-8")
		}
		if errPut != nil {
			return 0, 0, fmt.Errorf("enqueue chapter failed: %w", errPut)
		}
		if changed {
			enqueued++
		}
	}

	// 3. Profiles
	pRows, err := tx.Query("SELECT user_id, data, last_modified, revision FROM profiles")
	if err != nil {
		return 0, 0, fmt.Errorf("query profiles failed: %w", err)
	}
	defer pRows.Close()
	type pRow struct {
		userID       string
		data         string
		lastModified int64
		revision     int
	}
	var pList []pRow
	for pRows.Next() {
		var r pRow
		if errScan := pRows.Scan(&r.userID, &r.data, &r.lastModified, &r.revision); errScan != nil {
			return 0, 0, fmt.Errorf("scan profile failed: %w", errScan)
		}
		pList = append(pList, r)
	}
	pRows.Close()

	for _, row := range pList {
		checked++
		key := fmt.Sprintf("v1/users/%s/profile.json", url.QueryEscape(row.userID))
		bytes, errSer := SerializeProfile(row.userID, row.data, row.lastModified, row.revision)
		if errSer != nil {
			return 0, 0, fmt.Errorf("serialize profile failed: %w", errSer)
		}
		changed, errPut := enqueuePutIfChanged(tx, key, bytes, "application/json")
		if errPut != nil {
			return 0, 0, fmt.Errorf("enqueue profile failed: %w", errPut)
		}
		if changed {
			enqueued++
		}
	}

	if errCommit := tx.Commit(); errCommit != nil {
		return 0, 0, fmt.Errorf("commit seed transaction failed: %w", errCommit)
	}

	return checked, enqueued, nil
}

func (m *Manager) seedReplicaManifest() int {
	rows, err := m.db.Query("SELECT key, operation, payload, content_type, checksum, generation FROM storage_replica_manifest")
	if err != nil {
		return 0
	}
	defer rows.Close()

	type manifestRow struct {
		key         string
		operation   string
		payload     []byte
		contentType string
		checksum    string
		generation  int
	}

	var list []manifestRow
	for rows.Next() {
		var r manifestRow
		var payloadVal []byte
		var ctVal sql.NullString
		var csVal sql.NullString
		if errScan := rows.Scan(&r.key, &r.operation, &payloadVal, &ctVal, &csVal, &r.generation); errScan == nil {
			r.payload = payloadVal
			if ctVal.Valid {
				r.contentType = ctVal.String
			}
			if csVal.Valid {
				r.checksum = csVal.String
			}
			list = append(list, r)
		}
	}
	rows.Close()

	tx, err := m.db.Begin()
	if err != nil {
		return 0
	}
	defer tx.Rollback()

	count := 0
	for _, r := range list {
		err = EnqueueAtGeneration(tx, r.key, r.operation, r.generation, r.payload, r.contentType, r.checksum)
		if err == nil {
			count++
			m.ScheduleSyncKey(r.key)
		}
	}

	_ = tx.Commit()
	return count
}

func (m *Manager) replicaTargetFingerprint() string {
	if m.cfg.Storage.Replica == "none" {
		return "none"
	}
	var target interface{}
	if m.cfg.Storage.Replica == "s3" {
		target = map[string]string{
			"provider": "s3",
			"bucket":   m.cfg.S3.Bucket,
			"endpoint": m.cfg.S3.Endpoint,
			"prefix":   m.cfg.S3.Prefix,
			"region":   m.cfg.S3.Region,
		}
	} else {
		target = map[string]string{
			"provider": "nextcloud",
			"url":      m.cfg.Nextcloud.Url,
			"user":     m.cfg.Nextcloud.User,
			"root":     m.cfg.Nextcloud.StorageDir,
		}
	}
	bytes, _ := json.Marshal(target)
	return fmt.Sprintf("%s:%s", m.cfg.Storage.Replica, Sha256(bytes))
}

func (m *Manager) ReconcileReplicaTarget() (bool, int) {
	key := "storage/replica-target-fingerprint"
	target := m.replicaTargetFingerprint()

	var previous string
	err := m.db.QueryRow("SELECT v FROM kv WHERE k = ?", key).Scan(&previous)
	if err == nil && previous == target {
		return false, 0
	}

	seeded := 0
	if target != "none" {
		seeded = m.seedReplicaManifest()
	} else {
		// Clean up outbox when disabled
		_, _ = m.db.Exec("DELETE FROM storage_replication_outbox")
	}

	_, _ = m.db.Exec(`
		INSERT INTO kv(k, v, expires_at) VALUES (?, ?, NULL)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v, expires_at = NULL
	`, key, target)

	return true, seeded
}

func enqueuePutIfChanged(q Queryable, key string, content []byte, contentType string) (bool, error) {
	checksum := Sha256(content)
	var op string
	var curChecksum sql.NullString
	err := q.QueryRow("SELECT operation, checksum FROM storage_replica_manifest WHERE key = ?", key).Scan(&op, &curChecksum)
	if err == nil && op == "put" && curChecksum.Valid && curChecksum.String == checksum {
		return false, nil
	}

	err = EnqueueReplicaPut(q, key, content, contentType)
	return true, err
}

type VerificationMismatch struct {
	Key                string `json:"key"`
	ExpectedChecksum   string `json:"expectedChecksum"`
	ActualChecksum     string `json:"actualChecksum"`
	ExpectedGeneration int    `json:"expectedGeneration"`
	ActualGeneration   int    `json:"actualGeneration"`
}

type ReplicaVerificationResult struct {
	Checked      int                    `json:"checked"`
	Matched      int                    `json:"matched"`
	Missing      []string               `json:"missing"`
	Unexpected   []string               `json:"unexpected"`
	Mismatched   []VerificationMismatch `json:"mismatched"`
	Unverifiable []string               `json:"unverifiable"`
}

func (m *Manager) Verify(ctx context.Context, prefix string) (*ReplicaVerificationResult, error) {
	if m.provider == nil {
		return nil, errors.New("remote replication is disabled")
	}

	if err := m.provider.Initialize(ctx); err != nil {
		m.recordInitialize(err)
		return nil, err
	}
	m.recordInitialize(nil)

	res := &ReplicaVerificationResult{
		Missing:      []string{},
		Unexpected:   []string{},
		Mismatched:   []VerificationMismatch{},
		Unverifiable: []string{},
	}

	pattern := prefix + "%"
	rows, err := m.db.Query(`
		SELECT key, operation, checksum, generation
		FROM storage_replica_manifest
		WHERE key LIKE ?
		ORDER BY key
	`, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type manifestEntry struct {
		key        string
		operation  string
		checksum   string
		generation int
	}

	var desired []manifestEntry
	desiredKeys := make(map[string]bool)

	for rows.Next() {
		var e manifestEntry
		var checksumVal sql.NullString
		if errScan := rows.Scan(&e.key, &e.operation, &checksumVal, &e.generation); errScan == nil {
			if checksumVal.Valid {
				e.checksum = checksumVal.String
			}
			desired = append(desired, e)
			desiredKeys[e.key] = true
		}
	}
	rows.Close()

	for _, local := range desired {
		res.Checked++

		remote, errHead := m.provider.Head(ctx, local.key)
		if local.operation == "delete" {
			if errHead == nil && remote != nil {
				res.Unexpected = append(res.Unexpected, local.key)
			} else {
				res.Matched++
			}
			continue
		}

		if errHead != nil || remote == nil {
			res.Missing = append(res.Missing, local.key)
			continue
		}

		content, errGet := m.provider.Get(ctx, local.key)
		if errGet != nil || content == nil {
			res.Missing = append(res.Missing, local.key)
			continue
		}

		actualChecksum := Sha256(content)
		checksumMatches := strings.ToLower(actualChecksum) == strings.ToLower(local.checksum)

		metadataChecksumMatches := remote.Checksum == nil || strings.ToLower(*remote.Checksum) == strings.ToLower(local.checksum)
		generationMatches := remote.Generation == nil || *remote.Generation == local.generation

		if !checksumMatches || !metadataChecksumMatches || !generationMatches {
			actualGen := 0
			if remote.Generation != nil {
				actualGen = *remote.Generation
			}
			actualCS := ""
			if remote.Checksum != nil {
				actualCS = *remote.Checksum
			}
			res.Mismatched = append(res.Mismatched, VerificationMismatch{
				Key:                local.key,
				ExpectedChecksum:   local.checksum,
				ActualChecksum:     actualCS,
				ExpectedGeneration: local.generation,
				ActualGeneration:   actualGen,
			})
		} else if remote.Checksum == nil || remote.Generation == nil {
			res.Unverifiable = append(res.Unverifiable, local.key)
		} else {
			res.Matched++
		}
	}

	// Bidirectional: check for unexpected files on remote
	remoteObjects, errList := m.provider.List(ctx, prefix)
	if errList == nil {
		unexpectedSet := make(map[string]bool)
		for _, u := range res.Unexpected {
			unexpectedSet[u] = true
		}

		for _, remote := range remoteObjects {
			if !desiredKeys[remote.Key] {
				unexpectedSet[remote.Key] = true
			}
		}

		var finalUnexpected []string
		for k := range unexpectedSet {
			finalUnexpected = append(finalUnexpected, k)
		}
		sort.Strings(finalUnexpected)
		res.Unexpected = finalUnexpected
	}

	return res, nil
}

func (m *Manager) RetryDeadLetters(key *string) (int, error) {
	var result sql.Result
	var err error

	if key != nil {
		result, err = m.db.Exec(`
			UPDATE storage_replication_outbox SET
				attempts = 0, next_attempt_at = 0, last_error = NULL, dead_letter = 0
			WHERE dead_letter = 1 AND key = ?
		`, *key)
	} else {
		result, err = m.db.Exec(`
			UPDATE storage_replication_outbox SET
				attempts = 0, next_attempt_at = 0, last_error = NULL, dead_letter = 0
			WHERE dead_letter = 1
		`)
	}
	if err != nil {
		return 0, err
	}

	// Schedule sync for those keys
	var keys []string
	var rows *sql.Rows
	if key != nil {
		rows, err = m.db.Query("SELECT key FROM storage_replication_outbox WHERE key = ?", *key)
	} else {
		rows, err = m.db.Query("SELECT key FROM storage_replication_outbox WHERE dead_letter = 0 AND next_attempt_at = 0")
	}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var k string
			if errScan := rows.Scan(&k); errScan == nil {
				keys = append(keys, k)
			}
		}
		rows.Close()
		for _, k := range keys {
			m.ScheduleSyncKey(k)
		}
	}

	changes, _ := result.RowsAffected()
	return int(changes), nil
}

func (m *Manager) Provider() ReplicaProvider {
	return m.provider
}

func (m *Manager) SeedLocalBlobs() int {
	rows, err := m.db.Query(`
		SELECT key, content, content_type, generation, checksum
		FROM storage_blobs
	`)
	if err != nil {
		return 0
	}
	defer rows.Close()

	type blobRow struct {
		key         string
		content     []byte
		contentType string
		generation  int
		checksum    string
	}

	var blobs []blobRow
	for rows.Next() {
		var b blobRow
		if errScan := rows.Scan(&b.key, &b.content, &b.contentType, &b.generation, &b.checksum); errScan == nil {
			blobs = append(blobs, b)
		}
	}
	rows.Close()

	tx, err := m.db.Begin()
	if err != nil {
		return 0
	}
	defer tx.Rollback()

	count := 0
	for _, b := range blobs {
		repKey := PortableReplicaKey(b.key)
		err = EnqueueAtGeneration(tx, repKey, "put", b.generation, b.content, b.contentType, b.checksum)
		if err == nil {
			count++
			m.ScheduleSyncKey(repKey)
		}
	}

	_ = tx.Commit()
	return count
}

// recordInitializePublic lets the CLI record an Initialize outcome. `status`
// probes the provider before reporting, and that probe is often the first
// successful connection after a restart — so it must persist the result too,
// or the reported state contradicts what just happened.
func (m *Manager) recordInitializePublic(err error) {
	m.recordInitialize(err)
}

// enqueueCoverDeletes removes a deleted manuscript's cover blobs and enqueues
// the matching remote deletions.
//
// Cover keys are `covers/<user>/<manuscript>.<random>.<ext>`, so the prefix
// `covers/<user>/<manuscript>.` matches every generation of that manuscript's
// cover without touching another manuscript whose id shares a prefix — the
// trailing dot is load-bearing.
// EnqueueCoverDeletes is called from the manuscript delete path in pkg/db.
func EnqueueCoverDeletes(tx Queryable, userID string, manuscriptID string) error {
	prefix := fmt.Sprintf("covers/%s/%s.", userID, manuscriptID)

	rows, err := tx.Query("SELECT key FROM storage_blobs WHERE key LIKE ?", prefix+"%")
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var k string
		if errScan := rows.Scan(&k); errScan != nil {
			rows.Close()
			return errScan
		}
		keys = append(keys, k)
	}
	rows.Close()

	for _, k := range keys {
		if _, errDel := tx.Exec("DELETE FROM storage_blobs WHERE key = ?", k); errDel != nil {
			return errDel
		}
		gen, errGen := NextStorageGeneration(tx, k)
		if errGen != nil {
			return errGen
		}
		if errEnq := EnqueueAtGeneration(tx, PortableReplicaKey(k), "delete", gen, nil, "", ""); errEnq != nil {
			return errEnq
		}
	}
	return nil
}
