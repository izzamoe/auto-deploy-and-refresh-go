package admin

import (
	"context"
	"database/sql"
	"html/template"
	"net/http"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/route"
)

func newTestHistoryAdminHandler(t *testing.T, appStore *store.AppStore, queue *store.DeployQueue) *HistoryAdminHandler {
	t.Helper()
	tmpls := make(map[string]*template.Template)
	tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/history.html")
	if err != nil {
		t.Fatalf("failed to parse history.html: %v", err)
	}
	tmpls["history.html"] = tmpl

	return NewHistoryAdminHandler(appStore, queue, tmpls, progress.NewProgressTracker())
}

func setupHistoryTest(t *testing.T) (*sql.DB, *store.AppStore, *store.DeployQueue, *HistoryAdminHandler) {
	db := newTestDB(t)
	appStore, err := store.NewAppStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	queue, err := store.NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("failed to create queue: %v", err)
	}
	handler := newTestHistoryAdminHandler(t, appStore, queue)
	return db, appStore, queue, handler
}

func TestAdminHistoryList(t *testing.T) {
	TestHertzAdminHistoryList(t)
}

func TestAdminHistoryListTerminalRowIgnoresStaleTrackerProgress(t *testing.T) {
	TestHertzAdminHistoryListTerminalRowIgnoresStaleTrackerProgress(t)
}

func TestAdminHistoryListShowsActivePhaseLabel(t *testing.T) {
	TestHertzAdminHistoryListShowsActivePhaseLabel(t)
}

func TestAdminHistoryListAdminUIReturnsFragment(t *testing.T) {
	TestHertzAdminHistoryListAdminUIReturnsFragment(t)
}

func TestAdminHistoryRetry(t *testing.T) {
	TestHertzAdminHistoryRetry(t)
}

func TestAdminHistoryRetryAdminUIReturnsUpdatedTableAndFlash(t *testing.T) {
	TestHertzAdminHistoryRetryAdminUIReturnsUpdatedTableAndFlash(t)
}

func TestAdminHistoryRetryAdminUIDuplicateReturnsUpdatedTableAndErrorFlash(t *testing.T) {
	TestHertzAdminHistoryRetryAdminUIDuplicateReturnsUpdatedTableAndErrorFlash(t)
}

func TestAdminHistoryRetryRejectsMismatchedJobApp(t *testing.T) {
	TestHertzAdminHistoryRetryRejectsMismatchedJobApp(t)
}

func TestAdminHistoryRetryAdminUIRejectsMismatchedJobApp(t *testing.T) {
	TestHertzAdminHistoryRetryAdminUIRejectsMismatchedJobApp(t)
}

func TestAdminHistoryRetryDisabledApp(t *testing.T) {
	TestHertzAdminHistoryRetryDisabledApp(t)
}

func TestAdminHistoryRetryDuplicatePending(t *testing.T) {
	TestHertzAdminHistoryRetryDuplicatePending(t)
}

func newTestHertzHistoryServer(t *testing.T, handler *HistoryAdminHandler) *route.Engine {
	t.Helper()
	h := server.New(server.WithHostPorts(":0"), server.WithDisablePrintRoute(true))
	RegisterAdminHistoryRoutesHertz(h, handler, func(ctx context.Context, c *app.RequestContext) { c.Next(ctx) })
	return h.Engine
}

func TestHertzAdminHistoryList(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, err := store.Create("Test store.App", "izzamoe/test", "test-bin", "abc", "/bin/false", "test.service")
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

	engine := newTestHertzHistoryServer(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/apps/"+app.ID+"/history", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode())
	}

	body := string(resp.Body())
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
	if !strings.Contains(body, `data-progress-bar`) {
		t.Errorf("expected data-progress-bar for in_progress row")
	}
	if !strings.Contains(body, `data-admin-ws-url="/admin/progress/ws"`) {
		t.Errorf("expected WebSocket progress hook, got %s", body)
	}
}

func TestHertzAdminHistoryListTerminalRowIgnoresStaleTrackerProgress(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, err := store.Create("Test store.App", "izzamoe/test-2", "test-bin-2", "abc-2", "/bin/false-2", "test-2.service")
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

	engine := newTestHertzHistoryServer(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/apps/"+app.ID+"/history", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if !strings.Contains(body, `data-status="succeeded"`) {
		t.Fatalf("expected terminal history row status, got %s", body)
	}
	if !strings.Contains(body, `data-progress-percent`) || !strings.Contains(body, `data-progress-speed`) {
		t.Fatalf("expected terminal history row to keep stable progress hooks, got %s", body)
	}
}

func TestHertzAdminHistoryListShowsActivePhaseLabel(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, err := store.Create("Installing store.App", "phase-secret", "/bin/phase", "phase.service", "owner/phase", "artifact-phase")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	if err := queue.Enqueue(app.ID, "v9.9.9"); err != nil {
		t.Fatalf("queue.Enqueue: %v", err)
	}
	jobID, _, _ := queue.DequeueNext(app.ID)

	handler.tracker.Start(app.ID, jobID, "v9.9.9")
	handler.tracker.Update(app.ID, 2048, 2048, 512)
	handler.tracker.SetPhase(app.ID, progress.ProgressStageInstalling)

	engine := newTestHertzHistoryServer(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/apps/"+app.ID+"/history", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if !strings.Contains(body, `data-progress-phase`) {
		t.Fatalf("expected history row phase marker, got %s", body)
	}
	if !strings.Contains(body, `Applying update`) {
		t.Fatalf("expected history row phase label, got %s", body)
	}
	if !strings.Contains(body, `status-installing`) {
		t.Fatalf("expected history row to use status-installing badge class, got %s", body)
	}
	if !strings.Contains(body, ">installing<") && !strings.Contains(body, "installing") {
		t.Fatalf("expected history row to show installing badge text, got %s", body)
	}
	if !strings.Contains(body, `data-progress-percent`) || !strings.Contains(body, `data-progress-speed`) {
		t.Fatalf("expected installing history row to keep stable download hooks, got %s", body)
	}
	if !strings.Contains(body, `data-progress-bytes`) {
		t.Fatalf("expected installing history row to keep stable byte hook, got %s", body)
	}
	if !strings.Contains(body, `Download complete. Restarting service and verifying health.`) {
		t.Fatalf("expected history row installing detail text, got %s", body)
	}
}

func TestHertzAdminHistoryListAdminUIReturnsFragment(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test store.App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")
	queue.Enqueue(app.ID, "v1.0.0")

	engine := newTestHertzHistoryServer(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/apps/"+app.ID+"/history", nil,
		ut.Header{Key: adminUIRequestHeader, Value: "true"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("expected AdminUI history fragment, got full document: %s", body)
	}
	if !strings.Contains(body, `id="history-content"`) {
		t.Fatalf("expected history content root, got %s", body)
	}
	if !strings.Contains(body, `id="history-table"`) {
		t.Fatalf("expected history table fragment, got %s", body)
	}
}

func TestHertzAdminHistoryRetry(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test store.App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")

	queue.Enqueue(app.ID, "v1.0.0")
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	engine := newTestHertzHistoryServer(t, handler)

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/retry/"+jobID, nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", resp.StatusCode())
	}
	loc := string(resp.Header.Peek("Location"))
	if !strings.Contains(loc, "flash=Retry+queued") {
		t.Errorf("expected success flash in location, got %s", loc)
	}

	count, _ := queue.PendingCount(app.ID)
	if count != 1 {
		t.Errorf("expected 1 pending job, got %d", count)
	}
}

func TestHertzAdminHistoryRetryAdminUIReturnsUpdatedTableAndFlash(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test store.App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")
	queue.Enqueue(app.ID, "v1.0.0")
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	engine := newTestHertzHistoryServer(t, handler)

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/retry/"+jobID, nil,
		ut.Header{Key: adminUIRequestHeader, Value: "true"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 for AdminUI retry, got %d", resp.StatusCode())
	}
	if string(resp.Header.Peek("Location")) != "" {
		t.Fatalf("expected no raw redirect Location header, got %q", string(resp.Header.Peek("Location")))
	}
	if got := string(resp.Header.Peek(adminUILocationHeader)); got != "" {
		t.Fatalf("expected in-place AdminUI update instead of location header, got %q", got)
	}
	body := string(resp.Body())
	if !strings.Contains(body, `id="flash"`) {
		t.Fatalf("expected flash in AdminUI retry response, got %s", body)
	}
	if !strings.Contains(body, `id="history-table-region"`) {
		t.Fatalf("expected history table region in AdminUI retry response, got %s", body)
	}
	if !strings.Contains(body, `Retry queued`) {
		t.Fatalf("expected retry success flash in AdminUI retry response, got %s", body)
	}
	if !strings.Contains(body, `data-status="pending"`) {
		t.Fatalf("expected pending retry row in AdminUI retry response, got %s", body)
	}
}

func TestHertzAdminHistoryRetryAdminUIDuplicateReturnsUpdatedTableAndErrorFlash(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test store.App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")
	queue.Enqueue(app.ID, "v1.0.0")
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, false, "failed", nil)
	queue.Enqueue(app.ID, "v1.0.0")

	engine := newTestHertzHistoryServer(t, handler)

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/retry/"+jobID, nil,
		ut.Header{Key: adminUIRequestHeader, Value: "true"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200 for AdminUI retry duplicate, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if !strings.Contains(body, `Deploy already pending for this tag`) {
		t.Fatalf("expected duplicate flash in AdminUI retry response, got %s", body)
	}
	if !strings.Contains(body, `flash-error`) {
		t.Fatalf("expected error flash class in AdminUI retry response, got %s", body)
	}
}

func TestHertzAdminHistoryRetryRejectsMismatchedJobApp(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	appA, _ := store.Create("store.App A", "secret-a", "/bin/app-a", "app-a.service", "izzamoe/app-a", "artifact-a")
	appB, _ := store.Create("store.App B", "secret-b", "/bin/app-b", "app-b.service", "izzamoe/app-b", "artifact-b")

	if err := queue.Enqueue(appB.ID, "v1.0.0"); err != nil {
		t.Fatalf("queue.Enqueue: %v", err)
	}
	jobID, _, _ := queue.DequeueNext(appB.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	engine := newTestHertzHistoryServer(t, handler)

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+appA.ID+"/retry/"+jobID, nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("expected 404 for mismatched app/job retry, got %d", resp.StatusCode())
	}
	count, _ := queue.PendingCount(appA.ID)
	if count != 0 {
		t.Fatalf("expected no retry job queued for mismatched app, got %d", count)
	}
}

func TestHertzAdminHistoryRetryAdminUIRejectsMismatchedJobApp(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	appA, _ := store.Create("store.App A", "secret-a", "/bin/app-a", "app-a.service", "izzamoe/app-a", "artifact-a")
	appB, _ := store.Create("store.App B", "secret-b", "/bin/app-b", "app-b.service", "izzamoe/app-b", "artifact-b")

	if err := queue.Enqueue(appB.ID, "v1.0.0"); err != nil {
		t.Fatalf("queue.Enqueue: %v", err)
	}
	jobID, _, _ := queue.DequeueNext(appB.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	engine := newTestHertzHistoryServer(t, handler)

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+appA.ID+"/retry/"+jobID, nil,
		ut.Header{Key: adminUIRequestHeader, Value: "true"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("expected 404 for AdminUI mismatched app/job retry, got %d", resp.StatusCode())
	}
	if body := string(resp.Body()); !strings.Contains(body, "Job not found") {
		t.Fatalf("expected job not found body for AdminUI mismatch, got %s", body)
	}
}

func TestHertzAdminHistoryRetryDisabledApp(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test store.App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")
	store.SetEnabled(app.ID, false)

	queue.Enqueue(app.ID, "v1.0.0")
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	engine := newTestHertzHistoryServer(t, handler)

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/retry/"+jobID, nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", resp.StatusCode())
	}
	loc := string(resp.Header.Peek("Location"))
	if !strings.Contains(loc, "flash=Cannot+retry+disabled+app") {
		t.Errorf("expected disabled error flash in location, got %s", loc)
	}
}

func TestHertzAdminHistoryRetryDuplicatePending(t *testing.T) {
	_, store, queue, handler := setupHistoryTest(t)

	app, _ := store.Create("Test store.App", "abc", "/bin/false", "test.service", "izzamoe/test", "test-bin")

	queue.Enqueue(app.ID, "v1.0.0")
	jobID, _, _ := queue.DequeueNext(app.ID)
	queue.MarkDone(jobID, false, "failed", nil)

	queue.Enqueue(app.ID, "v1.0.0")

	engine := newTestHertzHistoryServer(t, handler)

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/retry/"+jobID, nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", resp.StatusCode())
	}
	loc := string(resp.Header.Peek("Location"))
	if !strings.Contains(loc, "flash=Deploy+already+pending+for+this+tag") {
		t.Errorf("expected duplicate error flash in location, got %s", loc)
	}
}
