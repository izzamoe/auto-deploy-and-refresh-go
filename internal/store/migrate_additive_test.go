package store

import (
	"database/sql"
	"testing"
)

// newHeadDB builds a database exactly the way the HEAD binary does at startup
// (store constructors + DeployQueue.Migrate), WITHOUT the versioned migrations.
func newHeadDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newTestDB(t)
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatalf("queue migrate: %v", err)
	}
	if _, err := NewAppStore(db); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAdminUserStore(db); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAppEnvStore(db); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAppArgsStore(db); err != nil {
		t.Fatal(err)
	}
	return db
}

type colInfo struct {
	typ     string
	notNull bool
	dflt    sql.NullString
	pk      bool
}

func tableColumns(t *testing.T, db *sql.DB, table string) map[string]colInfo {
	t.Helper()
	rows, err := db.Query(`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	out := map[string]colInfo{}
	for rows.Next() {
		var name string
		var c colInfo
		var nn, pk int
		if err := rows.Scan(&name, &c.typ, &nn, &c.dflt, &pk); err != nil {
			t.Fatal(err)
		}
		c.notNull, c.pk = nn == 1, pk > 0
		out[name] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func indexNames(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_index_list(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	return out
}

// want: dflt "" means "no default" (NULL in table_info).
type colWant struct {
	name    string
	typ     string
	notNull bool
	dflt    string
	pk      bool
}

func checkColumns(t *testing.T, db *sql.DB, table string, wants []colWant) {
	t.Helper()
	got := tableColumns(t, db, table)
	if len(got) == 0 {
		t.Errorf("table %s does not exist", table)
		return
	}
	for _, w := range wants {
		c, ok := got[w.name]
		if !ok {
			t.Errorf("%s.%s is missing", table, w.name)
			continue
		}
		if c.typ != w.typ {
			t.Errorf("%s.%s type = %q, want %q", table, w.name, c.typ, w.typ)
		}
		if c.notNull != w.notNull {
			t.Errorf("%s.%s NOT NULL = %v, want %v", table, w.name, c.notNull, w.notNull)
		}
		if c.pk != w.pk {
			t.Errorf("%s.%s primary key = %v, want %v", table, w.name, c.pk, w.pk)
		}
		switch {
		case w.dflt == "" && c.dflt.Valid:
			t.Errorf("%s.%s has default %q, want none", table, w.name, c.dflt.String)
		case w.dflt != "" && (!c.dflt.Valid || c.dflt.String != w.dflt):
			t.Errorf("%s.%s default = %v, want %s", table, w.name, c.dflt, w.dflt)
		}
	}
}

// TestMigrate_AdditiveColumnsHaveDefaults: every column/table of data-model §1-§2
// exists with the documented type, nullability and default.
func TestMigrate_AdditiveColumnsHaveDefaults(t *testing.T) {
	db := newHeadDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// §2 new columns on existing tables.
	checkColumns(t, db, "admin_users", []colWant{
		{name: "token_version", typ: "INTEGER", notNull: true, dflt: "0"},
	})
	checkColumns(t, db, "apps", []colWant{
		{name: "run_as_user", typ: "TEXT", notNull: true, dflt: "''"},
		{name: "hardening", typ: "INTEGER", notNull: true, dflt: "0"},
		{name: "health_url", typ: "TEXT", notNull: true, dflt: "''"},
		{name: "require_digest", typ: "INTEGER", notNull: true, dflt: "0"},
		{name: "is_self", typ: "INTEGER", notNull: true, dflt: "0"},
	})
	checkColumns(t, db, "deploy_jobs", []colWant{
		{name: "rolled_back", typ: "INTEGER", notNull: true, dflt: "0"},
		{name: "artifact_sha256", typ: "TEXT", notNull: true, dflt: "''"},
		{name: "artifact_verified", typ: "INTEGER", notNull: true, dflt: "0"},
		{name: "finished_at", typ: "DATETIME"},
		{name: "phase_at_end", typ: "TEXT", notNull: true, dflt: "''"},
	})

	// §1.2 / §1.3 new tables.
	checkColumns(t, db, "admin_sessions", []colWant{
		{name: "id", typ: "TEXT", pk: true},
		{name: "user_id", typ: "TEXT", notNull: true},
		{name: "created_at", typ: "DATETIME", notNull: true, dflt: "CURRENT_TIMESTAMP"},
		{name: "last_seen_at", typ: "DATETIME", notNull: true},
		{name: "expires_at", typ: "DATETIME", notNull: true},
		{name: "absolute_expires_at", typ: "DATETIME", notNull: true},
		{name: "revoked_at", typ: "DATETIME"},
		{name: "ip", typ: "TEXT", notNull: true, dflt: "''"},
		{name: "user_agent", typ: "TEXT", notNull: true, dflt: "''"},
	})
	checkColumns(t, db, "api_tokens", []colWant{
		{name: "id", typ: "TEXT", pk: true},
		{name: "user_id", typ: "TEXT", notNull: true},
		{name: "name", typ: "TEXT", notNull: true},
		{name: "token_hash", typ: "TEXT", notNull: true},
		{name: "prefix", typ: "TEXT", notNull: true},
		{name: "created_at", typ: "DATETIME", notNull: true, dflt: "CURRENT_TIMESTAMP"},
		{name: "last_used_at", typ: "DATETIME"},
		{name: "revoked_at", typ: "DATETIME"},
	})

	// §1.1 version bookkeeping.
	checkColumns(t, db, "schema_migrations", []colWant{
		{name: "version", typ: "INTEGER", pk: true},
		{name: "applied_at", typ: "DATETIME", notNull: true, dflt: "CURRENT_TIMESTAMP"},
		{name: "name", typ: "TEXT", notNull: true},
	})
	if got := appliedMigrations(t, db); len(got) != len(migrations) || len(got) == 0 {
		t.Fatalf("schema_migrations has %d rows, want %d (>0)", len(got), len(migrations))
	}

	// Indexes.
	if !indexNames(t, db, "admin_sessions")["idx_admin_sessions_user_id"] {
		t.Error("idx_admin_sessions_user_id is missing")
	}
	if !indexNames(t, db, "deploy_jobs")["idx_deploy_jobs_created_at"] {
		t.Error("idx_deploy_jobs_created_at is missing")
	}
	// token_hash must be UNIQUE: inserting the same hash twice has to fail.
	ins := `INSERT INTO api_tokens (id, user_id, name, token_hash, prefix) VALUES (?, 'u1', 'ci', 'hash-1', 'abcd1234')`
	if _, err := db.Exec(ins, "t1"); err != nil {
		t.Fatalf("insert api token: %v", err)
	}
	if _, err := db.Exec(ins, "t2"); err == nil {
		t.Error("api_tokens.token_hash accepted a duplicate value")
	}

	// Pre-existing indexes/constraints must survive.
	if !indexNames(t, db, "deploy_jobs")["idx_deploy_jobs_active_unique"] {
		t.Error("idx_deploy_jobs_active_unique disappeared")
	}
}

// TestMigrate_DefaultsApplyToExistingRows: rows written by the HEAD binary before
// the migration read back with the documented defaults and keep their old values.
func TestMigrate_DefaultsApplyToExistingRows(t *testing.T) {
	db := newHeadDB(t)
	mustExec(t, db, `INSERT INTO admin_users (id, username, password_hash, must_change_password) VALUES ('u1', 'admin', 'h', 1)`)
	mustExec(t, db, `INSERT INTO apps (id, name, webhook_secret_hash, binary_path, service_name, github_repo, artifact_name) VALUES ('a1', 'app', 'wh', '/opt/a/a', 'a.service', 'o/r', 'a')`)
	mustExec(t, db, `INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES ('j1', 1, 'a1', 'v1', 'succeeded', 'webhook')`)

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var tokenVersion int
	var mustChange int
	if err := db.QueryRow(`SELECT token_version, must_change_password FROM admin_users WHERE id = 'u1'`).Scan(&tokenVersion, &mustChange); err != nil {
		t.Fatal(err)
	}
	if tokenVersion != 0 || mustChange != 1 {
		t.Errorf("admin_users: token_version=%d must_change_password=%d, want 0 and 1", tokenVersion, mustChange)
	}

	var runAs, healthURL, name string
	var hardening, requireDigest, isSelf, enabled int
	if err := db.QueryRow(`SELECT name, enabled, run_as_user, hardening, health_url, require_digest, is_self FROM apps WHERE id = 'a1'`).
		Scan(&name, &enabled, &runAs, &hardening, &healthURL, &requireDigest, &isSelf); err != nil {
		t.Fatal(err)
	}
	if name != "app" || enabled != 1 || runAs != "" || hardening != 0 || healthURL != "" || requireDigest != 0 || isSelf != 0 {
		t.Errorf("apps defaults wrong: %q %d %q %d %q %d %d", name, enabled, runAs, hardening, healthURL, requireDigest, isSelf)
	}

	var rolledBack, verified int
	var sha, phase, status string
	var finished sql.NullString
	if err := db.QueryRow(`SELECT status, rolled_back, artifact_sha256, artifact_verified, finished_at, phase_at_end FROM deploy_jobs WHERE id = 'j1'`).
		Scan(&status, &rolledBack, &sha, &verified, &finished, &phase); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || rolledBack != 0 || sha != "" || verified != 0 || finished.Valid || phase != "" {
		t.Errorf("deploy_jobs defaults wrong: %q %d %q %d %v %q", status, rolledBack, sha, verified, finished, phase)
	}
}

// TestMigrate_BuiltInIdempotent: running the real registry twice is a no-op.
func TestMigrate_BuiltInIdempotent(t *testing.T) {
	db := newHeadDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	before := appliedMigrations(t, db)
	if err := Migrate(db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	after := appliedMigrations(t, db)
	if len(before) != len(after) {
		t.Fatalf("second Migrate changed schema_migrations: %v -> %v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("row %d changed: %+v -> %+v", i, before[i], after[i])
		}
	}
}

// TestMigrate_CompletesPartiallyMigratedDatabase: a column that already exists
// (for example a crash between ALTER statements outside a transaction in an
// earlier build) must not make the migration fail.
func TestMigrate_CompletesPartiallyMigratedDatabase(t *testing.T) {
	db := newHeadDB(t)
	// apps.run_as_user (and the other per-app settings columns) already exist here:
	// NewAppStore adds them itself since T056, which is exactly the "column already
	// there" situation this test is about.
	if _, ok := tableColumns(t, db, "apps")["run_as_user"]; !ok {
		mustExec(t, db, `ALTER TABLE apps ADD COLUMN run_as_user TEXT NOT NULL DEFAULT ''`)
	}
	mustExec(t, db, `ALTER TABLE deploy_jobs ADD COLUMN rolled_back INTEGER NOT NULL DEFAULT 0`)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate on a partially migrated DB: %v", err)
	}
	if _, ok := tableColumns(t, db, "apps")["is_self"]; !ok {
		t.Error("apps.is_self was not added")
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// scanAll runs an explicit-column query and returns every row as []any.
func scanAll(t *testing.T, db *sql.DB, q string, args ...any) [][]any {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %q: %v", q, err)
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMigrate_OldQueriesStillWork replays the explicit SELECT/INSERT/UPDATE/DELETE
// statements of the HEAD binary (copied verbatim from internal/store at 7c385e8)
// against a migrated database. This is what keeps the previous binary startable
// and correct on the new schema (FR-140, SC-013).
func TestMigrate_OldQueriesStillWork(t *testing.T) {
	db := newHeadDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	type stmt struct {
		name    string
		sql     string
		args    []any
		wantRow int // expected number of rows for a SELECT, -1 for Exec
	}
	steps := []stmt{
		// admin_users (admin_user_store.go)
		{"seed user", `INSERT INTO admin_users (id, username, password_hash, must_change_password) VALUES (?, ?, ?, 1)`, []any{"u1", "admin", "hash"}, -1},
		{"user by name", `SELECT id, username, password_hash, must_change_password, created_at, updated_at FROM admin_users WHERE username = ?`, []any{"admin"}, 1},
		{"user password", `UPDATE admin_users SET password_hash = ?, must_change_password = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, []any{"hash2", "u1"}, -1},
		{"user rename", `UPDATE admin_users SET username = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, []any{"root", "u1"}, -1},
		{"user count", `SELECT COUNT(*) FROM admin_users`, nil, 1},

		// apps (app_store.go)
		{"app insert", `INSERT INTO apps (id, name, webhook_secret_hash, binary_path, service_name, github_repo, artifact_name) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]any{"a1", "app", "wh1", "/opt/a/a", "a.service", "o/r", "a"}, -1},
		{"app list", `SELECT id, name, webhook_secret_hash, binary_path, service_name, github_repo, artifact_name, enabled, created_at, updated_at FROM apps ORDER BY created_at, id`, nil, 1},
		{"app by id", `SELECT id, name, webhook_secret_hash, binary_path, service_name, github_repo, artifact_name, enabled, created_at, updated_at FROM apps WHERE id = ?`, []any{"a1"}, 1},
		{"app update", `UPDATE apps SET name = ?, binary_path = ?, service_name = ?, github_repo = ?, artifact_name = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			[]any{"app2", "/opt/a/a2", "a2.service", "o/r2", "a2", 1, "a1"}, -1},
		{"app secret", `UPDATE apps SET webhook_secret_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, []any{"wh2", "a1"}, -1},
		{"app enable", `UPDATE apps SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, []any{0, "a1"}, -1},
		{"app count", `SELECT COUNT(*) FROM apps`, nil, 1},

		// app_env / app_args upserts
		{"env set", `INSERT INTO app_env (app_id, vars, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP) ON CONFLICT(app_id) DO UPDATE SET vars = excluded.vars, updated_at = CURRENT_TIMESTAMP`, []any{"a1", `[]`}, -1},
		{"env get", `SELECT vars FROM app_env WHERE app_id = ?`, []any{"a1"}, 1},
		{"args set", `INSERT INTO app_args (app_id, args, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP) ON CONFLICT(app_id) DO UPDATE SET args = excluded.args, updated_at = CURRENT_TIMESTAMP`, []any{"a1", `[]`}, -1},
		{"args get", `SELECT args FROM app_args WHERE app_id = ?`, []any{"a1"}, 1},

		// deploy_jobs (queue.go)
		{"job webhook", `INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM deploy_jobs), ?, ?, 'pending', 'webhook')`, []any{"j1", "a1", "v1"}, -1},
		{"job manual", `INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM deploy_jobs), ?, ?, 'pending', 'manual_deploy')`, []any{"j2", "a1", "v2"}, -1},
		{"job retry", `INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, retry_of_job_id) VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM deploy_jobs), ?, ?, 'pending', 'manual_retry', ?)`, []any{"j3", "a1", "v3", "j1"}, -1},
		{"active cache", `SELECT app_id, tag FROM deploy_jobs WHERE status IN ('pending', 'in_progress', 'cancel_requested')`, nil, 3},
		{"next pending", `SELECT id, tag FROM deploy_jobs WHERE app_id = ? AND status = 'pending' ORDER BY seq ASC LIMIT 1`, []any{"a1"}, 1},
		{"job start", `UPDATE deploy_jobs SET status = 'in_progress', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, []any{"j1"}, -1},
		{"job finish", `UPDATE deploy_jobs SET status = ?, error_msg = ?, download_bytes = ?, download_duration_ms = ?, download_speed_bps = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status IN ('in_progress', 'cancel_requested')`,
			[]any{"succeeded", nil, 10, 20, 0.5, "j1"}, -1},
		{"job log set", `UPDATE deploy_jobs SET deploy_log = ? WHERE id = ?`, []any{"log", "j1"}, -1},
		{"job log get", `SELECT COALESCE(deploy_log, '') FROM deploy_jobs WHERE id = ?`, []any{"j1"}, 1},
		{"history", `SELECT id, app_id, tag, status, trigger_type, COALESCE(retry_of_job_id, ''), COALESCE(error_msg, ''), download_bytes, download_duration_ms, download_speed_bps, created_at, updated_at FROM deploy_jobs ORDER BY seq DESC`, nil, 3},
		{"history by app", `SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ?`, []any{"a1"}, 1},
		{"recover stale", `UPDATE deploy_jobs SET status = 'failed', error_msg = 'recovered: process crashed', updated_at = CURRENT_TIMESTAMP WHERE status IN ('in_progress', 'cancel_requested')`, nil, -1},
		{"pending count", `SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ? AND status = 'pending'`, []any{"a1"}, 1},

		// app delete transaction pieces
		{"active count", `SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ? AND status IN ('in_progress', 'cancel_requested')`, []any{"a1"}, 1},
		{"delete jobs", `DELETE FROM deploy_jobs WHERE app_id = ?`, []any{"a1"}, -1},
		{"delete app", `DELETE FROM apps WHERE id = ?`, []any{"a1"}, -1},
	}

	for _, s := range steps {
		if s.wantRow < 0 {
			if _, err := db.Exec(s.sql, s.args...); err != nil {
				t.Fatalf("%s: %v\n%s", s.name, err, s.sql)
			}
			continue
		}
		rows := scanAll(t, db, s.sql, s.args...)
		if len(rows) != s.wantRow {
			t.Fatalf("%s: got %d rows, want %d\n%s", s.name, len(rows), s.wantRow, s.sql)
		}
	}

	// The partial UNIQUE index still arbitrates duplicate active jobs.
	mustExec(t, db, `INSERT INTO apps (id, name, webhook_secret_hash, binary_path, service_name, github_repo, artifact_name) VALUES ('a9', 'n9', 'wh9', '/opt/n9', 'n9.service', 'o/r', 'n9')`)
	mustExec(t, db, `INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES ('d1', 100, 'a9', 'v1', 'pending', 'webhook')`)
	if _, err := db.Exec(`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES ('d2', 101, 'a9', 'v1', 'pending', 'webhook')`); err == nil {
		t.Error("duplicate active (app_id, tag) job was accepted after migration")
	}
}

// TestMigrate_OldStoresWorkOnMigratedDB drives the real HEAD store types (their
// SQL is the HEAD SQL) against a migrated database and then re-runs their
// constructors, as the previous binary does at every start.
func TestMigrate_OldStoresWorkOnMigratedDB(t *testing.T) {
	db := newHeadDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// "Old binary start": constructors and queue migrate run again on the new schema.
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatalf("old queue.Migrate on a migrated DB: %v", err)
	}
	apps, err := NewAppStore(db)
	if err != nil {
		t.Fatal(err)
	}
	users, err := NewAdminUserStore(db)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := users.CreateUser("admin", "a-test-password", true); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	app, err := apps.Create("a", "secret", "/opt/a/a", "a.service", "o/r", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Enqueue(app.ID, "v1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got, err := apps.List()
	if err != nil || len(got) != 1 || got[0].ID != app.ID {
		t.Fatalf("List: %v %v", got, err)
	}
	hist, err := q.ListHistory(app.ID, 20)
	if err != nil || len(hist) != 1 || hist[0].Tag != "v1" {
		t.Fatalf("ListHistory: %v %v", hist, err)
	}
	all, err := q.ListAllHistoryPaged(20, 0)
	if err != nil || len(all) != 1 {
		t.Fatalf("ListAllHistoryPaged: %v %v", all, err)
	}
	if err := apps.Delete(app.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
