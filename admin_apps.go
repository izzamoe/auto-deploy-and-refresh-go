package main

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type AppAdminHandler struct {
	store     *AppStore
	queue     *DeployQueue
	tracker   *ProgressTracker
	templates map[string]*template.Template
}

func NewAppAdminHandler(store *AppStore, queue *DeployQueue, templates map[string]*template.Template, tracker *ProgressTracker) *AppAdminHandler {
	return &AppAdminHandler{
		store:     store,
		queue:     queue,
		tracker:   tracker,
		templates: templates,
	}
}

type appsListData struct {
	Apps         []AppWithLastDeploy
	ProgressStreamURL string
	Flash        string
	FlashMessage string
	FlashIsError bool
	CurlSecret   string
	CurlAppName  string
}

type appFormData struct {
	App          *App
	Errors       []string
	IsEdit       bool
	Flash        string
	FlashMessage string
	FlashIsError bool
}

func (h *AppAdminHandler) renderAppForm(w http.ResponseWriter, r *http.Request, data appFormData) {
	if isHTMXRequest(r) && r.Method == http.MethodPost {
		if err := h.templates["app_form.html"].ExecuteTemplate(w, "app-form", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if err := renderAdminTemplate(w, r, h.templates["app_form.html"], data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *AppAdminHandler) ListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.store.ListWithLastDeploy()
	if err != nil {
		http.Error(w, "Failed to list apps", http.StatusInternalServerError)
		return
	}
	if h.tracker != nil {
		for i := range apps {
			if snapshot, ok := activeSnapshotForApp(h.tracker, apps[i]); ok {
				apps[i].LiveProgress = snapshot
			}
		}
	}

	flash := r.URL.Query().Get("flash")
	flashError := r.URL.Query().Get("flash_error") == "1"
	curlSecret := r.URL.Query().Get("curl")
	curlAppName := r.URL.Query().Get("appname")

	data := appsListData{
		Apps:              apps,
		ProgressStreamURL: buildProgressStreamURL(activeAppIDsFromApps(apps), activeJobIDsFromApps(apps)),
		Flash:             flash,
		FlashMessage:      flash,
		FlashIsError:      flashError,
		CurlSecret:        curlSecret,
		CurlAppName:       curlAppName,
	}

	if err := renderAdminTemplate(w, r, h.templates["apps_list.html"], data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *AppAdminHandler) NewAppForm(w http.ResponseWriter, r *http.Request) {
	data := appFormData{
		App:    nil,
		Errors: nil,
		IsEdit: false,
	}
	h.renderAppForm(w, r, data)
}

func (h *AppAdminHandler) CreateApp(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	app := &App{
		Name:         r.FormValue("name"),
		BinaryPath:   r.FormValue("binary_path"),
		ServiceName:  r.FormValue("service_name"),
		GithubRepo:   r.FormValue("github_repo"),
		ArtifactName: r.FormValue("artifact_name"),
	}
	secret := r.FormValue("webhook_secret")

	var errors []string
	if app.Name == "" {
		errors = append(errors, "Name is required")
	}
	if app.BinaryPath == "" {
		errors = append(errors, "Binary Path is required")
	}
	if app.ServiceName == "" {
		errors = append(errors, "Service Name is required")
	}
	if app.GithubRepo == "" {
		errors = append(errors, "GitHub Repo is required")
	}
	if app.ArtifactName == "" {
		errors = append(errors, "Artifact Name is required")
	}
	if secret == "" {
		errors = append(errors, "Webhook Secret is required")
	}

	if len(errors) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		h.renderAppForm(w, r, appFormData{
			App:    app,
			Errors: errors,
			IsEdit: false,
		})
		return
	}

	_, err := h.store.Create(app.Name, secret, app.BinaryPath, app.ServiceName, app.GithubRepo, app.ArtifactName)
	if err != nil {
		if err == ErrDuplicateApp {
			errors = append(errors, "An app with this binary path, service name, or webhook secret already exists")
		} else {
			errors = append(errors, "Internal error creating app")
		}
		w.WriteHeader(http.StatusBadRequest)
		h.renderAppForm(w, r, appFormData{
			App:    app,
			Errors: errors,
			IsEdit: false,
		})
		return
	}

	adminNavigate(w, r, "/admin/apps?flash=App+created+successfully&curl="+url.QueryEscape(secret)+"&appname="+url.QueryEscape(app.Name))
}

func (h *AppAdminHandler) EditAppForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	app, err := h.store.Get(id)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	data := appFormData{
		App:    app,
		Errors: nil,
		IsEdit: true,
	}
	h.renderAppForm(w, r, data)
}

func (h *AppAdminHandler) UpdateApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	app, err := h.store.Get(id)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	updatedApp := &App{
		ID:           app.ID,
		Name:         r.FormValue("name"),
		BinaryPath:   r.FormValue("binary_path"),
		ServiceName:  r.FormValue("service_name"),
		GithubRepo:   r.FormValue("github_repo"),
		ArtifactName: r.FormValue("artifact_name"),
		Enabled:      app.Enabled,
	}
	secret := r.FormValue("webhook_secret")

	var errors []string
	if updatedApp.Name == "" {
		errors = append(errors, "Name is required")
	}
	if updatedApp.BinaryPath == "" {
		errors = append(errors, "Binary Path is required")
	}
	if updatedApp.ServiceName == "" {
		errors = append(errors, "Service Name is required")
	}
	if updatedApp.GithubRepo == "" {
		errors = append(errors, "GitHub Repo is required")
	}
	if updatedApp.ArtifactName == "" {
		errors = append(errors, "Artifact Name is required")
	}

	if len(errors) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		h.renderAppForm(w, r, appFormData{
			App:    updatedApp,
			Errors: errors,
			IsEdit: true,
		})
		return
	}

	err = h.store.Update(id, updatedApp.Name, updatedApp.BinaryPath, updatedApp.ServiceName, updatedApp.GithubRepo, updatedApp.ArtifactName, updatedApp.Enabled)
	if err != nil {
		if err == ErrDuplicateApp {
			errors = append(errors, "An app with this binary path or service name already exists")
		} else {
			errors = append(errors, "Internal error updating app")
		}
		w.WriteHeader(http.StatusBadRequest)
		h.renderAppForm(w, r, appFormData{
			App:    updatedApp,
			Errors: errors,
			IsEdit: true,
		})
		return
	}

	if secret != "" {
		if err := h.store.RotateSecret(id, secret); err != nil {
			if err == ErrDuplicateApp {
				errors = append(errors, "An app with this webhook secret already exists")
				w.WriteHeader(http.StatusBadRequest)
				h.renderAppForm(w, r, appFormData{
					App:    updatedApp,
					Errors: errors,
					IsEdit: true,
				})
				return
			}
			http.Error(w, "Failed to rotate secret", http.StatusInternalServerError)
			return
		}
	}

	adminNavigate(w, r, "/admin/apps?flash=App+updated+successfully")
}

func (h *AppAdminHandler) renderListInPlaceOrRedirect(w http.ResponseWriter, r *http.Request, flashMsg string, isError bool) {
	if !isHTMXRequest(r) {
		dest := "/admin/apps"
		if flashMsg != "" {
			dest += "?flash=" + url.QueryEscape(flashMsg)
			if isError {
				dest += "&flash_error=1"
			}
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}

	apps, err := h.store.ListWithLastDeploy()
	if err != nil {
		http.Error(w, "Failed to list apps", http.StatusInternalServerError)
		return
	}
	if h.tracker != nil {
		for i := range apps {
			if snapshot, ok := activeSnapshotForApp(h.tracker, apps[i]); ok {
				apps[i].LiveProgress = snapshot
			}
		}
	}

	data := appsListData{
		Apps:              apps,
		ProgressStreamURL: buildProgressStreamURL(activeAppIDsFromApps(apps), activeJobIDsFromApps(apps)),
		FlashMessage:      flashMsg,
		FlashIsError:      isError,
	}

	if err := renderAdminTemplate(w, r, h.templates["apps_list.html"], data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *AppAdminHandler) DeleteApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.store.Delete(id)
	if err != nil {
		if errors.Is(err, ErrActiveDeployExists) {
			h.renderListInPlaceOrRedirect(w, r, "Cannot delete app with active deploy", true)
			return
		}
		http.Error(w, "Failed to delete app", http.StatusInternalServerError)
		return
	}
	h.renderListInPlaceOrRedirect(w, r, "App deleted successfully", false)
}

func (h *AppAdminHandler) ManualDeployApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	source := r.FormValue("source")
	isList := source == "list"

	fail := func(msg string) {
		if isList {
			h.renderListInPlaceOrRedirect(w, r, msg, true)
		} else {
			adminNavigate(w, r, "/admin/apps/"+id+"/history?flash="+url.QueryEscape(msg)+"&flash_error=1")
		}
	}

	success := func(msg string) {
		if isList {
			h.renderListInPlaceOrRedirect(w, r, msg, false)
		} else {
			adminNavigate(w, r, "/admin/apps/"+id+"/history?flash="+url.QueryEscape(msg))
		}
	}

	app, err := h.store.Get(id)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	if !app.Enabled {
		fail("Cannot deploy disabled app")
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	tag := r.FormValue("tag")
	if tag == "" {
		fail("Tag is required")
		return
	}

	err = h.queue.EnqueueManual(id, tag)
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			fail("Deploy already queued for this tag")
			return
		}
		if errors.Is(err, ErrQueueFull) {
			fail("Queue is full")
			return
		}
		http.Error(w, "Failed to queue deploy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	success("Manual deploy queued for " + tag)
}

func (h *AppAdminHandler) ToggleApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Determine enable/disable based on the path
	enable := strings.HasSuffix(r.URL.Path, "/enable")

	if err := h.store.SetEnabled(id, enable); err != nil {
		http.Error(w, "Failed to update app status", http.StatusInternalServerError)
		return
	}

	status := "enabled"
	if !enable {
		status = "disabled"
	}
	h.renderListInPlaceOrRedirect(w, r, "App "+status+" successfully", false)
}

func RegisterAdminAppRoutes(mux *http.ServeMux, handler *AppAdminHandler, middleware func(http.Handler) http.Handler) {
	mux.Handle("GET /admin/apps", middleware(http.HandlerFunc(handler.ListApps)))
	mux.Handle("GET /admin/apps/new", middleware(http.HandlerFunc(handler.NewAppForm)))
	mux.Handle("POST /admin/apps/create", middleware(http.HandlerFunc(handler.CreateApp)))
	mux.Handle("GET /admin/apps/{id}/edit", middleware(http.HandlerFunc(handler.EditAppForm)))
	mux.Handle("POST /admin/apps/{id}/update", middleware(http.HandlerFunc(handler.UpdateApp)))
	mux.Handle("POST /admin/apps/{id}/delete", middleware(http.HandlerFunc(handler.DeleteApp)))
	mux.Handle("POST /admin/apps/{id}/enable", middleware(http.HandlerFunc(handler.ToggleApp)))
	mux.Handle("POST /admin/apps/{id}/disable", middleware(http.HandlerFunc(handler.ToggleApp)))
	mux.Handle("POST /admin/apps/{id}/deploy", middleware(http.HandlerFunc(handler.ManualDeployApp)))
}

func activeAppIDsFromApps(apps []AppWithLastDeploy) []string {
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for _, app := range apps {
		if app.ID == "" || !isActiveDeployStatus(app.LastDeployStatus) {
			continue
		}
		if _, ok := seen[app.ID]; ok {
			continue
		}
		seen[app.ID] = struct{}{}
		ids = append(ids, app.ID)
	}
	sort.Strings(ids)
	return ids
}

func activeJobIDsFromApps(apps []AppWithLastDeploy) []string {
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for _, app := range apps {
		if app.LastJobID == "" || !isActiveDeployStatus(app.LastDeployStatus) {
			continue
		}
		if _, ok := seen[app.LastJobID]; ok {
			continue
		}
		seen[app.LastJobID] = struct{}{}
		ids = append(ids, app.LastJobID)
	}
	sort.Strings(ids)
	return ids
}

func isActiveDeployStatus(status string) bool {
	return status == "pending" || status == "in_progress"
}

func activeSnapshotForApp(tracker *ProgressTracker, app AppWithLastDeploy) (*ProgressSnapshot, bool) {
	if tracker == nil || !isActiveDeployStatus(app.LastDeployStatus) || app.LastJobID == "" {
		return nil, false
	}
	snapshot, ok := tracker.Snapshot(app.ID)
	if !ok || snapshot.JobID != app.LastJobID {
		return nil, false
	}
	return snapshot, true
}

func buildProgressStreamURL(appIDs, jobIDs []string) string {
	if len(appIDs) == 0 && len(jobIDs) == 0 {
		return ""
	}
	values := url.Values{}
	for _, appID := range appIDs {
		if appID != "" {
			values.Add("app_id", appID)
		}
	}
	for _, jobID := range jobIDs {
		if jobID != "" {
			values.Add("job_id", jobID)
		}
	}
	encoded := values.Encode()
	if encoded == "" {
		return ""
	}
	return "/admin/progress/stream?" + encoded
}
