package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// enqueueAndDequeue moves one job for (appID, tag) to in_progress and returns
// its job ID.
func enqueueAndDequeue(t *testing.T, q *DeployQueue, appID, tag string) string {
	t.Helper()
	if err := q.Enqueue(appID, tag); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	id, gotTag, err := q.DequeueNext(appID)
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	if id == "" || gotTag != tag {
		t.Fatalf("DequeueNext returned (%q, %q), want tag %q", id, gotTag, tag)
	}
	return id
}

func mustIsDuplicate(t *testing.T, q *DeployQueue, appID, tag string, want bool) {
	t.Helper()
	dup, err := q.IsDuplicate(appID, tag)
	if err != nil {
		t.Fatalf("IsDuplicate: %v", err)
	}
	if dup != want {
		t.Fatalf("IsDuplicate(%q, %q) = %v, want %v", appID, tag, dup, want)
	}
}

// TestIsDuplicate_ServedFromCache proves reads come from the snapshot: a row
// deleted behind the cache's back stays visible until invalidation.
func TestIsDuplicate_ServedFromCache(t *testing.T) {
	q := newTestQueue(t, 10)
	if err := q.Enqueue("app-1", "v1.0.0"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	mustIsDuplicate(t, q, "app-1", "v1.0.0", true) // warms cache

	if _, err := q.db.Exec(`DELETE FROM deploy_jobs`); err != nil {
		t.Fatalf("raw delete: %v", err)
	}
	mustIsDuplicate(t, q, "app-1", "v1.0.0", true) // stale snapshot still served

	q.InvalidateActiveJobsCache()
	mustIsDuplicate(t, q, "app-1", "v1.0.0", false) // rebuild sees the delete
}

func TestIsDuplicate_InvalidatedOnEnqueue(t *testing.T) {
	q := newTestQueue(t, 10)
	mustIsDuplicate(t, q, "app-1", "v1.0.0", false) // warms empty cache
	if err := q.Enqueue("app-1", "v1.0.0"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	mustIsDuplicate(t, q, "app-1", "v1.0.0", true)
}

func TestIsDuplicate_InvalidatedOnEnqueueManual(t *testing.T) {
	q := newTestQueue(t, 10)
	mustIsDuplicate(t, q, "app-1", "v1.0.0", false)
	if err := q.EnqueueManual("app-1", "v1.0.0"); err != nil {
		t.Fatalf("EnqueueManual: %v", err)
	}
	mustIsDuplicate(t, q, "app-1", "v1.0.0", true)
}

// TestIsDuplicate_ActiveAcrossDequeue: pending → in_progress keeps the job in
// the active set.
func TestIsDuplicate_ActiveAcrossDequeue(t *testing.T) {
	q := newTestQueue(t, 10)
	mustIsDuplicate(t, q, "app-1", "v1.0.0", false)
	enqueueAndDequeue(t, q, "app-1", "v1.0.0")
	mustIsDuplicate(t, q, "app-1", "v1.0.0", true)
}

func TestIsDuplicate_InvalidatedOnMarkDone(t *testing.T) {
	q := newTestQueue(t, 10)
	id := enqueueAndDequeue(t, q, "app-1", "v1.0.0")
	mustIsDuplicate(t, q, "app-1", "v1.0.0", true) // warms cache
	if err := q.MarkDone(id, true, "", nil); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	mustIsDuplicate(t, q, "app-1", "v1.0.0", false)
}

func TestIsDuplicate_InvalidatedOnRecoverStale(t *testing.T) {
	q := newTestQueue(t, 10)
	enqueueAndDequeue(t, q, "app-1", "v1.0.0")
	mustIsDuplicate(t, q, "app-1", "v1.0.0", true)
	if err := q.RecoverStale(); err != nil {
		t.Fatalf("RecoverStale: %v", err)
	}
	mustIsDuplicate(t, q, "app-1", "v1.0.0", false)
}

func TestIsDuplicate_InvalidatedOnCreateRetryJob(t *testing.T) {
	q := newTestQueue(t, 10)
	id := enqueueAndDequeue(t, q, "app-1", "v1.0.0")
	if err := q.MarkDone(id, false, "boom", nil); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	mustIsDuplicate(t, q, "app-1", "v1.0.0", false) // warms cache
	if _, err := q.CreateRetryJob(id, "app-1", "v1.0.0"); err != nil {
		t.Fatalf("CreateRetryJob: %v", err)
	}
	mustIsDuplicate(t, q, "app-1", "v1.0.0", true)
}

// TestIsDuplicate_InvalidatedOnSaveJobLog checks the uniform rule "every
// deploy_jobs write drops the snapshot" — observable via the internal pointer
// because SaveJobLog never changes set membership.
func TestIsDuplicate_InvalidatedOnSaveJobLog(t *testing.T) {
	q := newTestQueue(t, 10)
	id := enqueueAndDequeue(t, q, "app-1", "v1.0.0")
	mustIsDuplicate(t, q, "app-1", "v1.0.0", true) // warms cache
	if q.activeCache.Load() == nil {
		t.Fatal("cache should be populated after read")
	}
	if err := q.SaveJobLog(id, "some log"); err != nil {
		t.Fatalf("SaveJobLog: %v", err)
	}
	if q.activeCache.Load() != nil {
		t.Fatal("cache should be invalidated after SaveJobLog")
	}
}

// TestIsDuplicate_InvalidatedOnAppDelete: AppStore.Delete removes an app's
// deploy_jobs rows outside DeployQueue; the SetJobsDeletedHook wiring must
// drop the queue's cache.
func TestIsDuplicate_InvalidatedOnAppDelete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	appStore, err := NewAppStore(db)
	if err != nil {
		t.Fatalf("NewAppStore: %v", err)
	}
	appStore.SetJobsDeletedHook(q.InvalidateActiveJobsCache) // as wired in main

	app, err := appStore.Create("app", "secret", "/tmp/bin", "svc", "o/r", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Enqueue(app.ID, "v1.0.0"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	mustIsDuplicate(t, q, app.ID, "v1.0.0", true) // warms cache

	if err := appStore.Delete(app.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	mustIsDuplicate(t, q, app.ID, "v1.0.0", false)
}

// TestIsDuplicate_ConcurrentReadWrite hammers cached reads against writers
// that invalidate; run with -race to catch unsynchronized access.
func TestIsDuplicate_ConcurrentReadWrite(t *testing.T) {
	q := newTestQueue(t, 1000)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := q.IsDuplicate("app-1", "v1.0.0"); err != nil {
					t.Errorf("IsDuplicate: %v", err)
					return
				}
			}
		})
	}
	for i := range 100 {
		tag := fmt.Sprintf("v0.0.%d", i)
		if err := q.Enqueue("app-1", tag); err != nil {
			t.Errorf("Enqueue: %v", err)
			break
		}
		id, _, err := q.DequeueNext("app-1")
		if err != nil {
			t.Errorf("DequeueNext: %v", err)
			break
		}
		if err := q.MarkDone(id, true, "", nil); err != nil {
			t.Errorf("MarkDone: %v", err)
			break
		}
	}
	close(stop)
	wg.Wait()
}

// TestActiveCache_BoundedByActiveJobs proves the snapshot only ever holds
// non-terminal jobs: a huge terminal history contributes zero entries, so the
// cache cannot accumulate over the app's lifetime.
func TestActiveCache_BoundedByActiveJobs(t *testing.T) {
	q := newTestQueue(t, 10)
	// 5000 finished jobs (the "pile" that grows forever in the DB)…
	for i := range 5000 {
		if _, err := q.db.Exec(
			`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES (?, ?, 'app-1', ?, 'succeeded', 'webhook')`,
			fmt.Sprintf("hist-%d", i), i+1, fmt.Sprintf("v0.0.%d", i),
		); err != nil {
			t.Fatalf("seed history: %v", err)
		}
	}
	// …and 3 active ones.
	for i := range 3 {
		if err := q.Enqueue("app-1", fmt.Sprintf("v9.9.%d", i)); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	m, err := q.loadActiveCache()
	if err != nil {
		t.Fatalf("loadActiveCache: %v", err)
	}
	total := 0
	for _, tags := range m {
		total += len(tags)
	}
	if total != 3 {
		t.Fatalf("cache holds %d entries, want 3 (history must not enter the cache)", total)
	}
}

// TestEnqueue_UniqueActiveIndexBlocksConcurrentDuplicate proves the partial
// UNIQUE index closes the check-then-insert race in IsDuplicate: when N
// goroutines Enqueue the SAME (app, tag) at once, the DB must admit exactly
// one active job — the rest get ErrDuplicate — instead of stacking duplicates.
func TestEnqueue_UniqueActiveIndexBlocksConcurrentDuplicate(t *testing.T) {
	// Match production's single-connection pool (main sets SetMaxOpenConns(1)),
	// which serializes writers so the loser gets a clean ErrDuplicate instead of
	// SQLITE_BUSY from multiple connections fighting over the file.
	dbPath := filepath.Join(t.TempDir(), "race.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	q, err := NewDeployQueue(db, 1000)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const racers = 16
	var wg sync.WaitGroup
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start // release all goroutines together to maximize the race
			errs[i] = q.Enqueue("app-1", "v1.0.0")
		})
	}
	close(start)
	wg.Wait()

	ok, dup, other := 0, 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrDuplicate):
			dup++
		default:
			other++
		}
	}
	if other != 0 {
		t.Fatalf("unexpected non-duplicate errors: %d", other)
	}
	if ok != 1 {
		t.Fatalf("admitted %d jobs, want exactly 1 (rest must be ErrDuplicate)", ok)
	}
	if dup != racers-1 {
		t.Fatalf("got %d ErrDuplicate, want %d", dup, racers-1)
	}

	// Ground truth: the table holds exactly one active row for (app-1, v1.0.0).
	var active int
	if err := q.db.QueryRow(
		`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = 'app-1' AND tag = 'v1.0.0'
		 AND status IN ('pending','in_progress','cancel_requested')`,
	).Scan(&active); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if active != 1 {
		t.Fatalf("table holds %d active duplicate jobs, want 1", active)
	}
}

// TestMigrate_DedupsPreexistingActiveDuplicates proves the migration heals a
// DB that already contains duplicate active jobs (from before the guard) so
// the UNIQUE index can be created: the earliest job survives, extras become
// failed.
func TestMigrate_DedupsPreexistingActiveDuplicates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dup.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Build the table WITHOUT the unique index, then plant 3 active duplicates.
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE deploy_jobs (
		id TEXT PRIMARY KEY, seq INTEGER NOT NULL, app_id TEXT NOT NULL, tag TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending', trigger_type TEXT NOT NULL DEFAULT 'webhook',
		retry_of_job_id TEXT, error_msg TEXT,
		download_bytes INTEGER NOT NULL DEFAULT 0, download_duration_ms INTEGER NOT NULL DEFAULT 0,
		download_speed_bps REAL NOT NULL DEFAULT 0, deploy_log TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	for i, status := range []string{"pending", "in_progress", "pending"} {
		if _, err := db.Exec(
			`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES (?, ?, 'app-1', 'v1.0.0', ?, 'webhook')`,
			fmt.Sprintf("dup-%d", i), i+1, status,
		); err != nil {
			t.Fatalf("seed dup %d: %v", i, err)
		}
	}

	// Migrate must dedup, then succeed creating the unique index.
	if err := q.Migrate(); err != nil {
		t.Fatalf("Migrate with pre-existing duplicates: %v", err)
	}

	var active int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM deploy_jobs WHERE status IN ('pending','in_progress','cancel_requested')`,
	).Scan(&active); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if active != 1 {
		t.Fatalf("after dedup %d active jobs remain, want 1", active)
	}
	// The survivor is the earliest-enqueued (seq=1).
	var survivorSeq int
	if err := db.QueryRow(
		`SELECT seq FROM deploy_jobs WHERE status IN ('pending','in_progress','cancel_requested')`,
	).Scan(&survivorSeq); err != nil {
		t.Fatalf("survivor seq: %v", err)
	}
	if survivorSeq != 1 {
		t.Fatalf("survivor seq = %d, want 1 (earliest enqueued)", survivorSeq)
	}
}
