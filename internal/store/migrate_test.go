package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// tableExists reports whether a table is present in sqlite_master.
func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatalf("sqlite_master lookup for %s: %v", name, err)
	}
	return n == 1
}

type appliedRow struct {
	version   int
	name      string
	appliedAt string
}

func appliedMigrations(t *testing.T, db *sql.DB) []appliedRow {
	t.Helper()
	rows, err := db.Query(`SELECT version, name, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	defer rows.Close()
	var out []appliedRow
	for rows.Next() {
		var r appliedRow
		if err := rows.Scan(&r.version, &r.name, &r.appliedAt); err != nil {
			t.Fatalf("scan schema_migrations: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// createTable returns a migration that creates one table and counts its runs.
func createTable(version int, name, table string, runs *int) migration {
	return migration{
		version: version,
		name:    name,
		up: func(tx *sql.Tx) error {
			*runs++
			_, err := tx.Exec(`CREATE TABLE ` + table + ` (id INTEGER PRIMARY KEY)`)
			return err
		},
	}
}

func TestMigrate_AppliesOnceAndRecordsVersion(t *testing.T) {
	db := newTestDB(t)
	var r1, r2 int
	migs := []migration{
		createTable(1, "create first", "mig_first", &r1),
		createTable(2, "create second", "mig_second", &r2),
	}

	if err := migrateWith(db, migs); err != nil {
		t.Fatalf("migrateWith: %v", err)
	}

	if r1 != 1 || r2 != 1 {
		t.Fatalf("each migration must run exactly once, got runs %d and %d", r1, r2)
	}
	if !tableExists(t, db, "mig_first") || !tableExists(t, db, "mig_second") {
		t.Fatal("migrated tables are missing")
	}
	got := appliedMigrations(t, db)
	if len(got) != 2 {
		t.Fatalf("want 2 recorded versions, got %v", got)
	}
	if got[0].version != 1 || got[0].name != "create first" || got[1].version != 2 || got[1].name != "create second" {
		t.Fatalf("unexpected schema_migrations content: %+v", got)
	}
	for _, r := range got {
		if strings.TrimSpace(r.appliedAt) == "" {
			t.Fatalf("version %d has an empty applied_at", r.version)
		}
	}
}

func TestMigrate_IdempotentOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		db.SetMaxOpenConns(1)
		return db
	}

	var r1, r2, r3 int
	migs := []migration{
		createTable(1, "create first", "mig_first", &r1),
		createTable(2, "create second", "mig_second", &r2),
	}

	db := open()
	if err := migrateWith(db, migs); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart with the same binary: nothing runs again and nothing errors even
	// though the tables already exist.
	db = open()
	if err := migrateWith(db, migs); err != nil {
		t.Fatalf("restart with same migrations: %v", err)
	}
	if r1 != 1 || r2 != 1 {
		t.Fatalf("restart re-ran migrations: runs %d and %d", r1, r2)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart with a newer binary: only the new version is applied.
	migs = append(migs, createTable(3, "create third", "mig_third", &r3))
	db = open()
	defer db.Close()
	if err := migrateWith(db, migs); err != nil {
		t.Fatalf("restart with a new migration: %v", err)
	}
	if r1 != 1 || r2 != 1 || r3 != 1 {
		t.Fatalf("want runs 1,1,1 got %d,%d,%d", r1, r2, r3)
	}
	if got := appliedMigrations(t, db); len(got) != 3 || got[2].version != 3 {
		t.Fatalf("unexpected schema_migrations after upgrade: %+v", got)
	}
}

func TestMigrate_OlderBinaryIgnoresNewerVersions(t *testing.T) {
	db := newTestDB(t)
	var r1, r2 int
	newer := []migration{
		createTable(1, "create first", "mig_first", &r1),
		createTable(2, "create second", "mig_second", &r2),
	}
	if err := migrateWith(db, newer); err != nil {
		t.Fatal(err)
	}
	// A downgraded binary only knows version 1; version 2 stays recorded and the
	// start must not fail (expand-only schema, FR-140).
	if err := migrateWith(db, newer[:1]); err != nil {
		t.Fatalf("older binary must start on a newer schema: %v", err)
	}
	if got := appliedMigrations(t, db); len(got) != 2 {
		t.Fatalf("newer version must remain recorded: %+v", got)
	}
}

func TestMigrate_FailureRollsBackThenRetries(t *testing.T) {
	db := newTestDB(t)
	var r1 int
	boom := errors.New("boom")
	failing := migration{
		version: 2,
		name:    "half applied",
		up: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`CREATE TABLE mig_half (id INTEGER PRIMARY KEY)`); err != nil {
				return err
			}
			return boom
		},
	}

	err := migrateWith(db, []migration{createTable(1, "create first", "mig_first", &r1), failing})
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("want error wrapping boom, got %v", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Fatalf("error should name the failing version: %v", err)
	}
	if tableExists(t, db, "mig_half") {
		t.Fatal("failed migration was not rolled back")
	}
	got := appliedMigrations(t, db)
	if len(got) != 1 || got[0].version != 1 {
		t.Fatalf("only version 1 may be recorded after the failure, got %+v", got)
	}

	// Fixed migration 2 is retried on the next start; version 1 is not re-run.
	var r2 int
	if err := migrateWith(db, []migration{createTable(1, "create first", "mig_first", &r1), createTable(2, "create second", "mig_second", &r2)}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if r1 != 1 || r2 != 1 {
		t.Fatalf("want runs 1,1 got %d,%d", r1, r2)
	}
}

func TestMigrate_RejectsBadRegistry(t *testing.T) {
	noop := func(*sql.Tx) error { return nil }
	cases := map[string][]migration{
		"duplicate version":   {{1, "a", noop}, {1, "b", noop}},
		"decreasing version":  {{2, "a", noop}, {1, "b", noop}},
		"does not start at 1": {{2, "a", noop}},
		"gap":                 {{1, "a", noop}, {3, "b", noop}},
		"empty name":          {{1, "", noop}},
		"nil up":              {{1, "a", nil}},
	}
	for name, migs := range cases {
		migs := migs
		t.Run(name, func(t *testing.T) {
			db := newTestDB(t)
			if err := migrateWith(db, migs); err == nil {
				t.Fatal("expected a registry validation error")
			}
			if tableExists(t, db, "schema_migrations") && len(appliedMigrations(t, db)) != 0 {
				t.Fatal("a rejected registry must not apply anything")
			}
		})
	}
}

// The package-level registry itself must be well formed.
func TestMigrate_BuiltInRegistryIsValid(t *testing.T) {
	if err := validateMigrations(migrations); err != nil {
		t.Fatalf("built-in migrations invalid: %v", err)
	}
}
