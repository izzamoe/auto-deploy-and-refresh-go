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

	return NewAppAdminHandler(store, queue, tmpls)
}

func TestAdminAppsList(t *testing.T) {
	store := newTestAppStoreWithJobs(t)
	handler := newTestAppAdminHandler(t, store)

	mux := http.NewServeMux()
	RegisterAdminAppRoutes(mux, handler, func(h http.Handler) http.Handler { return h })

	req := httptest.NewRequest("GET", "/admin/apps", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `id="apps-table"`) {
		t.Errorf("Expected body to contain #apps-table, got:\n%s", rr.Body.String())
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
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES ('activejob', 1, ?, 'v1.0.0', 'in_progress', 'webhook')`,
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
