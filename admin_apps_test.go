package main

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newTestAppAdminHandler(t *testing.T, store *AppStore) *AppAdminHandler {
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

	queue, err := NewDeployQueue(store.db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}

	return NewAppAdminHandler(store, queue, tmpls, NewProgressTracker())
}

func TestAdminAppsListAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	_, _ = store.Create("Test App", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps", nil)
	req.Header.Set(adminUIRequestHeader, "true")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("Expected AdminUI fragment without full document, got %s", body)
	}
	if !strings.Contains(body, `id="apps-table"`) {
		t.Fatalf("Expected AdminUI apps fragment, got %s", body)
	}
}

func TestAdminNewAppFormAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps/new", nil)
	req.Header.Set(adminUIRequestHeader, "true")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("Expected AdminUI fragment without full document, got %s", body)
	}
	if !strings.Contains(body, `id="app-form"`) {
		t.Fatalf("Expected AdminUI app form fragment, got %s", body)
	}
}

func TestAdminEditAppFormAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps/"+app.ID+"/edit", nil)
	req.Header.Set(adminUIRequestHeader, "true")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", rr.Code)
	}
	body := rr.Body.String()
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

func TestAdminAppsDelete(t *testing.T) {
	store := newTestAppStoreWithJobs(t)
	app, _ := store.Create("del-app", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/delete", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("Expected 303, got %d. Body: %s", rr.Code, rr.Body.String())
	}

	_, err := store.Get(app.ID)
	if err == nil {
		t.Error("Expected app to be deleted")
	}
}

func TestAdminAppsDeleteBlockedByActiveJob(t *testing.T) {
	store := newTestAppStoreWithJobs(t)
	app, _ := store.Create("active-app", "sec2", "/bin2", "svc2", "repo2", "art2")

	store.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, download_bytes, download_duration_ms, download_speed_bps) VALUES ('activejob', 1, ?, 'v1.0.0', 'in_progress', 'webhook', 0, 0, 0)`,
		app.ID,
	)

	handler := newTestAppAdminHandler(t, store)
	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/delete", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("Expected 303 redirect, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "flash_error=1") {
		t.Errorf("Expected flash_error=1 in redirect, got %q", loc)
	}

	_, err := store.Get(app.ID)
	if err != nil {
		t.Error("App should still exist after blocked delete")
	}
}

func TestAdminAppsCreate(t *testing.T) {
	store := newTestAppStore(t)
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	form := url.Values{}
	form.Add("name", "test-app")
	form.Add("binary_path", "/opt/test-app")
	form.Add("service_name", "test-app.service")
	form.Add("github_repo", "test/repo")
	form.Add("artifact_name", "test-artifact")
	form.Add("webhook_secret", "secret123")

	req := httptest.NewRequest("POST", "/admin/apps/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("Expected 303 Redirect, got %d", rr.Code)
	}

	apps, err := store.List()
	if err != nil {
		t.Fatalf("failed to list apps: %v", err)
	}
	if len(apps) != 1 {
		t.Errorf("Expected 1 app, got %d", len(apps))
	}
}

func TestAdminAppsCreateAdminUIUsesAdminLocation(t *testing.T) {
	store := newTestAppStore(t)
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	form := url.Values{}
	form.Add("name", "test-app")
	form.Add("binary_path", "/opt/test-app")
	form.Add("service_name", "test-app.service")
	form.Add("github_repo", "test/repo")
	form.Add("artifact_name", "test-artifact")
	form.Add("webhook_secret", "secret123")

	req := httptest.NewRequest("POST", "/admin/apps/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(adminUIRequestHeader, "true")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for AdminUI create, got %d", rr.Code)
	}
	if rr.Header().Get("Location") != "" {
		t.Fatalf("Expected no raw redirect Location header, got %q", rr.Header().Get("Location"))
	}
	adminLocation := rr.Header().Get(adminUILocationHeader)
	if !strings.Contains(adminLocation, "/admin/apps?flash=App+created+successfully") {
		t.Fatalf("Expected AdminUI success navigation, got %q", adminLocation)
	}
}

func TestAdminAppsCreateAdminUIValidationErrorsReturnInlineFormFragment(t *testing.T) {
	store := newTestAppStore(t)
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	form := url.Values{}
	form.Add("name", "Preserved Name")

	req := httptest.NewRequest("POST", "/admin/apps/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(adminUIRequestHeader, "true")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400 Bad Request, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "<div class=\"page-header\">") {
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
	if got := rr.Header().Get(adminUILocationHeader); got != "" {
		t.Fatalf("Expected no AdminUI location on validation error, got %q", got)
	}
}

func TestAdminAppsToggleAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/disable", nil)
	req.Header.Set(adminUIRequestHeader, "true")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for AdminUI toggle, got %d", rr.Code)
	}
	if rr.Header().Get("Location") != "" {
		t.Fatalf("Expected no raw redirect Location header, got %q", rr.Header().Get("Location"))
	}
	if got := rr.Header().Get(adminUILocationHeader); got != "" {
		t.Fatalf("Expected no AdminUI location for in-place toggle, got %q", got)
	}

	body := rr.Body.String()
	if !strings.Contains(body, `id="apps-table"`) {
		t.Fatalf("Expected AdminUI apps fragment, got %s", body)
	}
	if !strings.Contains(body, `App disabled successfully`) {
		t.Fatalf("Expected flash message in body, got %s", body)
	}
}

func TestAdminAppsDeleteAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/delete", nil)
	req.Header.Set(adminUIRequestHeader, "true")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for AdminUI delete, got %d", rr.Code)
	}
	if got := rr.Header().Get(adminUILocationHeader); got != "" {
		t.Fatalf("Expected no AdminUI location for in-place delete, got %q", got)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `id="apps-table"`) {
		t.Fatalf("Expected AdminUI apps fragment, got %s", body)
	}
	if !strings.Contains(body, `App deleted successfully`) {
		t.Fatalf("Expected flash message in body, got %s", body)
	}
}

func TestAdminAppsEdit(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps/"+app.ID+"/edit", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `id="app-form"`) {
		t.Errorf("Expected body to contain #app-form, got %s", rr.Body.String())
	}
}

func TestAdminAppsEnableDisable(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	// Test disable
	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/disable", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("Expected 303 Redirect, got %d", rr.Code)
	}

	updated, _ := store.Get(app.ID)
	if updated.Enabled {
		t.Errorf("Expected app to be disabled")
	}

	// Test enable
	req2 := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/enable", nil)
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusSeeOther {
		t.Errorf("Expected 303 Redirect, got %d", rr2.Code)
	}

	updated2, _ := store.Get(app.ID)
	if !updated2.Enabled {
		t.Errorf("Expected app to be enabled")
	}
}

func TestAdminAppsEditLeavesSecretUnchangedWhenBlank(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "original-secret", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	form := url.Values{}
	form.Add("name", "Updated Test")
	form.Add("binary_path", "/bin")
	form.Add("service_name", "svc")
	form.Add("github_repo", "repo")
	form.Add("artifact_name", "art")
	form.Add("webhook_secret", "") // Blank

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("Expected 303 Redirect, got %d. Body: %s", rr.Code, rr.Body.String())
	}

	updated, _ := store.Get(app.ID)
	if updated.WebhookSecretHash != app.WebhookSecretHash {
		t.Errorf("Expected secret hash to be unchanged")
	}
	if updated.Name != "Updated Test" {
		t.Errorf("Expected name to be updated")
	}
}

func TestAdminAppsUpdateAdminUIValidationErrorsReturnInlineFormFragment(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Original", "secret", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	form := url.Values{}
	form.Add("name", "Updated Name")
	form.Add("binary_path", "")
	form.Add("service_name", "svc")
	form.Add("github_repo", "repo")
	form.Add("artifact_name", "art")

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(adminUIRequestHeader, "true")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400 Bad Request, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "<div class=\"page-header\">") {
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
	if got := rr.Header().Get(adminUILocationHeader); got != "" {
		t.Fatalf("Expected no AdminUI location on validation error, got %q", got)
	}
}

func TestAdminAppsUpdateAdminUIUsesAdminLocation(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Test", "secret", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	form := url.Values{}
	form.Add("name", "Updated Test")
	form.Add("binary_path", "/opt/updated")
	form.Add("service_name", "updated.service")
	form.Add("github_repo", "updated/repo")
	form.Add("artifact_name", "updated-artifact")
	form.Add("webhook_secret", "")

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(adminUIRequestHeader, "true")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for AdminUI update, got %d", rr.Code)
	}
	if rr.Header().Get("Location") != "" {
		t.Fatalf("Expected no raw redirect Location header, got %q", rr.Header().Get("Location"))
	}
	if got := rr.Header().Get(adminUILocationHeader); !strings.Contains(got, "/admin/apps?flash=App+updated+successfully") {
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

func TestAdminAppsCreateValidationErrorsPreserveFormValues(t *testing.T) {
	store := newTestAppStore(t)
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	form := url.Values{}
	form.Add("name", "Preserved Name")
	// Missing required fields

	req := httptest.NewRequest("POST", "/admin/apps/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request, got %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, `id="form-errors"`) {
		t.Errorf("Expected body to contain #form-errors")
	}
	if !strings.Contains(body, `value="Preserved Name"`) {
		t.Errorf("Expected body to contain preserved name value")
	}
}

func TestAdminAppsManualDeployAdminUIReturnsFragment(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("Deploy", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	form := url.Values{}
	form.Add("tag", "v1.2.3")
	form.Add("source", "list")

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/deploy", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(adminUIRequestHeader, "true")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for AdminUI deploy, got %d", rr.Code)
	}
	if got := rr.Header().Get(adminUILocationHeader); got != "" {
		t.Fatalf("Expected no AdminUI location for in-place list deploy, got %q", got)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `id="apps-table"`) {
		t.Fatalf("Expected AdminUI apps fragment, got %s", body)
	}
	if !strings.Contains(body, `Manual deploy queued`) {
		t.Fatalf("Expected flash message in body, got %s", body)
	}
}

func TestAdminAppsManualDeploy(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("deploy-app", "sec", "/bin", "svc", "repo", "art")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	form := url.Values{}
	form.Add("tag", "v1.0.0")

	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/deploy", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("Expected 303, got %d. Body: %s", rr.Code, rr.Body.String())
	}

	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "/history") {
		t.Errorf("Expected redirect to history, got %q", loc)
	}
}

func TestAdminAppsManualDeployDisabledApp(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("dis-app", "sec3", "/bin3", "svc3", "repo3", "art3")
	store.SetEnabled(app.ID, false)
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	form := url.Values{}
	form.Add("tag", "v1.0.0")
	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/deploy", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("Expected 303, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "flash_error=1") {
		t.Errorf("Expected flash_error in redirect, got %q", loc)
	}
}

func TestAdminAppsManualDeployEmptyTag(t *testing.T) {
	store := newTestAppStore(t)
	app, _ := store.Create("tag-app", "sec4", "/bin4", "svc4", "repo4", "art4")
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	form := url.Values{}
	form.Add("tag", "")
	req := httptest.NewRequest("POST", "/admin/apps/"+app.ID+"/deploy", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("Expected 303, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "flash_error=1") {
		t.Errorf("Expected flash_error in redirect for empty tag, got %q", loc)
	}
}

func TestAdminAppsList(t *testing.T) {
	store := newTestAppStoreWithJobs(t)
	app, _ := store.Create("Active Deploy App", "sec-test", "/bin", "svc", "repo", "art")

	store.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, download_bytes, download_duration_ms, download_speed_bps) VALUES ('job-123', 1, ?, 'v1.0.0', 'in_progress', 'webhook', 0, 0, 0)`,
		app.ID,
	)

	handler := newTestAppAdminHandler(t, store)

	// Set progress for the app
	handler.tracker.Start(app.ID, "job-123", "v1.0.0")
	handler.tracker.Update(app.ID, 450, 1000, 2500)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", rr.Code)
	}
	body := rr.Body.String()

	// Check for DOM hooks
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

func TestAdminAppsListTerminalJobClearsActiveMarker(t *testing.T) {
	store := newTestAppStoreWithJobs(t)
	app, _ := store.Create("Settled Deploy App", "sec-test-2", "/bin2", "svc2", "repo2", "art2")

	store.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, download_bytes, download_duration_ms, download_speed_bps) VALUES ('job-999', 1, ?, 'v2.0.0', 'succeeded', 'webhook', 4096, 1000, 512)`,
		app.ID,
	)

	handler := newTestAppAdminHandler(t, store)
	handler.tracker.Start(app.ID, "job-999", "v2.0.0")
	handler.tracker.Update(app.ID, 2048, 4096, 256)
	handler.tracker.Finish(app.ID)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, `data-progress-job="job-999"`) {
		t.Fatalf("Expected terminal app card to clear stale progress marker, got:\n%s", body)
	}
	if !strings.Contains(body, `status-succeeded`) {
		t.Fatalf("Expected terminal app card to show succeeded state, got:\n%s", body)
	}
}

func TestAdminAppsListShowsActivePhaseLabel(t *testing.T) {
	store := newTestAppStoreWithJobs(t)
	app, _ := store.Create("Installing Deploy App", "sec-test-3", "/bin3", "svc3", "repo3", "art3")

	store.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, download_bytes, download_duration_ms, download_speed_bps) VALUES ('job-installing', 1, ?, 'v3.0.0', 'in_progress', 'webhook', 1000, 0, 0)`,
		app.ID,
	)

	handler := newTestAppAdminHandler(t, store)
	handler.tracker.Start(app.ID, "job-installing", "v3.0.0")
	handler.tracker.Update(app.ID, 1000, 1000, 2500)
	handler.tracker.SetPhase(app.ID, StageInstalling)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", rr.Code)
	}
	body := rr.Body.String()
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
