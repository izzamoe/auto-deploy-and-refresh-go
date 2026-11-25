package store

import (
	"database/sql"
	"testing"
)

// T046 (B14/FR-035): deleting an app removes its env vars (secrets), args and jobs
// in ONE transaction: all of them or none.

type deleteFixture struct {
	db   *sql.DB
	apps *AppStore
	envs *AppEnvStore
	args *AppArgsStore
	q    *DeployQueue
}

func newDeleteFixture(t *testing.T) *deleteFixture {
	t.Helper()
	db := newTestDB(t)
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	apps, err := NewAppStore(db)
	if err != nil {
		t.Fatal(err)
	}
	envs, err := NewAppEnvStore(db)
	if err != nil {
		t.Fatal(err)
	}
	args, err := NewAppArgsStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return &deleteFixture{db: db, apps: apps, envs: envs, args: args, q: q}
}

func (f *deleteFixture) seedApp(t *testing.T, name string) *App {
	t.Helper()
	app, err := f.apps.Create(name, "secret-"+name, "/opt/"+name+"/"+name, name+".service", "o/"+name, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.envs.Set(app.ID, []EnvVar{{Name: "BOT_TOKEN", Value: "123:SECRET"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.args.Set(app.ID, []string{"--port", "1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Enqueue(app.ID, "v1"); err != nil {
		t.Fatal(err)
	}
	return app
}

func (f *deleteFixture) count(t *testing.T, table, appID string) int {
	t.Helper()
	col := "app_id"
	if table == "apps" {
		col = "id"
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+col+` = ?`, appID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeleteApp_RemovesEnvArgsAndJobsOfThatAppOnly(t *testing.T) {
	f := newDeleteFixture(t)
	doomed := f.seedApp(t, "doomed")
	other := f.seedApp(t, "other")

	if err := f.apps.Delete(doomed.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"apps", "app_env", "app_args", "deploy_jobs"} {
		if n := f.count(t, table, doomed.ID); n != 0 {
			t.Errorf("%s still has %d rows of the deleted app", table, n)
		}
		if n := f.count(t, table, other.ID); n != 1 {
			t.Errorf("%s has %d rows of the other app, want 1 (must be untouched)", table, n)
		}
	}
}

func TestDeleteApp_RollsBackOnChildFailure(t *testing.T) {
	f := newDeleteFixture(t)
	app := f.seedApp(t, "keeper")
	// A child delete that fails AFTER the env and job rows were already deleted in the
	// same transaction: everything must be restored.
	if _, err := f.db.Exec(`CREATE TRIGGER fail_args_delete BEFORE DELETE ON app_args BEGIN SELECT RAISE(ABORT, 'child delete failed'); END`); err != nil {
		t.Fatal(err)
	}

	if err := f.apps.Delete(app.ID); err == nil {
		t.Fatal("Delete must report the child failure")
	}
	for _, table := range []string{"apps", "app_env", "app_args", "deploy_jobs"} {
		if n := f.count(t, table, app.ID); n != 1 {
			t.Errorf("%s has %d rows after a failed Delete, want 1 (transaction must roll back)", table, n)
		}
	}
	if got, _ := f.envs.Get(app.ID); len(got) != 1 || got[0].Value != "123:SECRET" {
		t.Errorf("env vars lost on rollback: %v", got)
	}
}

// AppStore alone (no env/args stores constructed) still deletes.
func TestDeleteApp_WorksWhenEnvAndArgsTablesAreAbsent(t *testing.T) {
	db := newTestDB(t)
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	apps, err := NewAppStore(db)
	if err != nil {
		t.Fatal(err)
	}
	app, err := apps.Create("solo", "s", "/opt/solo/solo", "solo.service", "o/solo", "solo")
	if err != nil {
		t.Fatal(err)
	}
	if err := apps.Delete(app.ID); err != nil {
		t.Fatalf("Delete without app_env/app_args tables: %v", err)
	}
}

func TestDeleteApp_UnknownAppIsNotFoundAndChangesNothing(t *testing.T) {
	f := newDeleteFixture(t)
	app := f.seedApp(t, "keeper")
	if err := f.apps.Delete("does-not-exist"); err == nil {
		t.Fatal("deleting an unknown app must fail")
	}
	if n := f.count(t, "app_env", app.ID); n != 1 {
		t.Errorf("other app's env was touched: %d rows", n)
	}
}
