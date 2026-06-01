package admission

import (
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"
)

func newTestAdmission(t *testing.T, maxPending int) (*AdmissionService, *store.AppStore, *store.DeployQueue) {
	t.Helper()
	db := newTestDB(t)

	appStore, err := store.NewAppStore(db)
	if err != nil {
		t.Fatalf("store.NewAppStore: %v", err)
	}

	queue, err := store.NewDeployQueue(db, maxPending)
	if err != nil {
		t.Fatalf("store.NewDeployQueue: %v", err)
	}
	if err := queue.Migrate(); err != nil {
		t.Fatalf("queue.Migrate: %v", err)
	}

	svc := NewAdmissionService(appStore, queue)
	return svc, appStore, queue
}

func TestAdmissionResolvesAppByBearerToken(t *testing.T) {
	svc, store, _ := newTestAdmission(t, 10)

	secret := "my-app-secret"
	app, err := store.Create("test-app", secret, "/bin/test", "test.service", "owner/repo", "artifact")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	result := svc.Admit(secret, "v1.0.0")

	if result.Outcome != OutcomeQueued {
		t.Fatalf("expected outcome=%s, got %s", OutcomeQueued, result.Outcome)
	}
	if result.App == nil {
		t.Fatal("expected non-nil app")
	}
	if result.App.ID != app.ID {
		t.Errorf("expected app.ID=%s, got %s", app.ID, result.App.ID)
	}
	if result.Tag != "v1.0.0" {
		t.Errorf("expected tag=v1.0.0, got %s", result.Tag)
	}
}

func TestAdmissionRejectsUnknownToken(t *testing.T) {
	svc, _, _ := newTestAdmission(t, 10)

	result := svc.Admit("totally-unknown-token", "v1.0.0")

	if result.Outcome != OutcomeUnauthorized {
		t.Fatalf("expected outcome=%s, got %s", OutcomeUnauthorized, result.Outcome)
	}
	if result.App != nil {
		t.Errorf("expected nil app for unknown token, got %+v", result.App)
	}
}

func TestAdmissionRejectsDisabledApp(t *testing.T) {
	svc, store, _ := newTestAdmission(t, 10)

	secret := "disabled-app-secret"
	app, err := store.Create("disabled-app", secret, "/bin/disabled", "disabled.service", "owner/disabled", "artifact")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	if err := store.SetEnabled(app.ID, false); err != nil {
		t.Fatalf("disable app: %v", err)
	}

	result := svc.Admit(secret, "v1.0.0")

	if result.Outcome != OutcomeUnauthorized {
		t.Fatalf("expected outcome=%s, got %s", OutcomeUnauthorized, result.Outcome)
	}
	if result.App != nil {
		t.Errorf("expected nil app for disabled app, got %+v", result.App)
	}
}

func TestAdmissionRejectsBadRequestEmptyTag(t *testing.T) {
	svc, store, _ := newTestAdmission(t, 10)

	secret := "tag-test-secret"
	_, err := store.Create("tag-app", secret, "/bin/tag", "tag.service", "owner/tag", "artifact")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	result := svc.Admit(secret, "")

	if result.Outcome != OutcomeBadRequest {
		t.Fatalf("expected outcome=%s, got %s", OutcomeBadRequest, result.Outcome)
	}
	if result.App == nil {
		t.Error("expected app to be resolved even with bad tag")
	}
}

func TestAdmissionDuplicateWithinSameApp(t *testing.T) {
	svc, store, _ := newTestAdmission(t, 10)

	secret := "dup-secret"
	_, err := store.Create("dup-app", secret, "/bin/dup", "dup.service", "owner/dup", "artifact")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	first := svc.Admit(secret, "v2.0.0")
	if first.Outcome != OutcomeQueued {
		t.Fatalf("first admit: expected outcome=%s, got %s", OutcomeQueued, first.Outcome)
	}

	second := svc.Admit(secret, "v2.0.0")
	if second.Outcome != OutcomeDuplicate {
		t.Fatalf("second admit: expected outcome=%s, got %s", OutcomeDuplicate, second.Outcome)
	}
}

func TestAdmissionAllowsSameTagAcrossDifferentApps(t *testing.T) {
	svc, store, _ := newTestAdmission(t, 10)

	secretA := "app-a-secret"
	_, err := store.Create("app-a", secretA, "/bin/a", "a.service", "owner/a", "artifact-a")
	if err != nil {
		t.Fatalf("create app-a: %v", err)
	}

	secretB := "app-b-secret"
	_, err = store.Create("app-b", secretB, "/bin/b", "b.service", "owner/b", "artifact-b")
	if err != nil {
		t.Fatalf("create app-b: %v", err)
	}

	resultA := svc.Admit(secretA, "v3.0.0")
	if resultA.Outcome != OutcomeQueued {
		t.Fatalf("app-a: expected outcome=%s, got %s", OutcomeQueued, resultA.Outcome)
	}

	resultB := svc.Admit(secretB, "v3.0.0")
	if resultB.Outcome != OutcomeQueued {
		t.Fatalf("app-b: expected outcome=%s, got %s (same tag different app should be allowed)", OutcomeQueued, resultB.Outcome)
	}
}

func TestAdmissionRejectsQueueFull(t *testing.T) {
	svc, store, _ := newTestAdmission(t, 1)

	secret := "full-secret"
	_, err := store.Create("full-app", secret, "/bin/full", "full.service", "owner/full", "artifact")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	first := svc.Admit(secret, "v1.0.0")
	if first.Outcome != OutcomeQueued {
		t.Fatalf("first admit: expected outcome=%s, got %s", OutcomeQueued, first.Outcome)
	}

	second := svc.Admit(secret, "v2.0.0")
	if second.Outcome != OutcomeError {
		t.Fatalf("second admit: expected outcome=%s, got %s", OutcomeError, second.Outcome)
	}
	if second.Error == nil {
		t.Fatal("expected non-nil error for queue full")
	}
	if second.Error.Error() != "queue full" {
		t.Errorf("expected error message 'queue full', got %q", second.Error.Error())
	}
}
