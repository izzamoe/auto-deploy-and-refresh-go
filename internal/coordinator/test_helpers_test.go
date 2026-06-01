package coordinator

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		t.Fatalf("WAL pragma: %v", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		t.Fatalf("busy_timeout pragma: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestQueue(t *testing.T, maxPending int) *store.DeployQueue {
	t.Helper()
	q, err := store.NewDeployQueue(newTestDB(t), maxPending)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	return q
}
