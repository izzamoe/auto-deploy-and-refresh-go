package coordinator

import (
	"database/sql"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
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
