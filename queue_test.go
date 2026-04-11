package main

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestQueue(t *testing.T, maxPending int) *DeployQueue {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	q, err := NewDeployQueue(db, maxPending)
	if err != nil {
		db.Close()
		t.Fatalf("NewDeployQueue: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

func openTestDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	return db
}

func TestQueueFIFOAndPersistencePerApp(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fifo.db")

	db := openTestDB(t, dbPath)
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	for _, tag := range []string{"v1", "v2", "v3"} {
		if err := q.Enqueue("app1", tag); err != nil {
			t.Fatalf("Enqueue(%s): %v", tag, err)
		}
	}
	if err := q.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2 := openTestDB(t, dbPath)
	q2, err := NewDeployQueue(db2, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue (reopen): %v", err)
	}
	defer q2.Close()

	for _, want := range []string{"v1", "v2", "v3"} {
		id, tag, err := q2.DequeueNext("app1")
		if err != nil {
			t.Fatalf("DequeueNext: %v", err)
		}
		if id == "" {
			t.Fatal("DequeueNext returned empty id, expected a row")
		}
		if tag != want {
			t.Errorf("DequeueNext tag = %q, want %q", tag, want)
		}
	}

	id, tag, err := q2.DequeueNext("app1")
	if err != nil {
		t.Fatalf("DequeueNext (empty): %v", err)
	}
	if id != "" || tag != "" {
		t.Errorf("expected empty queue, got id=%q tag=%q", id, tag)
	}
}

func TestQueueDeduplicatePendingTagWithinApp(t *testing.T) {
	q := newTestQueue(t, 10)

	if err := q.Enqueue("app1", "v1"); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}

	err := q.Enqueue("app1", "v1")
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("second Enqueue = %v, want ErrDuplicate", err)
	}

	count, err := q.PendingCount("app1")
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	if count != 1 {
		t.Errorf("PendingCount = %d, want 1", count)
	}
}

func TestQueueAllowsSameTagAcrossApps(t *testing.T) {
	q := newTestQueue(t, 10)

	if err := q.Enqueue("app1", "v1"); err != nil {
		t.Fatalf("Enqueue app1: %v", err)
	}
	if err := q.Enqueue("app2", "v1"); err != nil {
		t.Fatalf("Enqueue app2: %v", err)
	}

	count1, err := q.PendingCount("app1")
	if err != nil {
		t.Fatalf("PendingCount app1: %v", err)
	}
	count2, err := q.PendingCount("app2")
	if err != nil {
		t.Fatalf("PendingCount app2: %v", err)
	}
	if count1 != 1 || count2 != 1 {
		t.Errorf("PendingCount app1=%d app2=%d, want 1 each", count1, count2)
	}
}

func TestQueueRejectWhenFull(t *testing.T) {
	q := newTestQueue(t, 2)

	if err := q.Enqueue("app1", "v1"); err != nil {
		t.Fatalf("Enqueue v1: %v", err)
	}
	if err := q.Enqueue("app1", "v2"); err != nil {
		t.Fatalf("Enqueue v2: %v", err)
	}

	err := q.Enqueue("app1", "v3")
	if !errors.Is(err, ErrQueueFull) {
		t.Errorf("third Enqueue = %v, want ErrQueueFull", err)
	}

	if err := q.Enqueue("app2", "v3"); err != nil {
		t.Fatalf("Enqueue app2 v3 should succeed (per-app capacity): %v", err)
	}
}

func TestQueueRecoverStale(t *testing.T) {
	q := newTestQueue(t, 10)

	if err := q.Enqueue("app1", "v1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	id, _, err := q.DequeueNext("app1")
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}

	if err := q.RecoverStale(); err != nil {
		t.Fatalf("RecoverStale: %v", err)
	}

	var status, errMsg string
	err = q.db.QueryRow(`SELECT status, COALESCE(error_msg, '') FROM deploy_jobs WHERE id = ?`, id).Scan(&status, &errMsg)
	if err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "failed" {
		t.Errorf("status = %q, want 'failed'", status)
	}
	if errMsg != "recovered: process crashed" {
		t.Errorf("error_msg = %q, want 'recovered: process crashed'", errMsg)
	}
}

func TestQueueDeduplicateInProgress(t *testing.T) {
	q := newTestQueue(t, 10)

	if err := q.Enqueue("app1", "v1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	id, tag, err := q.DequeueNext("app1")
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	if id == "" || tag != "v1" {
		t.Fatalf("DequeueNext = (%q, %q), want (non-empty, v1)", id, tag)
	}

	err = q.Enqueue("app1", "v1")
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("Enqueue in_progress duplicate = %v, want ErrDuplicate", err)
	}
}

func TestQueueDeduplicateCompletedAllowed(t *testing.T) {
	q := newTestQueue(t, 10)

	if err := q.Enqueue("app1", "v1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	id, tag, err := q.DequeueNext("app1")
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	if tag != "v1" {
		t.Fatalf("DequeueNext tag = %q, want v1", tag)
	}

	if err := q.MarkDone(id, true, ""); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	if err := q.Enqueue("app1", "v1"); err != nil {
		t.Errorf("re-enqueue completed tag: %v, want nil", err)
	}

	count, err := q.PendingCount("app1")
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	if count != 1 {
		t.Errorf("PendingCount = %d, want 1", count)
	}
}

func TestQueueMarkDone(t *testing.T) {
	q := newTestQueue(t, 10)

	if err := q.Enqueue("app1", "v1"); err != nil {
		t.Fatalf("Enqueue v1: %v", err)
	}
	id1, _, err := q.DequeueNext("app1")
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	if err := q.MarkDone(id1, true, ""); err != nil {
		t.Fatalf("MarkDone success: %v", err)
	}

	var status1 string
	var errMsg1 sql.NullString
	err = q.db.QueryRow(`SELECT status, error_msg FROM deploy_jobs WHERE id = ?`, id1).Scan(&status1, &errMsg1)
	if err != nil {
		t.Fatalf("query row: %v", err)
	}
	if status1 != "succeeded" {
		t.Errorf("status = %q, want 'succeeded'", status1)
	}
	if errMsg1.Valid {
		t.Errorf("error_msg = %q, want NULL", errMsg1.String)
	}

	if err := q.Enqueue("app1", "v2"); err != nil {
		t.Fatalf("Enqueue v2: %v", err)
	}
	id2, _, err := q.DequeueNext("app1")
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	if err := q.MarkDone(id2, false, "connection timeout"); err != nil {
		t.Fatalf("MarkDone failure: %v", err)
	}

	var status2 string
	var errMsg2 sql.NullString
	err = q.db.QueryRow(`SELECT status, error_msg FROM deploy_jobs WHERE id = ?`, id2).Scan(&status2, &errMsg2)
	if err != nil {
		t.Fatalf("query row: %v", err)
	}
	if status2 != "failed" {
		t.Errorf("status = %q, want 'failed'", status2)
	}
	if !errMsg2.Valid || errMsg2.String != "connection timeout" {
		t.Errorf("error_msg = %q, want 'connection timeout'", errMsg2.String)
	}
}

func TestQueueHistoryOrdering(t *testing.T) {
	q := newTestQueue(t, 10)

	for _, tag := range []string{"v1", "v2", "v3"} {
		if err := q.Enqueue("app1", tag); err != nil {
			t.Fatalf("Enqueue(%s): %v", tag, err)
		}
	}

	history, err := q.ListHistory("app1", 10)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 history items, got %d", len(history))
	}

	if history[0].Tag != "v3" || history[1].Tag != "v2" || history[2].Tag != "v1" {
		t.Errorf("history order = [%s, %s, %s], want [v3, v2, v1]", history[0].Tag, history[1].Tag, history[2].Tag)
	}
}

func TestQueueRetryCreatesNewRow(t *testing.T) {
	q := newTestQueue(t, 10)

	if err := q.Enqueue("app1", "v1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	origID, _, err := q.DequeueNext("app1")
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	if err := q.MarkDone(origID, false, "deploy error"); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	retryID, err := q.CreateRetryJob(origID, "app1", "v1")
	if err != nil {
		t.Fatalf("CreateRetryJob: %v", err)
	}
	if retryID == "" {
		t.Fatal("CreateRetryJob returned empty id")
	}
	if retryID == origID {
		t.Error("retry job ID should differ from original")
	}

	var trigger, retryOf, status string
	err = q.db.QueryRow(
		`SELECT trigger_type, COALESCE(retry_of_job_id, ''), status FROM deploy_jobs WHERE id = ?`, retryID,
	).Scan(&trigger, &retryOf, &status)
	if err != nil {
		t.Fatalf("query retry row: %v", err)
	}
	if trigger != "manual_retry" {
		t.Errorf("trigger = %q, want 'manual_retry'", trigger)
	}
	if retryOf != origID {
		t.Errorf("retry_of_job_id = %q, want %q", retryOf, origID)
	}
	if status != "pending" {
		t.Errorf("status = %q, want 'pending'", status)
	}
}

func TestQueueRetryRejectsDuplicatePending(t *testing.T) {
	q := newTestQueue(t, 10)

	if err := q.Enqueue("app1", "v1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	origID, _, err := q.DequeueNext("app1")
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	if err := q.MarkDone(origID, false, "error"); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	if _, err := q.CreateRetryJob(origID, "app1", "v1"); err != nil {
		t.Fatalf("first CreateRetryJob: %v", err)
	}

	_, err = q.CreateRetryJob(origID, "app1", "v1")
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("second CreateRetryJob = %v, want ErrDuplicate", err)
	}
}

func TestLegacyQueueMigrationBackfillsAppID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")

	db := openTestDB(t, dbPath)

	_, err := db.Exec(`CREATE TABLE deploy_queue (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		tag        TEXT NOT NULL,
		status     TEXT NOT NULL DEFAULT 'pending',
		error_msg  TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO deploy_queue (tag, status) VALUES ('v0.1', 'succeeded'), ('v0.2', 'failed')`)
	if err != nil {
		t.Fatalf("insert legacy rows: %v", err)
	}

	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	defer q.Close()

	history, err := q.ListHistory("legacy", 10)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 migrated rows, got %d", len(history))
	}

	for _, job := range history {
		if job.AppID != "legacy" {
			t.Errorf("migrated job app_id = %q, want 'legacy'", job.AppID)
		}
		if job.ID == "" {
			t.Error("migrated job has empty id")
		}
	}
}

func TestQueueRejectsDuplicateWithinSameApp(t *testing.T) {
	q := newTestQueue(t, 10)

	if err := q.Enqueue("app1", "v1"); err != nil {
		t.Fatalf("Enqueue app1 v1: %v", err)
	}

	if err := q.Enqueue("app2", "v1"); err != nil {
		t.Fatalf("Enqueue app2 v1 should succeed: %v", err)
	}

	err := q.Enqueue("app1", "v1")
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate within app1 = %v, want ErrDuplicate", err)
	}

	err = q.Enqueue("app2", "v1")
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate within app2 = %v, want ErrDuplicate", err)
	}
}

func TestEnqueueManual(t *testing.T) {
	db := newTestDB(t)
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}

	err = q.EnqueueManual("app1", "v1.0.0")
	if err != nil {
		t.Fatalf("EnqueueManual: %v", err)
	}

	var trigger string
	err = db.QueryRow(`SELECT trigger_type FROM deploy_jobs WHERE app_id = ? AND tag = ?`, "app1", "v1.0.0").Scan(&trigger)
	if err != nil {
		t.Fatalf("query job: %v", err)
	}
	if trigger != "manual_deploy" {
		t.Errorf("Expected trigger_type=manual_deploy, got %q", trigger)
	}
}
