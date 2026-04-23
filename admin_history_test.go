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
	if !strings.Contains(body, `id="history-content"`) {
		t.Errorf("expected history content root")
	}
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
	if !strings.Contains(body, `id="history-progress-subscription"`) {
		t.Errorf("expected history SSE subscription root")
	}
	if !strings.Contains(body, `sse-connect="/admin/progress/stream?`) {
		t.Errorf("expected HTMX SSE stream connection, got %s", body)
	}
	if strings.Contains(body, `new EventSource(`) {
		t.Errorf("expected history template to avoid manual EventSource JS, got %s", body)
	}
}

func TestAdminHistoryListTerminalRowIgnoresStaleTrackerProgress(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, err := store.Create("Test App", "izzamoe/test-2", "test-bin-2", "abc-2", "/bin/false-2", "test-2.service")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	if err := queue.Enqueue(app.ID, "v2.0.0"); err != nil {
		t.Fatalf("queue.Enqueue: %v", err)
	}
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, true, "", nil)

	handler.tracker.Start(app.ID, jobID, "v2.0.0")
	handler.tracker.Update(app.ID, 50, 100, 10.5)
	handler.tracker.Finish(app.ID)

	mux := http.NewServeMux()
	RegisterAdminHistoryRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps/"+app.ID+"/history", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `data-status="succeeded"`) {
		t.Fatalf("expected terminal history row status, got %s", body)
	}
	if strings.Contains(body, `data-progress-percent`) || strings.Contains(body, `data-progress-speed`) {
		t.Fatalf("expected terminal history row to omit active progress markers, got %s", body)
	}
	if strings.Contains(body, `sse-connect="/admin/progress/stream?`) {
		t.Fatalf("expected no history SSE subscription once all jobs are terminal, got %s", body)
	}
}

func TestAdminHistoryListShowsActivePhaseLabel(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, err := store.Create("Installing App", "phase-secret", "/bin/phase", "phase.service", "owner/phase", "artifact-phase")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	if err := queue.Enqueue(app.ID, "v9.9.9"); err != nil {
		t.Fatalf("queue.Enqueue: %v", err)
	}
	jobID, _, _ := queue.DequeueNext(app.ID)

	handler.tracker.Start(app.ID, jobID, "v9.9.9")
	handler.tracker.Update(app.ID, 2048, 2048, 512)
	handler.tracker.SetPhase(app.ID, PhaseInstalling)

	mux := http.NewServeMux()
	RegisterAdminHistoryRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps/"+app.ID+"/history", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `data-progress-phase`) {
		t.Fatalf("expected history row phase marker, got %s", body)
	}
	if !strings.Contains(body, `Applying update`) {
		t.Fatalf("expected history row phase label, got %s", body)
	}
	if strings.Contains(body, `data-progress-percent`) || strings.Contains(body, `data-progress-speed`) {
		t.Fatalf("expected installing history row to hide stale download stats, got %s", body)
	}
	if strings.Contains(body, `data-progress-bytes`) {
		t.Fatalf("expected installing history row to hide download byte counters, got %s", body)
	}
	if !strings.Contains(body, `Download complete. Restarting service and verifying health.`) {
		t.Fatalf("expected history row installing detail text, got %s", body)
	}
}

func TestAdminHistoryListHTMXReturnsFragment(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")
	queue.Enqueue(app.ID, "v1.0.0")

	mux := http.NewServeMux()
	RegisterAdminHistoryRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps/"+app.ID+"/history", nil)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("expected HTMX history fragment, got full document: %s", body)
	}
	if !strings.Contains(body, `id="history-content"`) {
		t.Fatalf("expected history content root, got %s", body)
	}
	if !strings.Contains(body, `id="history-table"`) {
		t.Fatalf("expected history table fragment, got %s", body)
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

func TestAdminHistoryRetryHTMXReturnsUpdatedTableAndFlash(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")
	queue.Enqueue(app.ID, "v1.0.0")
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	mux := http.NewServeMux()
	RegisterAdminHistoryRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/retry/"+jobID, nil)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for HTMX retry, got %d", w.Code)
	}
	if w.Header().Get("Location") != "" {
		t.Fatalf("expected no raw redirect Location header, got %q", w.Header().Get("Location"))
	}
	if got := w.Header().Get("HX-Location"); got != "" {
		t.Fatalf("expected in-place HTMX update instead of HX-Location, got %q", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="flash"`) {
		t.Fatalf("expected OOB flash in HTMX retry response, got %s", body)
	}
	if !strings.Contains(body, `id="history-table-region"`) {
		t.Fatalf("expected history table region in HTMX retry response, got %s", body)
	}
	if !strings.Contains(body, `Retry queued`) {
		t.Fatalf("expected retry success flash in HTMX retry response, got %s", body)
	}
	if !strings.Contains(body, `data-status="pending"`) {
		t.Fatalf("expected pending retry row in HTMX retry response, got %s", body)
	}
}

func TestAdminHistoryRetryHTMXDuplicateReturnsUpdatedTableAndErrorFlash(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")
	queue.Enqueue(app.ID, "v1.0.0")
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, false, "failed", nil)
	queue.Enqueue(app.ID, "v1.0.0")

	mux := http.NewServeMux()
	RegisterAdminHistoryRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/retry/"+jobID, nil)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for HTMX retry duplicate, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `Deploy already pending for this tag`) {
		t.Fatalf("expected duplicate flash in HTMX retry response, got %s", body)
	}
	if !strings.Contains(body, `flash-error`) {
		t.Fatalf("expected error flash class in HTMX retry response, got %s", body)
	}
}

func TestAdminHistoryRetryRejectsMismatchedJobApp(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	appA, _ := store.Create("App A", "secret-a", "/bin/app-a", "app-a.service", "izzamoe/app-a", "artifact-a")
	appB, _ := store.Create("App B", "secret-b", "/bin/app-b", "app-b.service", "izzamoe/app-b", "artifact-b")

	if err := queue.Enqueue(appB.ID, "v1.0.0"); err != nil {
		t.Fatalf("queue.Enqueue: %v", err)
	}
	jobID, _, _ := queue.DequeueNext(appB.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	mux := http.NewServeMux()
	RegisterAdminHistoryRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("POST", "/admin/apps/"+appA.ID+"/retry/"+jobID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for mismatched app/job retry, got %d", w.Code)
	}
	count, _ := queue.PendingCount(appA.ID)
	if count != 0 {
		t.Fatalf("expected no retry job queued for mismatched app, got %d", count)
	}
}

func TestAdminHistoryRetryHTMXRejectsMismatchedJobApp(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	appA, _ := store.Create("App A", "secret-a", "/bin/app-a", "app-a.service", "izzamoe/app-a", "artifact-a")
	appB, _ := store.Create("App B", "secret-b", "/bin/app-b", "app-b.service", "izzamoe/app-b", "artifact-b")

	if err := queue.Enqueue(appB.ID, "v1.0.0"); err != nil {
		t.Fatalf("queue.Enqueue: %v", err)
	}
	jobID, _, _ := queue.DequeueNext(appB.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	mux := http.NewServeMux()
	RegisterAdminHistoryRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("POST", "/admin/apps/"+appA.ID+"/retry/"+jobID, nil)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for HTMX mismatched app/job retry, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "Job not found") {
		t.Fatalf("expected job not found body for HTMX mismatch, got %s", body)
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
