package store

import (
	"database/sql"
	"fmt"
)

// migration is one forward-only, additive schema step. Versions are consecutive
// integers starting at 1; each step runs in its own transaction and is recorded
// in schema_migrations only when it commits.
//
// Release rule (FR-140, data-model §0): migrations are expand-only. They add
// tables and columns with defaults and never DROP, RENAME or rebuild a table, so
// the previous binary keeps starting on a migrated database.
type migration struct {
	version int
	name    string
	up      func(tx *sql.Tx) error
}

// migrations is the ordered registry applied by Migrate.
var migrations = []migration{
	{version: 1, name: "additive hardening schema: sessions, api tokens, app/job columns", up: migrateV1Additive},
}

// columnAdd is one ALTER TABLE ... ADD COLUMN of data-model §2.
type columnAdd struct {
	table, column, definition string
}

// v1Columns lists data-model §2 verbatim. Every NOT NULL column has a DEFAULT, so
// rows written by the previous binary stay valid and the previous binary can keep
// running on the migrated database (it never names these columns).
var v1Columns = []columnAdd{
	{"admin_users", "token_version", "token_version INTEGER NOT NULL DEFAULT 0"},
	{"apps", "run_as_user", "run_as_user TEXT NOT NULL DEFAULT ''"},
	{"apps", "hardening", "hardening INTEGER NOT NULL DEFAULT 0"},
	{"apps", "health_url", "health_url TEXT NOT NULL DEFAULT ''"},
	{"apps", "require_digest", "require_digest INTEGER NOT NULL DEFAULT 0"},
	{"apps", "is_self", "is_self INTEGER NOT NULL DEFAULT 0"},
	{"deploy_jobs", "rolled_back", "rolled_back INTEGER NOT NULL DEFAULT 0"},
	{"deploy_jobs", "artifact_sha256", "artifact_sha256 TEXT NOT NULL DEFAULT ''"},
	{"deploy_jobs", "artifact_verified", "artifact_verified INTEGER NOT NULL DEFAULT 0"},
	{"deploy_jobs", "finished_at", "finished_at DATETIME"},
	{"deploy_jobs", "phase_at_end", "phase_at_end TEXT NOT NULL DEFAULT ''"},
}

// v1Statements are the new tables (data-model §1.2, §1.3) and indexes. All are
// IF NOT EXISTS so the step is safe on a partially migrated database.
var v1Statements = []string{
	`CREATE TABLE IF NOT EXISTS admin_sessions (
		id                  TEXT PRIMARY KEY,
		user_id             TEXT NOT NULL,
		created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen_at        DATETIME NOT NULL,
		expires_at          DATETIME NOT NULL,
		absolute_expires_at DATETIME NOT NULL,
		revoked_at          DATETIME,
		ip                  TEXT NOT NULL DEFAULT '',
		user_agent          TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_admin_sessions_user_id ON admin_sessions(user_id)`,
	`CREATE TABLE IF NOT EXISTS api_tokens (
		id           TEXT PRIMARY KEY,
		user_id      TEXT NOT NULL,
		name         TEXT NOT NULL,
		token_hash   TEXT NOT NULL UNIQUE,
		prefix       TEXT NOT NULL,
		created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_used_at DATETIME,
		revoked_at   DATETIME
	)`,
	`CREATE INDEX IF NOT EXISTS idx_deploy_jobs_created_at ON deploy_jobs(created_at)`,
}

func migrateV1Additive(tx *sql.Tx) error {
	for _, c := range v1Columns {
		if err := addColumnIfMissing(tx, c.table, c.column, c.definition); err != nil {
			return err
		}
	}
	for _, stmt := range v1Statements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("%w (statement: %.60s)", err, stmt)
		}
	}
	return nil
}

// Migrate applies every pending migration. It must run after the stores that own
// the base tables (apps, admin_users, deploy_jobs) have created them, because
// migrations ALTER those tables. It is idempotent: already recorded versions are
// skipped, so a restart re-runs nothing.
func Migrate(db *sql.DB) error {
	return migrateWith(db, migrations)
}

// validateMigrations requires versions 1..N in strictly consecutive order, a
// non-empty name and a non-nil up function.
func validateMigrations(ms []migration) error {
	for i, m := range ms {
		want := i + 1
		if m.version != want {
			return fmt.Errorf("migrations: entry %d has version %d, want %d (versions must be consecutive from 1)", i, m.version, want)
		}
		if m.name == "" {
			return fmt.Errorf("migrations: version %d has an empty name", m.version)
		}
		if m.up == nil {
			return fmt.Errorf("migrations: version %d has no up function", m.version)
		}
	}
	return nil
}

func migrateWith(db *sql.DB, ms []migration) error {
	if err := validateMigrations(ms); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		name       TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return err
		}
	}
	return nil
}

// appliedVersions reads the recorded versions and closes the cursor before any
// migration runs (the pool is a single connection in production).
func appliedVersions(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("migrate: scan schema_migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate: iterate schema_migrations: %w", err)
	}
	return applied, nil
}

func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("migrate: begin version %d (%s): %w", m.version, m.name, err)
	}
	defer tx.Rollback() // no-op after Commit

	// Another process may have applied it between our read and this transaction.
	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&done); err != nil {
		return fmt.Errorf("migrate: check version %d (%s): %w", m.version, m.name, err)
	}
	if done > 0 {
		return nil
	}
	if err := m.up(tx); err != nil {
		return fmt.Errorf("migrate: apply version %d (%s): %w", m.version, m.name, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, m.version, m.name); err != nil {
		return fmt.Errorf("migrate: record version %d (%s): %w", m.version, m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate: commit version %d (%s): %w", m.version, m.name, err)
	}
	return nil
}

// addColumnIfMissing runs `ALTER TABLE <table> ADD COLUMN <definition>` unless
// the column already exists. It checks PRAGMA table_info instead of matching the
// "duplicate column" error text. definition is "<name> <type> ..." and is
// compiled-in DDL, never user input.
func addColumnIfMissing(tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	found, hasTable := false, false
	for rows.Next() {
		hasTable = true
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("inspect %s: %w", table, err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	if !hasTable {
		return fmt.Errorf("table %s does not exist (construct its store before Migrate)", table)
	}
	if found {
		return nil
	}
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + definition); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}
