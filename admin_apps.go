package main

import (
	"html/template"
	"net/http"
	"strings"
)

type AppAdminHandler struct {
	store     *AppStore
	templates map[string]*template.Template
}

func NewAppAdminHandler(store *AppStore, templates map[string]*template.Template) *AppAdminHandler {
	return &AppAdminHandler{
		store:     store,
		templates: templates,
	}
}

type appsListData struct {
	Apps         []App
	Flash        string
	FlashMessage string
	FlashIsError bool
}

type appFormData struct {
	App    *App
	Errors []string
	IsEdit bool
	Flash        string
	FlashMessage string
	FlashIsError bool
}

func (h *AppAdminHandler) ListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.store.List()
	if err != nil {
		http.Error(w, "Failed to list apps", http.StatusInternalServerError)
		return
	}

	flash := r.URL.Query().Get("flash")
	
	data := appsListData{
		Apps:         apps,
		Flash:        flash,
		FlashMessage: flash,
		FlashIsError: false, // Could inspect flash content or add error param, but keep simple
	}

	if err := h.templates["apps_list.html"].ExecuteTemplate(w, "base.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *AppAdminHandler) NewAppForm(w http.ResponseWriter, r *http.Request) {
	data := appFormData{
		App:    nil,
		Errors: nil,
		IsEdit: false,
	}
	if err := h.templates["app_form.html"].ExecuteTemplate(w, "base.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
	if app.Name == "" { errors = append(errors, "Name is required") }
	if app.BinaryPath == "" { errors = append(errors, "Binary Path is required") }
	if app.ServiceName == "" { errors = append(errors, "Service Name is required") }
	if app.GithubRepo == "" { errors = append(errors, "GitHub Repo is required") }
	if app.ArtifactName == "" { errors = append(errors, "Artifact Name is required") }
	if secret == "" { errors = append(errors, "Webhook Secret is required") }

	if len(errors) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		h.templates["app_form.html"].ExecuteTemplate(w, "base.html", appFormData{
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
		h.templates["app_form.html"].ExecuteTemplate(w, "base.html", appFormData{
			App:    app,
			Errors: errors,
			IsEdit: false,
		})
		return
	}

	http.Redirect(w, r, "/admin/apps?flash=App+created+successfully", http.StatusSeeOther)
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
	if err := h.templates["app_form.html"].ExecuteTemplate(w, "base.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
	if updatedApp.Name == "" { errors = append(errors, "Name is required") }
	if updatedApp.BinaryPath == "" { errors = append(errors, "Binary Path is required") }
	if updatedApp.ServiceName == "" { errors = append(errors, "Service Name is required") }
	if updatedApp.GithubRepo == "" { errors = append(errors, "GitHub Repo is required") }
	if updatedApp.ArtifactName == "" { errors = append(errors, "Artifact Name is required") }

	if len(errors) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		h.templates["app_form.html"].ExecuteTemplate(w, "base.html", appFormData{
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
		h.templates["app_form.html"].ExecuteTemplate(w, "base.html", appFormData{
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
				h.templates["app_form.html"].ExecuteTemplate(w, "base.html", appFormData{
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

	http.Redirect(w, r, "/admin/apps?flash=App+updated+successfully", http.StatusSeeOther)
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
	http.Redirect(w, r, "/admin/apps?flash=App+"+status+"+successfully", http.StatusSeeOther)
}

func RegisterAdminAppRoutes(mux *http.ServeMux, handler *AppAdminHandler, middleware func(http.Handler) http.Handler) {
	mux.Handle("GET /admin/apps", middleware(http.HandlerFunc(handler.ListApps)))
	mux.Handle("GET /admin/apps/new", middleware(http.HandlerFunc(handler.NewAppForm)))
	mux.Handle("POST /admin/apps/create", middleware(http.HandlerFunc(handler.CreateApp)))
	mux.Handle("GET /admin/apps/{id}/edit", middleware(http.HandlerFunc(handler.EditAppForm)))
	mux.Handle("POST /admin/apps/{id}/update", middleware(http.HandlerFunc(handler.UpdateApp)))
	mux.Handle("POST /admin/apps/{id}/enable", middleware(http.HandlerFunc(handler.ToggleApp)))
	mux.Handle("POST /admin/apps/{id}/disable", middleware(http.HandlerFunc(handler.ToggleApp)))
}
