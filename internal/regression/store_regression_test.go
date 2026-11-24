package regression

import (
	"strings"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/deploy"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// B13 (proof P1): legacy migration deadlocks with MaxOpenConns(1) because it Execs
// while rows are open.
func TestRegression_B13_LegacyMigrationDeadlock(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`CREATE TABLE deploy_queue (id INTEGER PRIMARY KEY, tag TEXT, status TEXT, error_msg TEXT, created_at DATETIME, updated_at DATETIME)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deploy_queue (tag,status,created_at,updated_at) VALUES ('v1','succeeded',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	q, _ := store.NewDeployQueue(db, 10)
	done := make(chan error, 1)
	go func() { done <- q.Migrate() }()
	select {
	case err := <-done:
		t.Logf("Migrate returned (no deadlock): err=%v", err)
		if err != nil {
			t.Errorf("B13: Migrate on a legacy DB returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("B13: Migrate() still blocked after 5s -> deadlock (rows open + Exec with MaxOpenConns(1))")
	}
}

// B14 (proof P8): deleting an app must remove its env vars (secrets) and args.
func TestRegression_B14_DeleteAppLeavesEnvAndArgs(t *testing.T) {
	db := openDB(t)
	q, _ := store.NewDeployQueue(db, 10)
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	apps, _ := store.NewAppStore(db)
	envs, _ := store.NewAppEnvStore(db)
	args, _ := store.NewAppArgsStore(db)
	app, err := apps.Create("a", "s3cret", "/opt/a/a", "a.service", "o/r", "a")
	if err != nil {
		t.Fatal(err)
	}
	_ = envs.Set(app.ID, []store.EnvVar{{Name: "BOT_TOKEN", Value: "123:SECRET"}})
	_ = args.Set(app.ID, []string{"--port", "1"})
	if err := apps.Delete(app.ID); err != nil {
		t.Fatal(err)
	}
	gotEnv, _ := envs.Get(app.ID)
	gotArgs, _ := args.Get(app.ID)
	if len(gotEnv) > 0 || len(gotArgs) > 0 {
		t.Errorf("B14: after Delete, env=%v args=%v still stored", gotEnv, gotArgs)
	}
}

// B7 (proof P2): a newline in BinaryPath must not inject systemd directives.
func TestRegression_B7_UnitDirectiveInjection(t *testing.T) {
	app := store.App{
		Name:        "x",
		BinaryPath:  "/opt/x\nExecStartPre=/bin/sh -c 'id > /tmp/pwned'\n/bin",
		ServiceName: "x.service",
	}
	unit, err := deploy.PreviewServiceUnit(app)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rendered unit:\n%s", unit)
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "ExecStartPre=") {
			t.Errorf("B7: injected directive line present: %q", line)
		}
	}
}

// B8 (proof P2b): '%' in Environment values must be escaped (%%) so systemd does
// not expand specifiers.
func TestRegression_B8_PercentNotEscaped(t *testing.T) {
	app := store.App{
		Name:        "x",
		BinaryPath:  "/opt/x/x",
		ServiceName: "x.service",
		EnvVars:     []store.EnvVar{{Name: "PCT", Value: "100%h"}},
	}
	unit, err := deploy.PreviewServiceUnit(app)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rendered unit:\n%s", unit)
	found := false
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "Environment=PCT=") {
			found = true
			if strings.Contains(line, "%h") && !strings.Contains(line, "%%h") {
				t.Errorf("B8: '%%' not escaped in Environment line: %q (systemd expands %%h)", line)
			}
		}
	}
	if !found {
		t.Errorf("B8: no Environment=PCT= line rendered in unit:\n%s", unit)
	}
}
