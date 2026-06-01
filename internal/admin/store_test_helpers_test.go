package admin

import (
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"
)

func newTestAppStore(t *testing.T) *store.AppStore {
	t.Helper()
	appStore, err := store.NewAppStore(newTestDB(t))
	if err != nil {
		t.Fatalf("NewAppStore: %v", err)
	}
	return appStore
}

func newTestAppStoreWithJobs(t *testing.T) *store.AppStore {
	t.Helper()
	appStore := newTestAppStore(t)
	app, err := appStore.Create("test-app", "secret", "/opt/test-app", "test.service", "owner/repo", "artifact")
	if err != nil {
		t.Fatalf("Create app: %v", err)
	}
	queue, err := store.NewDeployQueue(appStore.DB(), 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if err := queue.Enqueue(app.ID, "v1.0.0"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return appStore
}
