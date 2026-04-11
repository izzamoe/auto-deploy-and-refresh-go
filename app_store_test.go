package main

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

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
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestAppStore(t *testing.T) *AppStore {
	t.Helper()
	db := newTestDB(t)
	store, err := NewAppStore(db)
	if err != nil {
		t.Fatalf("NewAppStore: %v", err)
	}
	return store
}

func TestAppStoreCreate(t *testing.T) {
	store := newTestAppStore(t)

	app, err := store.Create("myapp", "secret123", "/usr/bin/myapp", "myapp.service", "owner/repo", "myapp-linux")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if app.ID == "" {
		t.Error("expected non-empty ID")
	}
	if app.Name != "myapp" {
		t.Errorf("Name = %q, want %q", app.Name, "myapp")
	}
	if app.BinaryPath != "/usr/bin/myapp" {
		t.Errorf("BinaryPath = %q, want %q", app.BinaryPath, "/usr/bin/myapp")
	}
	if app.ServiceName != "myapp.service" {
		t.Errorf("ServiceName = %q, want %q", app.ServiceName, "myapp.service")
	}
	if app.GithubRepo != "owner/repo" {
		t.Errorf("GithubRepo = %q, want %q", app.GithubRepo, "owner/repo")
	}
	if app.ArtifactName != "myapp-linux" {
		t.Errorf("ArtifactName = %q, want %q", app.ArtifactName, "myapp-linux")
	}
	if !app.Enabled {
		t.Error("expected Enabled = true")
	}
	if app.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if app.UpdatedAt.IsZero() {
		t.Error("expected non-zero UpdatedAt")
	}
}

func TestAppStoreList(t *testing.T) {
	store := newTestAppStore(t)

	for i, name := range []string{"app-a", "app-b", "app-c"} {
		_, err := store.Create(name, "secret-"+name, "/bin/"+name, name+".service", "owner/"+name, name+"-artifact")
		if err != nil {
			t.Fatalf("Create app %d: %v", i, err)
		}
	}

	apps, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(apps) != 3 {
		t.Fatalf("List returned %d apps, want 3", len(apps))
	}
	if apps[0].Name != "app-a" {
		t.Errorf("first app name = %q, want %q", apps[0].Name, "app-a")
	}
	if apps[2].Name != "app-c" {
		t.Errorf("last app name = %q, want %q", apps[2].Name, "app-c")
	}
}

func TestAppStoreGet(t *testing.T) {
	store := newTestAppStore(t)

	created, err := store.Create("testapp", "secret", "/bin/test", "test.service", "o/r", "art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "testapp" {
		t.Errorf("Name = %q, want %q", got.Name, "testapp")
	}

	_, err = store.Get("nonexistent-id")
	if err == nil {
		t.Error("Get(nonexistent) should return error")
	}
}

func TestAppStoreGetBySecretHash(t *testing.T) {
	store := newTestAppStore(t)

	_, err := store.Create("hashapp", "my-secret", "/bin/hashapp", "hashapp.service", "o/h", "h-art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	hash := HashSecret("my-secret")
	app, err := store.GetBySecretHash(hash)
	if err != nil {
		t.Fatalf("GetBySecretHash: %v", err)
	}
	if app == nil {
		t.Fatal("expected non-nil app")
	}
	if app.Name != "hashapp" {
		t.Errorf("Name = %q, want %q", app.Name, "hashapp")
	}

	noApp, err := store.GetBySecretHash("badhash")
	if err != nil {
		t.Fatalf("GetBySecretHash(bad): %v", err)
	}
	if noApp != nil {
		t.Errorf("expected nil for unknown hash, got %+v", noApp)
	}
}

func TestAppStoreHashesSecret(t *testing.T) {
	store := newTestAppStore(t)

	rawSecret := "super-secret-value"
	app, err := store.Create("hashcheck", rawSecret, "/bin/hc", "hc.service", "o/hc", "hc-art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if app.WebhookSecretHash == rawSecret {
		t.Error("stored hash equals raw secret — plaintext is being stored")
	}

	expected := HashSecret(rawSecret)
	if app.WebhookSecretHash != expected {
		t.Errorf("hash = %q, want %q", app.WebhookSecretHash, expected)
	}
}

func TestAppStoreRejectsDuplicateBinaryPath(t *testing.T) {
	store := newTestAppStore(t)

	_, err := store.Create("app1", "secret1", "/bin/shared", "svc1.service", "o/r1", "art1")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	_, err = store.Create("app2", "secret2", "/bin/shared", "svc2.service", "o/r2", "art2")
	if !errors.Is(err, ErrDuplicateApp) {
		t.Errorf("second Create = %v, want ErrDuplicateApp", err)
	}
}

func TestAppStoreRejectsDuplicateServiceName(t *testing.T) {
	store := newTestAppStore(t)

	_, err := store.Create("app1", "secret1", "/bin/app1", "shared.service", "o/r1", "art1")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	_, err = store.Create("app2", "secret2", "/bin/app2", "shared.service", "o/r2", "art2")
	if !errors.Is(err, ErrDuplicateApp) {
		t.Errorf("second Create = %v, want ErrDuplicateApp", err)
	}
}

func TestAppStoreRejectsDuplicateSecretHash(t *testing.T) {
	store := newTestAppStore(t)

	_, err := store.Create("app1", "same-secret", "/bin/app1", "svc1.service", "o/r1", "art1")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	_, err = store.Create("app2", "same-secret", "/bin/app2", "svc2.service", "o/r2", "art2")
	if !errors.Is(err, ErrDuplicateApp) {
		t.Errorf("second Create = %v, want ErrDuplicateApp", err)
	}
}

func TestAppStoreUpdate(t *testing.T) {
	store := newTestAppStore(t)

	app, err := store.Create("orig", "secret", "/bin/orig", "orig.service", "o/orig", "orig-art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	err = store.Update(app.ID, "updated", "/bin/updated", "updated.service", "o/updated", "updated-art", false)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := store.Get(app.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Name != "updated" {
		t.Errorf("Name = %q, want %q", got.Name, "updated")
	}
	if got.BinaryPath != "/bin/updated" {
		t.Errorf("BinaryPath = %q, want %q", got.BinaryPath, "/bin/updated")
	}
	if got.ServiceName != "updated.service" {
		t.Errorf("ServiceName = %q, want %q", got.ServiceName, "updated.service")
	}
	if got.Enabled {
		t.Error("expected Enabled = false after update")
	}
}

func TestAppStoreRotateSecret(t *testing.T) {
	store := newTestAppStore(t)

	app, err := store.Create("rotapp", "old-secret", "/bin/rot", "rot.service", "o/rot", "rot-art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	oldHash := HashSecret("old-secret")
	found, err := store.GetBySecretHash(oldHash)
	if err != nil {
		t.Fatalf("GetBySecretHash(old): %v", err)
	}
	if found == nil {
		t.Fatal("old secret should resolve before rotation")
	}

	err = store.RotateSecret(app.ID, "new-secret")
	if err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}

	found, err = store.GetBySecretHash(oldHash)
	if err != nil {
		t.Fatalf("GetBySecretHash(old) after rotate: %v", err)
	}
	if found != nil {
		t.Error("old secret should NOT resolve after rotation")
	}

	newHash := HashSecret("new-secret")
	found, err = store.GetBySecretHash(newHash)
	if err != nil {
		t.Fatalf("GetBySecretHash(new): %v", err)
	}
	if found == nil {
		t.Fatal("new secret should resolve after rotation")
	}
	if found.ID != app.ID {
		t.Errorf("rotated app ID = %q, want %q", found.ID, app.ID)
	}
}

func TestAppStoreSetEnabled(t *testing.T) {
	store := newTestAppStore(t)

	app, err := store.Create("toggleapp", "toggle-secret", "/bin/toggle", "toggle.service", "o/tog", "tog-art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	hash := HashSecret("toggle-secret")
	found, err := store.GetBySecretHash(hash)
	if err != nil {
		t.Fatalf("GetBySecretHash: %v", err)
	}
	if found == nil {
		t.Fatal("enabled app should be found")
	}

	err = store.SetEnabled(app.ID, false)
	if err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}

	found, err = store.GetBySecretHash(hash)
	if err != nil {
		t.Fatalf("GetBySecretHash after disable: %v", err)
	}
	if found != nil {
		t.Error("disabled app should NOT be returned by GetBySecretHash")
	}

	err = store.SetEnabled(app.ID, true)
	if err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}

	found, err = store.GetBySecretHash(hash)
	if err != nil {
		t.Fatalf("GetBySecretHash after re-enable: %v", err)
	}
	if found == nil {
		t.Error("re-enabled app should be found")
	}
}

func TestBootstrapFromLegacyConfigIsIdempotent(t *testing.T) {
	store := newTestAppStore(t)

	legacy := &LegacyBootstrapConfig{
		Secret:       "bootstrap-secret",
		BinaryPath:   "/bin/legacy",
		ServiceName:  "legacy.service",
		GithubRepo:   "o/legacy",
		ArtifactName: "legacy-art",
	}

	app1, err := store.BootstrapIfEmpty(legacy)
	if err != nil {
		t.Fatalf("first BootstrapIfEmpty: %v", err)
	}
	if app1 == nil {
		t.Fatal("first bootstrap should return app")
	}

	app2, err := store.BootstrapIfEmpty(legacy)
	if err != nil {
		t.Fatalf("second BootstrapIfEmpty: %v", err)
	}
	if app2 == nil {
		t.Fatal("second bootstrap should return app")
	}

	if app1.ID != app2.ID {
		t.Errorf("bootstrap created two apps: %q vs %q", app1.ID, app2.ID)
	}

	apps, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(apps) != 1 {
		t.Errorf("expected 1 app after double bootstrap, got %d", len(apps))
	}
}

func TestBootstrapSkipsWhenAppsExist(t *testing.T) {
	store := newTestAppStore(t)

	_, err := store.Create("existing", "existing-secret", "/bin/existing", "existing.service", "o/existing", "existing-art")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	legacy := &LegacyBootstrapConfig{
		Secret:       "new-bootstrap-secret",
		BinaryPath:   "/bin/bootstrap",
		ServiceName:  "bootstrap.service",
		GithubRepo:   "o/bootstrap",
		ArtifactName: "bootstrap-art",
	}

	app, err := store.BootstrapIfEmpty(legacy)
	if err != nil {
		t.Fatalf("BootstrapIfEmpty: %v", err)
	}
	if app == nil {
		t.Fatal("expected non-nil app")
	}
	if app.Name != "existing" {
		t.Errorf("expected existing app, got name=%q", app.Name)
	}

	apps, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(apps) != 1 {
		t.Errorf("expected 1 app, got %d", len(apps))
	}
}

func newTestAppStoreWithJobs(t *testing.T) *AppStore {
	t.Helper()
	store := newTestAppStore(t)
	_, err := store.db.Exec(`CREATE TABLE IF NOT EXISTS deploy_jobs (
		id              TEXT PRIMARY KEY,
		seq             INTEGER NOT NULL,
		app_id          TEXT NOT NULL,
		tag             TEXT NOT NULL,
		status          TEXT NOT NULL DEFAULT 'pending',
		trigger_type    TEXT NOT NULL DEFAULT 'webhook',
		retry_of_job_id TEXT,
		error_msg       TEXT,
		created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create deploy_jobs table: %v", err)
	}
	return store
}

func TestAppStoreDeleteRemovesAppAndJobs(t *testing.T) {
	store := newTestAppStoreWithJobs(t)

	app, err := store.Create("testapp", "secret", "/bin/app", "app.service", "user/repo", "app-linux")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = store.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES ('job1', 1, ?, 'v1.0.0', 'succeeded', 'webhook')`,
		app.ID,
	)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}

	err = store.Delete(app.ID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = store.Get(app.ID)
	if err == nil {
		t.Error("Expected app to be gone after delete")
	}

	var count int
	store.db.QueryRow(`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ?`, app.ID).Scan(&count)
	if count != 0 {
		t.Errorf("Expected 0 jobs, got %d", count)
	}
}

func TestAppStoreDeleteBlockedByActiveJob(t *testing.T) {
	store := newTestAppStoreWithJobs(t)

	app, _ := store.Create("testapp", "secret", "/bin/app", "app.service", "user/repo", "app-linux")

	store.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES ('job1', 1, ?, 'v1.0.0', 'in_progress', 'webhook')`,
		app.ID,
	)

	err := store.Delete(app.ID)
	if err != ErrActiveDeployExists {
		t.Errorf("Expected ErrActiveDeployExists, got %v", err)
	}
}

func TestAppStoreListWithLastDeploy(t *testing.T) {
	store := newTestAppStoreWithJobs(t)

	app1, _ := store.Create("app1", "secret1", "/bin/app1", "app1.service", "user/repo1", "art1")
	app2, _ := store.Create("app2", "secret2", "/bin/app2", "app2.service", "user/repo2", "art2")

	store.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES ('job1', 1, ?, 'v2.0.0', 'succeeded', 'webhook')`,
		app1.ID,
	)

	apps, err := store.ListWithLastDeploy()
	if err != nil {
		t.Fatalf("ListWithLastDeploy failed: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("Expected 2 apps, got %d", len(apps))
	}

	var result1, result2 AppWithLastDeploy
	for _, a := range apps {
		if a.ID == app1.ID {
			result1 = a
		}
		if a.ID == app2.ID {
			result2 = a
		}
	}

	if result1.LastDeployTag != "v2.0.0" {
		t.Errorf("Expected app1 LastDeployTag=v2.0.0, got %q", result1.LastDeployTag)
	}
	if result1.LastDeployStatus != "succeeded" {
		t.Errorf("Expected app1 LastDeployStatus=succeeded, got %q", result1.LastDeployStatus)
	}
	if result2.LastDeployTag != "" {
		t.Errorf("Expected app2 LastDeployTag empty, got %q", result2.LastDeployTag)
	}
	_ = app2
}
