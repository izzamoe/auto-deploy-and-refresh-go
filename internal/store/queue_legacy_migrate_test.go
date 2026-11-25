package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// T045 (B13/FR-034): the legacy deploy_queue -> deploy_jobs backfill must not
// deadlock on the single database connection, and must run BEFORE the partial
// UNIQUE index on active (app_id, tag) exists.

func openSingleConnDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // exactly what cmd/server/main.go does
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func migrateWithin(t *testing.T, q *DeployQueue, d time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- q.Migrate() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("Migrate still blocked after %v (deadlock)", d)
		return nil
	}
}

func TestMigrate_LegacyBackfillBeforeUniqueIndex(t *testing.T) {
	db := openSingleConnDB(t)
	if _, err := db.Exec(`CREATE TABLE deploy_queue (id INTEGER PRIMARY KEY, tag TEXT, status TEXT, error_msg TEXT, created_at DATETIME, updated_at DATETIME)`); err != nil {
		t.Fatal(err)
	}
	// The SAME tag is active twice (legacy data predates the unique-index guard), plus
	// one finished row of that tag and an unrelated active tag.
	for _, r := range []struct{ tag, status, msg string }{
		{"v1", "pending", ""},
		{"v1", "in_progress", ""},
		{"v1", "succeeded", ""},
		{"v2", "pending", ""},
		{"v0", "failed", "boom"},
	} {
		if _, err := db.Exec(`INSERT INTO deploy_queue (tag,status,error_msg,created_at,updated_at) VALUES (?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, r.tag, r.status, r.msg); err != nil {
			t.Fatal(err)
		}
	}
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateWithin(t, q, 5*time.Second); err != nil {
		t.Fatalf("Migrate on legacy data: %v", err)
	}

	var total, activeV1, superseded int
	db.QueryRow(`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = 'legacy'`).Scan(&total)
	db.QueryRow(`SELECT COUNT(*) FROM deploy_jobs WHERE app_id='legacy' AND tag='v1' AND status IN ('pending','in_progress','cancel_requested')`).Scan(&activeV1)
	db.QueryRow(`SELECT COUNT(*) FROM deploy_jobs WHERE error_msg LIKE 'superseded:%'`).Scan(&superseded)
	if total != 5 {
		t.Errorf("deploy_jobs has %d legacy rows, want all 5 backfilled", total)
	}
	if activeV1 != 1 || superseded != 1 {
		t.Errorf("active v1 rows = %d (want 1), superseded = %d (want 1): the duplicate must be demoted, the earliest kept", activeV1, superseded)
	}
	var kept string
	db.QueryRow(`SELECT status FROM deploy_jobs WHERE app_id='legacy' AND tag='v1' AND status IN ('pending','in_progress') `).Scan(&kept)
	if kept != "pending" {
		t.Errorf("kept active row has status %q, want the earliest (pending)", kept)
	}
	var failedMsg string
	db.QueryRow(`SELECT error_msg FROM deploy_jobs WHERE app_id='legacy' AND tag='v0'`).Scan(&failedMsg)
	if failedMsg != "boom" {
		t.Errorf("legacy error_msg = %q, want it carried over", failedMsg)
	}

	// The unique index exists afterwards and is enforced.
	var idx int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_deploy_jobs_active_unique'`).Scan(&idx)
	if idx != 1 {
		t.Fatal("idx_deploy_jobs_active_unique was not created")
	}
	if _, err := db.Exec(`INSERT INTO deploy_jobs (id, seq, app_id, tag, status) VALUES ('dup', 99, 'legacy', 'v2', 'pending')`); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("a second active job for (legacy, v2) must violate the unique index, got %v", err)
	}

	// Idempotent: a second Migrate neither re-imports nor fails.
	if err := migrateWithin(t, q, 5*time.Second); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = 'legacy' AND id != 'dup'`).Scan(&total)
	if total != 5 {
		t.Errorf("after a second Migrate there are %d legacy rows, want still 5", total)
	}
}

// A large legacy queue is copied in one transaction without blocking.
func TestMigrate_LegacyBackfillLargeQueueSingleConn(t *testing.T) {
	db := openSingleConnDB(t)
	if _, err := db.Exec(`CREATE TABLE deploy_queue (id INTEGER PRIMARY KEY, tag TEXT, status TEXT, error_msg TEXT, created_at DATETIME, updated_at DATETIME)`); err != nil {
		t.Fatal(err)
	}
	for i := range 500 {
		if _, err := db.Exec(`INSERT INTO deploy_queue (tag,status,created_at,updated_at) VALUES (?, 'succeeded', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, fmt.Sprintf("v%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateWithin(t, q, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM deploy_jobs`).Scan(&n)
	if n != 500 {
		t.Errorf("deploy_jobs has %d rows, want 500", n)
	}
}
