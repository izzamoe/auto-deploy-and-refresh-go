package admin

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/route"
)

func newTestAppAdminHandler(t *testing.T, appStore *store.AppStore) *AppAdminHandler {
	t.Helper()
	tmpls := make(map[string]*template.Template)
	pages := []string{"apps_list.html", "app_form.html"}

	for _, page := range pages {
		tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/"+page)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", page, err)
		}
		tmpls[page] = tmpl
	}

	queue, err := store.NewDeployQueue(appStore.DB(), 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if err := queue.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	return NewAppAdminHandler(appStore, queue, tmpls, progress.NewProgressTracker())
}

func TestAdminAppsListAdminUIReturnsFragment(t *testing.T) {
	TestHertzAdminAppsListAdminUIReturnsFragment(t)
}

func TestAdminNewAppFormAdminUIReturnsFragment(t *testing.T) {
	TestHertzAdminNewAppFormAdminUIReturnsFragment(t)
}

func TestAdminEditAppFormAdminUIReturnsFragment(t *testing.T) {
	TestHertzAdminEditAppFormAdminUIReturnsFragment(t)
}

func TestAdminAppsDelete(t *testing.T) {
	TestHertzAdminAppsDelete(t)
}

func TestAdminAppsDeleteBlockedByActiveJob(t *testing.T) {
	TestHertzAdminAppsDeleteBlockedByActiveJob(t)
}

func TestAdminAppsCreate(t *testing.T) {
	TestHertzAdminAppsCreate(t)
}

func TestAdminAppsCreateAdminUIUsesAdminLocation(t *testing.T) {
	TestHertzAdminAppsCreateAdminUIUsesAdminLocation(t)
}

func TestAdminAppsCreateAdminUIValidationErrorsReturnInlineFormFragment(t *testing.T) {
	TestHertzAdminAppsCreateAdminUIValidationErrorsReturnInlineFormFragment(t)
}

func TestAdminAppsToggleAdminUIReturnsFragment(t *testing.T) {
	TestHertzAdminAppsToggleAdminUIReturnsFragment(t)
}

func TestAdminAppsDeleteAdminUIReturnsFragment(t *testing.T) {
	TestHertzAdminAppsDeleteAdminUIReturnsFragment(t)
}

func TestAdminAppsEdit(t *testing.T) {
	TestHertzAdminAppsEdit(t)
}

func TestAdminAppsEnableDisable(t *testing.T) {
	TestHertzAdminAppsEnableDisable(t)
}

func TestAdminAppsEditLeavesSecretUnchangedWhenBlank(t *testing.T) {
	TestHertzAdminAppsEditLeavesSecretUnchangedWhenBlank(t)
}

func TestAdminAppsUpdateAdminUIValidationErrorsReturnInlineFormFragment(t *testing.T) {
	TestHertzAdminAppsUpdateAdminUIValidationErrorsReturnInlineFormFragment(t)
}

func TestAdminAppsUpdateAdminUIUsesAdminLocation(t *testing.T) {
	TestHertzAdminAppsUpdateAdminUIUsesAdminLocation(t)
}

func TestAdminAppsCreateValidationErrorsPreserveFormValues(t *testing.T) {
	TestHertzAdminAppsCreateValidationErrorsPreserveFormValues(t)
}

func TestAdminAppsManualDeployAdminUIReturnsFragment(t *testing.T) {
	TestHertzAdminAppsManualDeployAdminUIReturnsFragment(t)
}

func TestAdminAppsManualDeploy(t *testing.T) {
	TestHertzAdminAppsManualDeploy(t)
}

func TestAdminAppsManualDeployDisabledApp(t *testing.T) {
	TestHertzAdminAppsManualDeployDisabledApp(t)
}

func TestAdminAppsManualDeployEmptyTag(t *testing.T) {
	TestHertzAdminAppsManualDeployEmptyTag(t)
}

func TestAdminAppsList(t *testing.T) {
	TestHertzAdminAppsList(t)
}

func TestAdminAppsListTerminalJobClearsActiveMarker(t *testing.T) {
	TestHertzAdminAppsListTerminalJobClearsActiveMarker(t)
}

func TestAdminAppsListShowsActivePhaseLabel(t *testing.T) {
	TestHertzAdminAppsListShowsActivePhaseLabel(t)
}

func newTestHertzAppServer(t *testing.T, handler *AppAdminHandler) *route.Engine {
	t.Helper()
	h := server.New(server.WithHostPorts(":0"), server.WithDisablePrintRoute(true))
	RegisterAdminAppRoutesHertz(h, handler, func(ctx context.Context, c *app.RequestContext) { c.Next(ctx) })
	return h.Engine
}

func TestHertzAdminAppsListAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	_, _ = store.Create("Test store.App", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/apps", nil, ut.Header{Key: adminUIRequestHeader, Value: "true"})
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("Expected AdminUI fragment without full document, got %s", body)
	}
	if !strings.Contains(body, `id="apps-table"`) {
		t.Fatalf("Expected AdminUI apps fragment, got %s", body)
	}
}

func TestHertzAdminNewAppFormAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/apps/new", nil, ut.Header{Key: adminUIRequestHeader, Value: "true"})
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("Expected AdminUI fragment without full document, got %s", body)
	}
	if !strings.Contains(body, `id="app-form"`) {
		t.Fatalf("Expected AdminUI app form fragment, got %s", body)
	}
}

func TestHertzAdminEditAppFormAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/apps/"+app.ID+"/edit", nil, ut.Header{Key: adminUIRequestHeader, Value: "true"})
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("Expected AdminUI fragment without full document, got %s", body)
	}
	if !strings.Contains(body, `id="app-form"`) {
		t.Fatalf("Expected AdminUI app edit fragment, got %s", body)
	}
	if !strings.Contains(body, `data-testid="admin-flash"`) {
		t.Fatalf("Expected AdminUI edit fragment to retain flash container, got %s", body)
	}
	if strings.Contains(body, `data-admin-swap="outerHTML"`) {
		t.Fatalf("Expected AdminUI GET fragment flash container to render in place, got %s", body)
	}
}

func TestHertzAdminAppsDelete(t *testing.T) {
	store := newTestAppStoreWithJobs(t)
	app, _ := store.Create("del-app", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/delete", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusSeeOther {
		t.Errorf("Expected 303, got %d. Body: %s", resp.StatusCode(), string(resp.Body()))
	}

	_, err := store.Get(app.ID)
	if err == nil {
		t.Error("Expected app to be deleted")
	}
}

func TestHertzAdminAppsDeleteBlockedByActiveJob(t *testing.T) {
	store := newTestAppStoreWithJobs(t)
	app, _ := store.Create("active-app", "sec2", "/bin2", "svc2", "repo2", "art2")

	store.DB().Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, download_bytes, download_duration_ms, download_speed_bps) VALUES ('activejob', 1, ?, 'v1.0.0', 'in_progress', 'webhook', 0, 0, 0)`,
		app.ID,
	)

	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/delete", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusSeeOther {
		t.Errorf("Expected 303 redirect, got %d", resp.StatusCode())
	}
	loc := string(resp.Header.Peek("Location"))
	if !strings.Contains(loc, "flash_error=1") {
		t.Errorf("Expected flash_error=1 in redirect, got %q", loc)
	}

	_, err := store.Get(app.ID)
	if err != nil {
		t.Error("store.App should still exist after blocked delete")
	}
}

func TestHertzAdminAppsCreate(t *testing.T) {
	store := newTestAppStore(t)
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	form := url.Values{}
	form.Add("name", "test-app")
	form.Add("binary_path", "/opt/test-app")
	form.Add("service_name", "test-app.service")
	form.Add("github_repo", "test/repo")
	form.Add("artifact_name", "test-artifact")
	form.Add("webhook_secret", "secret123")
	bodyStr := form.Encode()

	w := ut.PerformRequest(engine, "POST", "/admin/apps/create",
		&ut.Body{Body: strings.NewReader(bodyStr), Len: len(bodyStr)},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusSeeOther {
		t.Errorf("Expected 303 Redirect, got %d", resp.StatusCode())
	}

	apps, err := store.List()
	if err != nil {
		t.Fatalf("failed to list apps: %v", err)
	}
	if len(apps) != 1 {
		t.Errorf("Expected 1 app, got %d", len(apps))
	}
}

func TestHertzAdminAppsCreateAdminUIUsesAdminLocation(t *testing.T) {
	store := newTestAppStore(t)
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	form := url.Values{}
	form.Add("name", "test-app")
	form.Add("binary_path", "/opt/test-app")
	form.Add("service_name", "test-app.service")
	form.Add("github_repo", "test/repo")
	form.Add("artifact_name", "test-artifact")
	form.Add("webhook_secret", "secret123")
	bodyStr := form.Encode()

	w := ut.PerformRequest(engine, "POST", "/admin/apps/create",
		&ut.Body{Body: strings.NewReader(bodyStr), Len: len(bodyStr)},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"},
		ut.Header{Key: adminUIRequestHeader, Value: "true"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("Expected 200 OK for AdminUI create, got %d", resp.StatusCode())
	}
	if string(resp.Header.Peek("Location")) != "" {
		t.Fatalf("Expected no raw redirect Location header, got %q", string(resp.Header.Peek("Location")))
	}
	adminLocation := string(resp.Header.Peek(adminUILocationHeader))
	if !strings.Contains(adminLocation, "/admin/apps?flash=store.App+created+successfully") {
		t.Fatalf("Expected AdminUI success navigation, got %q", adminLocation)
	}
}

func TestHertzAdminAppsCreateAdminUIValidationErrorsReturnInlineFormFragment(t *testing.T) {
	store := newTestAppStore(t)
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	form := url.Values{}
	form.Add("name", "Preserved Name")
	bodyStr := form.Encode()

	w := ut.PerformRequest(engine, "POST", "/admin/apps/create",
		&ut.Body{Body: strings.NewReader(bodyStr), Len: len(bodyStr)},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"},
		ut.Header{Key: adminUIRequestHeader, Value: "true"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusBadRequest {
		t.Fatalf("Expected 400 Bad Request, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if strings.Contains(body, `<div class="page-header">`) {
		t.Fatalf("Expected inline form fragment without page header, got %s", body)
	}
	if !strings.Contains(body, `id="app-form"`) {
		t.Fatalf("Expected inline app form fragment, got %s", body)
	}
	if !strings.Contains(body, `id="form-errors"`) {
		t.Fatalf("Expected inline form errors, got %s", body)
	}
	if !strings.Contains(body, `value="Preserved Name"`) {
		t.Fatalf("Expected preserved name value, got %s", body)
	}
	if !strings.Contains(body, `data-admin-target="#app-form"`) {
		t.Fatalf("Expected AdminUI form retargeting to stay on #app-form, got %s", body)
	}
	if got := string(resp.Header.Peek(adminUILocationHeader)); got != "" {
		t.Fatalf("Expected no AdminUI location on validation error, got %q", got)
	}
}

func TestHertzAdminAppsToggleAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/disable", nil,
		ut.Header{Key: adminUIRequestHeader, Value: "true"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("Expected 200 OK for AdminUI toggle, got %d", resp.StatusCode())
	}
	if string(resp.Header.Peek("Location")) != "" {
		t.Fatalf("Expected no raw redirect Location header, got %q", string(resp.Header.Peek("Location")))
	}
	if got := string(resp.Header.Peek(adminUILocationHeader)); got != "" {
		t.Fatalf("Expected no AdminUI location for in-place toggle, got %q", got)
	}

	body := string(resp.Body())
	if !strings.Contains(body, `id="apps-table"`) {
		t.Fatalf("Expected AdminUI apps fragment, got %s", body)
	}
	if !strings.Contains(body, `store.App disabled successfully`) {
		t.Fatalf("Expected flash message in body, got %s", body)
	}
}

func TestHertzAdminAppsDeleteAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/delete", nil,
		ut.Header{Key: adminUIRequestHeader, Value: "true"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("Expected 200 OK for AdminUI delete, got %d", resp.StatusCode())
	}
	if got := string(resp.Header.Peek(adminUILocationHeader)); got != "" {
		t.Fatalf("Expected no AdminUI location for in-place delete, got %q", got)
	}
	body := string(resp.Body())
	if !strings.Contains(body, `id="apps-table"`) {
		t.Fatalf("Expected AdminUI apps fragment, got %s", body)
	}
	if !strings.Contains(body, `store.App deleted successfully`) {
		t.Fatalf("Expected flash message in body, got %s", body)
	}
}

func TestHertzAdminAppsEdit(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/apps/"+app.ID+"/edit", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", resp.StatusCode())
	}
	if !strings.Contains(string(resp.Body()), `id="app-form"`) {
		t.Errorf("Expected body to contain #app-form, got %s", string(resp.Body()))
	}
}

func TestHertzAdminAppsEnableDisable(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	// Test disable
	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/disable", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusSeeOther {
		t.Errorf("Expected 303 Redirect, got %d", resp.StatusCode())
	}

	updated, _ := store.Get(app.ID)
	if updated.Enabled {
		t.Errorf("Expected app to be disabled")
	}

	// Test enable
	w2 := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/enable", nil)
	resp2 := w2.Result()

	if resp2.StatusCode() != http.StatusSeeOther {
		t.Errorf("Expected 303 Redirect, got %d", resp2.StatusCode())
	}

	updated2, _ := store.Get(app.ID)
	if !updated2.Enabled {
		t.Errorf("Expected app to be enabled")
	}
}

func TestHertzAdminAppsEditLeavesSecretUnchangedWhenBlank(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "original-secret", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	form := url.Values{}
	form.Add("name", "Updated Test")
	form.Add("binary_path", "/bin")
	form.Add("service_name", "svc")
	form.Add("github_repo", "repo")
	form.Add("artifact_name", "art")
	form.Add("webhook_secret", "")
	bodyStr := form.Encode()

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/update",
		&ut.Body{Body: strings.NewReader(bodyStr), Len: len(bodyStr)},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusSeeOther {
		t.Errorf("Expected 303 Redirect, got %d. Body: %s", resp.StatusCode(), string(resp.Body()))
	}

	updated, _ := store.Get(app.ID)
	if updated.WebhookSecretHash != app.WebhookSecretHash {
		t.Errorf("Expected secret hash to be unchanged")
	}
	if updated.Name != "Updated Test" {
		t.Errorf("Expected name to be updated")
	}
}

func TestHertzAdminAppsUpdateAdminUIValidationErrorsReturnInlineFormFragment(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Original", "secret", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	form := url.Values{}
	form.Add("name", "Updated Name")
	form.Add("binary_path", "")
	form.Add("service_name", "svc")
	form.Add("github_repo", "repo")
	form.Add("artifact_name", "art")
	bodyStr := form.Encode()

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/update",
		&ut.Body{Body: strings.NewReader(bodyStr), Len: len(bodyStr)},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"},
		ut.Header{Key: adminUIRequestHeader, Value: "true"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusBadRequest {
		t.Fatalf("Expected 400 Bad Request, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if strings.Contains(body, `<div class="page-header">`) {
		t.Fatalf("Expected inline edit form fragment without page header, got %s", body)
	}
	if !strings.Contains(body, `action="/admin/apps/`+app.ID+`/update"`) {
		t.Fatalf("Expected update action in returned form, got %s", body)
	}
	if !strings.Contains(body, `value="Updated Name"`) {
		t.Fatalf("Expected preserved updated name value, got %s", body)
	}
	if !strings.Contains(body, "Binary Path is required") {
		t.Fatalf("Expected binary path validation message, got %s", body)
	}
	if got := string(resp.Header.Peek(adminUILocationHeader)); got != "" {
		t.Fatalf("Expected no AdminUI location on validation error, got %q", got)
	}
}

func TestHertzAdminAppsUpdateAdminUIUsesAdminLocation(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "secret", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	form := url.Values{}
	form.Add("name", "Updated Test")
	form.Add("binary_path", "/opt/updated")
	form.Add("service_name", "updated.service")
	form.Add("github_repo", "updated/repo")
	form.Add("artifact_name", "updated-artifact")
	form.Add("webhook_secret", "")
	bodyStr := form.Encode()

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/update",
		&ut.Body{Body: strings.NewReader(bodyStr), Len: len(bodyStr)},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"},
		ut.Header{Key: adminUIRequestHeader, Value: "true"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("Expected 200 OK for AdminUI update, got %d", resp.StatusCode())
	}
	if string(resp.Header.Peek("Location")) != "" {
		t.Fatalf("Expected no raw redirect Location header, got %q", string(resp.Header.Peek("Location")))
	}
	if got := string(resp.Header.Peek(adminUILocationHeader)); !strings.Contains(got, "/admin/apps?flash=store.App+updated+successfully") {
		t.Fatalf("Expected AdminUI location for update, got %q", got)
	}

	updated, err := store.Get(app.ID)
	if err != nil {
		t.Fatalf("failed to fetch updated app: %v", err)
	}
	if updated.Name != "Updated Test" {
		t.Fatalf("Expected name to be updated, got %q", updated.Name)
	}
}

func TestHertzAdminAppsCreateValidationErrorsPreserveFormValues(t *testing.T) {
	store := newTestAppStore(t)
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	form := url.Values{}
	form.Add("name", "Preserved Name")
	bodyStr := form.Encode()

	w := ut.PerformRequest(engine, "POST", "/admin/apps/create",
		&ut.Body{Body: strings.NewReader(bodyStr), Len: len(bodyStr)},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Errorf("Expected 200 OK validation form, got %d", resp.StatusCode())
	}

	body := string(resp.Body())
	if !strings.Contains(body, `id="form-errors"`) {
		t.Errorf("Expected body to contain #form-errors")
	}
	if !strings.Contains(body, `value="Preserved Name"`) {
		t.Errorf("Expected body to contain preserved name value")
	}
}

func TestHertzAdminAppsManualDeployAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Deploy", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	form := url.Values{}
	form.Add("tag", "v1.2.3")
	form.Add("source", "list")
	bodyStr := form.Encode()

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/deploy",
		&ut.Body{Body: strings.NewReader(bodyStr), Len: len(bodyStr)},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"},
		ut.Header{Key: adminUIRequestHeader, Value: "true"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("Expected 200 OK for AdminUI deploy, got %d", resp.StatusCode())
	}
	if got := string(resp.Header.Peek(adminUILocationHeader)); got != "" {
		t.Fatalf("Expected no AdminUI location for in-place list deploy, got %q", got)
	}
	body := string(resp.Body())
	if !strings.Contains(body, `id="apps-table"`) {
		t.Fatalf("Expected AdminUI apps fragment, got %s", body)
	}
	if !strings.Contains(body, `Manual deploy queued`) {
		t.Fatalf("Expected flash message in body, got %s", body)
	}
}

func TestHertzAdminAppsManualDeploy(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("deploy-app", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	form := url.Values{}
	form.Add("tag", "v1.0.0")
	bodyStr := form.Encode()

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/deploy",
		&ut.Body{Body: strings.NewReader(bodyStr), Len: len(bodyStr)},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusSeeOther {
		t.Errorf("Expected 303, got %d. Body: %s", resp.StatusCode(), string(resp.Body()))
	}

	loc := string(resp.Header.Peek("Location"))
	if !strings.Contains(loc, "/history") {
		t.Errorf("Expected redirect to history, got %q", loc)
	}
}

func TestHertzAdminAppsManualDeployDisabledApp(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("dis-app", "sec3", "/bin3", "svc3", "repo3", "art3")
	store.SetEnabled(app.ID, false)
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	form := url.Values{}
	form.Add("tag", "v1.0.0")
	bodyStr := form.Encode()

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/deploy",
		&ut.Body{Body: strings.NewReader(bodyStr), Len: len(bodyStr)},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusSeeOther {
		t.Errorf("Expected 303, got %d", resp.StatusCode())
	}
	loc := string(resp.Header.Peek("Location"))
	if !strings.Contains(loc, "flash_error=1") {
		t.Errorf("Expected flash_error in redirect, got %q", loc)
	}
}

func TestHertzAdminAppsManualDeployEmptyTag(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("tag-app", "sec4", "/bin4", "svc4", "repo4", "art4")
	handler := newTestAppAdminHandler(t, store)
	engine := newTestHertzAppServer(t, handler)

	form := url.Values{}
	form.Add("tag", "")
	bodyStr := form.Encode()

	w := ut.PerformRequest(engine, "POST", "/admin/apps/"+app.ID+"/deploy",
		&ut.Body{Body: strings.NewReader(bodyStr), Len: len(bodyStr)},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"},
	)
	resp := w.Result()

	if resp.StatusCode() != http.StatusSeeOther {
		t.Errorf("Expected 303, got %d", resp.StatusCode())
	}
	loc := string(resp.Header.Peek("Location"))
	if !strings.Contains(loc, "flash_error=1") {
		t.Errorf("Expected flash_error in redirect for empty tag, got %q", loc)
	}
}

func TestHertzAdminAppsList(t *testing.T) {
	store := newTestAppStoreWithJobs(t)
	app, _ := store.Create("Active Deploy store.App", "sec-test", "/bin", "svc", "repo", "art")

	store.DB().Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, download_bytes, download_duration_ms, download_speed_bps) VALUES ('job-123', 1, ?, 'v1.0.0', 'in_progress', 'webhook', 0, 0, 0)`,
		app.ID,
	)

	handler := newTestAppAdminHandler(t, store)
	handler.tracker.Start(app.ID, "job-123", "v1.0.0")
	handler.tracker.Update(app.ID, 450, 1000, 2500)

	engine := newTestHertzAppServer(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/apps", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", resp.StatusCode())
	}
	body := string(resp.Body())

	if !strings.Contains(body, `data-progress-job="job-123"`) {
		t.Errorf("Expected body to contain data-progress-job, got:\n%s", body)
	}
	if !strings.Contains(body, `data-app-id="`+app.ID+`"`) {
		t.Errorf("Expected body to contain data-app-id")
	}
	if !strings.Contains(body, `data-progress-percent`) {
		t.Errorf("Expected body to contain data-progress-percent")
	}
	if !strings.Contains(body, `data-progress-speed`) {
		t.Errorf("Expected body to contain data-progress-speed")
	}
	if !strings.Contains(body, `data-progress-bar`) {
		t.Errorf("Expected body to contain data-progress-bar")
	}
	if !strings.Contains(body, `data-admin-ws-url="/admin/progress/ws"`) {
		t.Errorf("Expected body to contain WebSocket progress hook, got:\n%s", body)
	}
}

func TestHertzAdminAppsListTerminalJobClearsActiveMarker(t *testing.T) {
	store := newTestAppStoreWithJobs(t)
	app, _ := store.Create("Settled Deploy store.App", "sec-test-2", "/bin2", "svc2", "repo2", "art2")

	store.DB().Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, download_bytes, download_duration_ms, download_speed_bps) VALUES ('job-999', 1, ?, 'v2.0.0', 'succeeded', 'webhook', 4096, 1000, 512)`,
		app.ID,
	)

	handler := newTestAppAdminHandler(t, store)
	handler.tracker.Start(app.ID, "job-999", "v2.0.0")
	handler.tracker.Update(app.ID, 2048, 4096, 256)
	handler.tracker.Finish(app.ID)

	engine := newTestHertzAppServer(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/apps", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if strings.Contains(body, `data-progress-job="job-999"`) {
		t.Fatalf("Expected terminal app card to clear stale progress marker, got:\n%s", body)
	}
	if !strings.Contains(body, `status-succeeded`) {
		t.Fatalf("Expected terminal app card to show succeeded state, got:\n%s", body)
	}
}

func TestHertzAdminAppsListShowsActivePhaseLabel(t *testing.T) {
	store := newTestAppStoreWithJobs(t)
	app, _ := store.Create("Installing Deploy store.App", "sec-test-3", "/bin3", "svc3", "repo3", "art3")

	store.DB().Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, download_bytes, download_duration_ms, download_speed_bps) VALUES ('job-installing', 1, ?, 'v3.0.0', 'in_progress', 'webhook', 1000, 0, 0)`,
		app.ID,
	)

	handler := newTestAppAdminHandler(t, store)
	handler.tracker.Start(app.ID, "job-installing", "v3.0.0")
	handler.tracker.Update(app.ID, 1000, 1000, 2500)
	handler.tracker.SetPhase(app.ID, progress.ProgressStageInstalling)

	engine := newTestHertzAppServer(t, handler)

	w := ut.PerformRequest(engine, "GET", "/admin/apps", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", resp.StatusCode())
	}
	body := string(resp.Body())
	if !strings.Contains(body, `data-progress-phase`) {
		t.Fatalf("Expected body to contain data-progress-phase, got:\n%s", body)
	}
	if !strings.Contains(body, `Applying update`) {
		t.Fatalf("Expected body to describe installing phase, got:\n%s", body)
	}
	if !strings.Contains(body, `status-installing`) {
		t.Fatalf("Expected body to contain status-installing badge class, got:\n%s", body)
	}
	if !strings.Contains(body, ">installing<") && !strings.Contains(body, "installing") {
		t.Fatalf("Expected body to show installing badge text, got:\n%s", body)
	}
	if !strings.Contains(body, `data-progress-percent`) {
		t.Fatalf("Expected installing phase to keep stable download percent hook, got:\n%s", body)
	}
	if !strings.Contains(body, `data-progress-speed`) {
		t.Fatalf("Expected installing phase to keep stable speed hook, got:\n%s", body)
	}
	if !strings.Contains(body, `data-progress-bytes`) {
		t.Fatalf("Expected installing phase to keep stable byte hook, got:\n%s", body)
	}
	if !strings.Contains(body, `Download complete. Restarting service and verifying health.`) {
		t.Fatalf("Expected installing detail text, got:\n%s", body)
	}
}
