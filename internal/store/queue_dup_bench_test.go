package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// newBenchQueue creates a DeployQueue backed by a file DB configured like
// production (WAL, single connection), seeded with `history` finished jobs
// and one in_progress job for the probed (app, tag).
func newBenchQueue(b *testing.B, history int) *DeployQueue {
	b.Helper()
	dbPath := filepath.Join(b.TempDir(), "bench.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		b.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		db.Close()
		b.Fatalf("NewDeployQueue: %v", err)
	}
	if err := q.Migrate(); err != nil {
		db.Close()
		b.Fatalf("Migrate: %v", err)
	}
	b.Cleanup(func() { q.Close() })

	for i := range history {
		if _, err := db.Exec(
			`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES (?, ?, '0123456789abcdef0123456789abcdef', ?, 'succeeded', 'webhook')`,
			fmt.Sprintf("hist-%d", i), i+1, fmt.Sprintf("v0.0.%d", i),
		); err != nil {
			b.Fatalf("seed history: %v", err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES ('active-job', ?, '0123456789abcdef0123456789abcdef', 'v9.9.9', 'in_progress', 'webhook')`,
		history+1,
	); err != nil {
		b.Fatalf("seed active job: %v", err)
	}
	return q
}

// isDuplicateUncached replicates the pre-cache IsDuplicate implementation
// (one COUNT query per call) as the benchmark baseline.
func isDuplicateUncached(q *DeployQueue, appID, tag string) (bool, error) {
	var count int
	err := q.db.QueryRow(
		`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ? AND tag = ? AND status IN ('pending', 'in_progress', 'cancel_requested')`,
		appID, tag,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func benchIsDuplicateUncached(b *testing.B, history int) {
	q := newBenchQueue(b, history)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		dup, err := isDuplicateUncached(q, "0123456789abcdef0123456789abcdef", "v9.9.9")
		if err != nil {
			b.Fatal(err)
		}
		if !dup {
			b.Fatal("expected duplicate")
		}
	}
}

func benchIsDuplicatePrepared(b *testing.B, history int) {
	q := newBenchQueue(b, history)
	stmt, err := q.db.Prepare(
		`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ? AND tag = ? AND status IN ('pending', 'in_progress', 'cancel_requested')`,
	)
	if err != nil {
		b.Fatal(err)
	}
	defer stmt.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var count int
		if err := stmt.QueryRow("0123456789abcdef0123456789abcdef", "v9.9.9").Scan(&count); err != nil {
			b.Fatal(err)
		}
		if count == 0 {
			b.Fatal("expected duplicate")
		}
	}
}

func benchIsDuplicateCached(b *testing.B, history int) {
	q := newBenchQueue(b, history)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		dup, err := q.IsDuplicate("0123456789abcdef0123456789abcdef", "v9.9.9")
		if err != nil {
			b.Fatal(err)
		}
		if !dup {
			b.Fatal("expected duplicate")
		}
	}
}

func BenchmarkIsDuplicate(b *testing.B) {
	for _, history := range []int{10, 200, 2000} {
		b.Run(fmt.Sprintf("Uncached/history=%d", history), func(b *testing.B) { benchIsDuplicateUncached(b, history) })
		b.Run(fmt.Sprintf("Prepared/history=%d", history), func(b *testing.B) { benchIsDuplicatePrepared(b, history) })
		b.Run(fmt.Sprintf("Cached/history=%d", history), func(b *testing.B) { benchIsDuplicateCached(b, history) })
	}
}
