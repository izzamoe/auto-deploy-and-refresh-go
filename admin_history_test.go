package main

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestHistoryAdminHandler(t *testing.T, store *AppStore, queue *DeployQueue) *HistoryAdminHandler {
	t.Helper()
	tmpls := make(map[string]*template.Template)
	tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/history.html")
	if err != nil {
		t.Fatalf("failed to parse history.html: %v", err)
	}
	tmpls["history.html"] = tmpl

	return NewHistoryAdminHandler(store, queue, tmpls, NewProgressTracker())
}

func setupHistoryTest(t *testing.T) (*sql.DB, *AppStore, *DeployQueue, *HistoryAdminHandler) {
	db := newTestDB(t)
	store, err := NewAppStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	queue, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("failed to create queue: %v", err)
	}
	handler := newTestHistoryAdminHandler(t, store, queue)
	return db, store, queue, handler
}

func TestAdminHistoryList(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, err := store.Create("Test App", "izzamoe/test", "test-bin", "abc", "/bin/false", "test.service")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	if err := queue.Enqueue(app.ID, "v1.0.0"); err != nil {
		t.Fatalf("queue.Enqueue: %v", err)
	}
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, true, "", nil)

	if err := queue.Enqueue(app.ID, "v1.0.1"); err != nil {
		t.Fatalf("queue.Enqueue: %v", err)
	}
	jobID2, _, _ := queue.DequeueNext(app.ID)

	handler.tracker.Start(app.ID, jobID2, "v1.0.1")
	handler.tracker.Update(app.ID, 50, 100, 10.5)

	mux := http.NewServeMux()
	RegisterAdminHistoryRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps/"+app.ID+"/history", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "id=\"history-table\"") {
		t.Errorf("expected table with id history-table")
	}
	if !strings.Contains(body, "v1.0.0") {
		t.Errorf("expected tag v1.0.0 in output")
	}
	if !strings.Contains(body, "v1.0.1") {
		t.Errorf("expected tag v1.0.1 in output")
	}
	if !strings.Contains(body, `data-job-id="`+jobID2+`"`) {
		t.Errorf("expected data-job-id attribute for row")
	}
	if !strings.Contains(body, `data-progress-percent`) {
		t.Errorf("expected data-progress-percent for in_progress row")
	}
	if !strings.Contains(body, `data-progress-speed`) {
		t.Errorf("expected data-progress-speed for in_progress row")
	}
}

func TestAdminHistoryRetry(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")

	queue.Enqueue(app.ID, "v1.0.0")
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	mux := http.NewServeMux()
	RegisterAdminHistoryRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/retry/"+jobID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "flash=Retry+queued") {
		t.Errorf("expected success flash in location, got %s", loc)
	}

	count, _ := queue.PendingCount(app.ID)
	if count != 1 {
		t.Errorf("expected 1 pending job, got %d", count)
	}
}

func TestAdminHistoryRetryDisabledApp(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")
	store.SetEnabled(app.ID, false)

	queue.Enqueue(app.ID, "v1.0.0")
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	mux := http.NewServeMux()
	RegisterAdminHistoryRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/retry/"+jobID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "flash=Cannot+retry+disabled+app") {
		t.Errorf("expected disabled error flash in location, got %s", loc)
	}
}

func TestAdminHistoryRetryDuplicatePending(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")

	queue.Enqueue(app.ID, "v1.0.0")
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	queue.Enqueue(app.ID, "v1.0.0")

	mux := http.NewServeMux()
	RegisterAdminHistoryRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/retry/"+jobID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "flash=Deploy+already+pending+for+this+tag") {
		t.Errorf("expected duplicate error flash in location, got %s", loc)
	}
}
