package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// newFatLogQueue seeds `history` finished jobs that each carry a `logKB` KB
// deploy_log, plus one active job — to measure whether stored logs leak into
// the hot path's memory use.
func newFatLogQueue(b *testing.B, history, logKB int) (*DeployQueue, string) {
	b.Helper()
	dbPath := filepath.Join(b.TempDir(), "fat.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		b.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		b.Fatalf("NewDeployQueue: %v", err)
	}
	if err := q.Migrate(); err != nil {
		b.Fatalf("Migrate: %v", err)
	}
	b.Cleanup(func() { q.Close() })

	fatLog := strings.Repeat("x", logKB*1024)
	for i := range history {
		if _, err := db.Exec(
			`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, deploy_log) VALUES (?, ?, 'app-1', ?, 'succeeded', 'webhook', ?)`,
			fmt.Sprintf("hist-%d", i), i+1, fmt.Sprintf("v0.0.%d", i), fatLog,
		); err != nil {
			b.Fatalf("seed: %v", err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES ('active', ?, 'app-1', 'v9.9.9', 'in_progress', 'webhook')`,
		history+1,
	); err != nil {
		b.Fatalf("seed active: %v", err)
	}
	return q, dbPath
}

func BenchmarkFatLogs(b *testing.B) {
	for _, logKB := range []int{0, 100, 1024} {
		b.Run(fmt.Sprintf("IsDuplicate/logKB=%d", logKB), func(b *testing.B) {
			q, dbPath := newFatLogQueue(b, 100, logKB)
			if fi, err := os.Stat(dbPath); err == nil {
				b.Logf("db file size: %.1f MB", float64(fi.Size())/1024/1024)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				dup, err := q.IsDuplicate("app-1", "v9.9.9")
				if err != nil {
					b.Fatal(err)
				}
				if !dup {
					b.Fatal("want duplicate")
				}
			}
		})
		b.Run(fmt.Sprintf("CacheRebuild/logKB=%d", logKB), func(b *testing.B) {
			q, _ := newFatLogQueue(b, 100, logKB)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				q.InvalidateActiveJobsCache()
				if _, err := q.loadActiveCache(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
