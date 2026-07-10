package admin

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

type AppAdminHandler struct {
	store     *store.AppStore
	queue     *store.DeployQueue
	tracker   *progress.ProgressTracker
	templates map[string]*template.Template
}

func NewAppAdminHandler(store *store.AppStore, queue *store.DeployQueue, templates map[string]*template.Template, tracker *progress.ProgressTracker) *AppAdminHandler {
	return &AppAdminHandler{
		store:     store,
		queue:     queue,
		tracker:   tracker,
		templates: templates,
	}
}

type appsListData struct {
	Apps         []store.AppWithLastDeploy
	Flash        string
	FlashMessage string
	FlashIsError bool
	CurlSecret   string
	CurlAppName  string
}

type appFormData struct {
	App          *store.App
	Errors       []string
	IsEdit       bool
	Flash        string
	FlashMessage string
	FlashIsError bool
}

func (h *AppAdminHandler) renderAppFormHertz(c *app.RequestContext, data appFormData) {
	if isAdminUIRequestHertz(c) && string(c.Request.Method()) == http.MethodPost {
		if err := h.templates["app_form.html"].ExecuteTemplate(c.Response.BodyWriter(), "app-form", data); err != nil {
			c.String(http.StatusInternalServerError, err.Error())
		}
		return
	}

	if err := renderAdminTemplateHertz(c, h.templates["app_form.html"], data); err != nil {
		c.String(http.StatusInternalServerError, err.Error())
	}
}

func (h *AppAdminHandler) ListAppsHertz(ctx context.Context, c *app.RequestContext) {
	apps, err := h.store.ListWithLastDeploy()
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to list apps")
		return
	}
	if h.tracker != nil {
		for i := range apps {
			if snapshot, ok := activeSnapshotForApp(h.tracker, apps[i]); ok {
				apps[i].LiveProgress = snapshot
			}
		}
	}

	flash := c.Query("flash")
	flashError := c.Query("flash_error") == "1"
	curlSecret := c.Query("curl")
	curlAppName := c.Query("appname")

	data := appsListData{
		Apps:         apps,
		Flash:        flash,
		FlashMessage: flash,
		FlashIsError: flashError,
		CurlSecret:   curlSecret,
		CurlAppName:  curlAppName,
	}

	if err := renderAdminTemplateHertz(c, h.templates["apps_list.html"], data); err != nil {
		c.String(http.StatusInternalServerError, err.Error())
	}
}

func (h *AppAdminHandler) NewAppFormHertz(ctx context.Context, c *app.RequestContext) {
	data := appFormData{
		App:    nil,
		Errors: nil,
		IsEdit: false,
	}
	h.renderAppFormHertz(c, data)
}

func (h *AppAdminHandler) CreateAppHertz(ctx context.Context, c *app.RequestContext) {
	app := &store.App{
		Name:         string(c.PostForm("name")),
		BinaryPath:   string(c.PostForm("binary_path")),
		ServiceName:  string(c.PostForm("service_name")),
		GithubRepo:   string(c.PostForm("github_repo")),
		ArtifactName: string(c.PostForm("artifact_name")),
	}
	secret := string(c.PostForm("webhook_secret"))

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
		c.SetStatusCode(http.StatusBadRequest)
		h.renderAppFormHertz(c, appFormData{
			App:    app,
			Errors: errors,
			IsEdit: false,
		})
		return
	}

	_, err := h.store.Create(app.Name, secret, app.BinaryPath, app.ServiceName, app.GithubRepo, app.ArtifactName)
	if err != nil {
		if err == store.ErrDuplicateApp {
			errors = append(errors, "An app with this binary path, service name, or webhook secret already exists")
		} else {
			errors = append(errors, "Internal error creating app")
		}
		c.SetStatusCode(http.StatusBadRequest)
		h.renderAppFormHertz(c, appFormData{
			App:    app,
			Errors: errors,
			IsEdit: false,
		})
		return
	}

	adminUINavigateHertz(c, "/admin/apps?flash=App+created+successfully&curl="+url.QueryEscape(secret)+"&appname="+url.QueryEscape(app.Name))
}

func (h *AppAdminHandler) EditAppFormHertz(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	app, err := h.store.Get(id)
	if err != nil {
		c.String(http.StatusNotFound, "App not found")
		return
	}

	data := appFormData{
		App:    app,
		Errors: nil,
		IsEdit: true,
	}
	h.renderAppFormHertz(c, data)
}

func (h *AppAdminHandler) UpdateAppHertz(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	app, err := h.store.Get(id)
	if err != nil {
		c.String(http.StatusNotFound, "App not found")
		return
	}

	updatedApp := &store.App{
		ID:           app.ID,
		Name:         string(c.PostForm("name")),
		BinaryPath:   string(c.PostForm("binary_path")),
		ServiceName:  string(c.PostForm("service_name")),
		GithubRepo:   string(c.PostForm("github_repo")),
		ArtifactName: string(c.PostForm("artifact_name")),
		Enabled:      app.Enabled,
	}
	secret := string(c.PostForm("webhook_secret"))

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
		c.SetStatusCode(http.StatusBadRequest)
		h.renderAppFormHertz(c, appFormData{
			App:    updatedApp,
			Errors: errors,
			IsEdit: true,
		})
		return
	}

	err = h.store.Update(id, updatedApp.Name, updatedApp.BinaryPath, updatedApp.ServiceName, updatedApp.GithubRepo, updatedApp.ArtifactName, updatedApp.Enabled)
	if err != nil {
		if err == store.ErrDuplicateApp {
			errors = append(errors, "An app with this binary path or service name already exists")
		} else {
			errors = append(errors, "Internal error updating app")
		}
		c.SetStatusCode(http.StatusBadRequest)
		h.renderAppFormHertz(c, appFormData{
			App:    updatedApp,
			Errors: errors,
			IsEdit: true,
		})
		return
	}

	if secret != "" {
		if err := h.store.RotateSecret(id, secret); err != nil {
			if err == store.ErrDuplicateApp {
				errors = append(errors, "An app with this webhook secret already exists")
				c.SetStatusCode(http.StatusBadRequest)
				h.renderAppFormHertz(c, appFormData{
					App:    updatedApp,
					Errors: errors,
					IsEdit: true,
				})
				return
			}
			c.String(http.StatusInternalServerError, "Failed to rotate secret")
			return
		}
	}

	adminUINavigateHertz(c, "/admin/apps?flash=App+updated+successfully")
}

func (h *AppAdminHandler) renderListInPlaceOrRedirectHertz(c *app.RequestContext, flashMsg string, isError bool) {
	if !isAdminUIRequestHertz(c) {
		dest := "/admin/apps"
		if flashMsg != "" {
			dest += "?flash=" + url.QueryEscape(flashMsg)
			if isError {
				dest += "&flash_error=1"
			}
		}
		c.Redirect(http.StatusSeeOther, []byte(dest))
		return
	}

	apps, err := h.store.ListWithLastDeploy()
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to list apps")
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
		Apps:         apps,
		FlashMessage: flashMsg,
		FlashIsError: isError,
	}

	if err := renderAdminTemplateHertz(c, h.templates["apps_list.html"], data); err != nil {
		c.String(http.StatusInternalServerError, err.Error())
	}
}

func (h *AppAdminHandler) DeleteAppHertz(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	err := h.store.Delete(id)
	if err != nil {
		if errors.Is(err, store.ErrActiveDeployExists) {
			h.renderListInPlaceOrRedirectHertz(c, "Cannot delete app with active deploy", true)
			return
		}
		c.String(http.StatusInternalServerError, "Failed to delete app")
		return
	}
	h.renderListInPlaceOrRedirectHertz(c, "App deleted successfully", false)
}

func (h *AppAdminHandler) ManualDeployAppHertz(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	source := string(c.PostForm("source"))
	isList := source == "list"

	fail := func(msg string) {
		if isList {
			h.renderListInPlaceOrRedirectHertz(c, msg, true)
		} else {
			adminUINavigateHertz(c, "/admin/apps/"+id+"/history?flash="+url.QueryEscape(msg)+"&flash_error=1")
		}
	}

	success := func(msg string) {
		if isList {
			h.renderListInPlaceOrRedirectHertz(c, msg, false)
		} else {
			adminUINavigateHertz(c, "/admin/apps/"+id+"/history?flash="+url.QueryEscape(msg))
		}
	}

	app, err := h.store.Get(id)
	if err != nil {
		c.String(http.StatusNotFound, "App not found")
		return
	}

	if !app.Enabled {
		fail("Cannot deploy disabled app")
		return
	}

	tag := string(c.PostForm("tag"))
	if tag == "" {
		fail("Tag is required")
		return
	}

	err = h.queue.EnqueueManual(id, tag)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			fail("Deploy already queued for this tag")
			return
		}
		if errors.Is(err, store.ErrInvalidTag) {
			fail("Invalid tag")
			return
		}
		if errors.Is(err, store.ErrQueueFull) {
			fail("Queue is full")
			return
		}
		c.String(http.StatusInternalServerError, "Failed to queue deploy: "+err.Error())
		return
	}

	success("Manual deploy queued for " + tag)
}

func (h *AppAdminHandler) ToggleAppHertz(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")

	// Determine enable/disable based on the path
	enable := strings.HasSuffix(string(c.Request.URI().Path()), "/enable")

	if err := h.store.SetEnabled(id, enable); err != nil {
		c.String(http.StatusInternalServerError, "Failed to update app status")
		return
	}

	status := "enabled"
	if !enable {
		status = "disabled"
	}
	h.renderListInPlaceOrRedirectHertz(c, "App "+status+" successfully", false)
}

func RegisterAdminAppRoutesHertz(h *server.Hertz, handler *AppAdminHandler, auth app.HandlerFunc) {
	h.GET("/admin/apps", auth, handler.ListAppsHertz)
	h.GET("/admin/apps/new", auth, handler.NewAppFormHertz)
	h.GET("/admin/apps/:id/edit", auth, handler.EditAppFormHertz)
	RegisterAdminAppActionRoutesHertz(h, handler, auth)
}

func RegisterAdminAppActionRoutesHertz(h *server.Hertz, handler *AppAdminHandler, auth app.HandlerFunc) {
	h.POST("/admin/apps/create", auth, handler.CreateAppHertz)
	h.POST("/admin/apps/:id/update", auth, handler.UpdateAppHertz)
	h.POST("/admin/apps/:id/delete", auth, handler.DeleteAppHertz)
	h.POST("/admin/apps/:id/enable", auth, handler.ToggleAppHertz)
	h.POST("/admin/apps/:id/disable", auth, handler.ToggleAppHertz)
	h.POST("/admin/apps/:id/deploy", auth, handler.ManualDeployAppHertz)
}

func isActiveDeployStatus(status string) bool {
	return status == "pending" || status == "in_progress"
}

func activeSnapshotForApp(tracker *progress.ProgressTracker, app store.AppWithLastDeploy) (*progress.ProgressSnapshot, bool) {
	if tracker == nil || !isActiveDeployStatus(app.LastDeployStatus) || app.LastJobID == "" {
		return nil, false
	}
	snapshot, ok := tracker.Snapshot(app.ID)
	if !ok || snapshot.JobID != app.LastJobID {
		return nil, false
	}
	return snapshot, true
}
