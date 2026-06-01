package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type AdminAPIHandler struct {
	store   *store.AppStore
	queue   *store.DeployQueue
	tracker *progress.ProgressTracker
	cancel  *cancel.CancelService
}

func NewAdminAPIHandler(store *store.AppStore, queue *store.DeployQueue, tracker *progress.ProgressTracker, cancel *cancel.CancelService) *AdminAPIHandler {
	return &AdminAPIHandler{store: store, queue: queue, tracker: tracker, cancel: cancel}
}

type adminAPIErrorResponse struct {
	Status string   `json:"status"`
	Error  string   `json:"error"`
	Errors []string `json:"errors,omitempty"`
}

type adminAPIStatusResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type adminAPIAppRequest struct {
	Name          string `json:"name"`
	Secret        string `json:"secret"`
	WebhookSecret string `json:"webhookSecret"`
	BinaryPath    string `json:"binaryPath"`
	ServiceName   string `json:"serviceName"`
	GithubRepo    string `json:"githubRepo"`
	ArtifactName  string `json:"artifactName"`
	Enabled       *bool  `json:"enabled"`
}

func (r adminAPIAppRequest) webhookSecret() string {
	if r.Secret != "" {
		return r.Secret
	}
	return r.WebhookSecret
}

type adminAPIDeployRequest struct {
	Tag string `json:"tag"`
}

type adminAPIToggleRequest struct {
	Enabled *bool `json:"enabled"`
}

type adminAPIAppResponse struct {
	ID                   string                    `json:"id"`
	Name                 string                    `json:"name"`
	BinaryPath           string                    `json:"binaryPath"`
	ServiceName          string                    `json:"serviceName"`
	GithubRepo           string                    `json:"githubRepo"`
	ArtifactName         string                    `json:"artifactName"`
	Enabled              bool                      `json:"enabled"`
	CreatedAt            time.Time                 `json:"createdAt"`
	UpdatedAt            time.Time                 `json:"updatedAt"`
	LastDeployTag        string                    `json:"lastDeployTag,omitempty"`
	LastDeployStatus     string                    `json:"lastDeployStatus,omitempty"`
	LastDeployTime       *time.Time                `json:"lastDeployTime,omitempty"`
	LastJobID            string                    `json:"lastJobId,omitempty"`
	LastJobStatus        string                    `json:"lastJobStatus,omitempty"`
	LastDownloadBytes    int64                     `json:"lastDownloadBytes,omitempty"`
	LastDownloadSpeedBPS float64                   `json:"lastDownloadSpeedBps,omitempty"`
	LiveProgress         *adminAPIProgressResponse `json:"liveProgress,omitempty"`
}

type adminAPIProgressResponse struct {
	AppID           string    `json:"appId"`
	JobID           string    `json:"jobId"`
	Tag             string    `json:"tag"`
	Phase           string    `json:"phase"`
	DownloadedBytes int64     `json:"downloadedBytes"`
	TotalBytes      int64     `json:"totalBytes"`
	SpeedBPS        float64   `json:"speedBps"`
	Percent         float64   `json:"percent"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type adminAPIJobResponse struct {
	ID                 string                    `json:"id"`
	AppID              string                    `json:"appId"`
	AppName            string                    `json:"appName,omitempty"`
	AppEnabled         bool                      `json:"appEnabled"`
	Tag                string                    `json:"tag"`
	Status             string                    `json:"status"`
	Trigger            string                    `json:"trigger"`
	RetryOfJobID       string                    `json:"retryOfJobId,omitempty"`
	ErrorMsg           string                    `json:"errorMsg,omitempty"`
	DownloadBytes      int64                     `json:"downloadBytes"`
	DownloadDurationMs int64                     `json:"downloadDurationMs"`
	DownloadSpeedBPS   float64                   `json:"downloadSpeedBps"`
	CreatedAt          time.Time                 `json:"createdAt"`
	UpdatedAt          time.Time                 `json:"updatedAt"`
	LiveProgress       *adminAPIProgressResponse `json:"liveProgress,omitempty"`
}

type adminAPICancelJobResponse struct {
	JobID         string               `json:"jobId"`
	AppID         string               `json:"appId,omitempty"`
	PreviousState string               `json:"previousState,omitempty"`
	Status        string               `json:"status"`
	Outcome       cancel.CancelOutcome `json:"outcome"`
	Message       string               `json:"message,omitempty"`
}

type adminAPICancelAppResponse struct {
	AppID           string                      `json:"appId"`
	Total           int                         `json:"total"`
	Pending         int                         `json:"pending"`
	Active          int                         `json:"active"`
	Terminal        int                         `json:"terminal"`
	Unknown         int                         `json:"unknown"`
	PendingCanceled int                         `json:"pendingCanceled"`
	ActiveSignaled  int                         `json:"activeSignaled"`
	AlreadyTerminal int                         `json:"alreadyTerminal"`
	Requested       []adminAPICancelJobResponse `json:"requested"`
}

func (h *AdminAPIHandler) jobResponses(app *store.App, jobs []store.JobRecord) []adminAPIJobResponse {
	resp := make([]adminAPIJobResponse, 0, len(jobs))
	for _, job := range jobs {
		resp = append(resp, jobResponse(app, job, h.tracker))
	}
	return resp
}

func (h *AdminAPIHandler) updateAppWithOptionalSecret(id string, body adminAPIAppRequest, enabled bool) error {
	return h.store.UpdateWithOptionalSecret(id, body.Name, body.webhookSecret(), body.BinaryPath, body.ServiceName, body.GithubRepo, body.ArtifactName, enabled)
}

func isAdminAPINotFoundError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

func validateAdminAPIAppRequest(body adminAPIAppRequest, requireSecret bool) []string {
	var errs []string
	if body.Name == "" {
		errs = append(errs, "Name is required")
	}
	if body.BinaryPath == "" {
		errs = append(errs, "Binary Path is required")
	}
	if body.ServiceName == "" {
		errs = append(errs, "Service Name is required")
	}
	if body.GithubRepo == "" {
		errs = append(errs, "GitHub Repo is required")
	}
	if body.ArtifactName == "" {
		errs = append(errs, "Artifact Name is required")
	}
	if requireSecret && body.webhookSecret() == "" {
		errs = append(errs, "Webhook Secret is required")
	}
	return errs
}

func appWithLastDeployResponse(app store.AppWithLastDeploy) adminAPIAppResponse {
	resp := appResponse(app.App)
	resp.LastDeployTag = app.LastDeployTag
	resp.LastDeployStatus = app.LastDeployStatus
	resp.LastDeployTime = app.LastDeployTime
	resp.LastJobID = app.LastJobID
	resp.LastJobStatus = app.LastJobStatus
	resp.LastDownloadBytes = app.LastDownloadBytes
	resp.LastDownloadSpeedBPS = app.LastDownloadSpeedBPS
	resp.LiveProgress = progressResponse(app.LiveProgress)
	return resp
}

func appResponse(app store.App) adminAPIAppResponse {
	return adminAPIAppResponse{
		ID:           app.ID,
		Name:         app.Name,
		BinaryPath:   app.BinaryPath,
		ServiceName:  app.ServiceName,
		GithubRepo:   app.GithubRepo,
		ArtifactName: app.ArtifactName,
		Enabled:      app.Enabled,
		CreatedAt:    app.CreatedAt,
		UpdatedAt:    app.UpdatedAt,
	}
}

func jobResponse(app *store.App, job store.JobRecord, tracker *progress.ProgressTracker) adminAPIJobResponse {
	resp := adminAPIJobResponse{
		ID:                 job.ID,
		AppID:              job.AppID,
		Tag:                job.Tag,
		Status:             job.Status,
		Trigger:            job.Trigger,
		RetryOfJobID:       job.RetryOfJobID,
		ErrorMsg:           job.ErrorMsg,
		DownloadBytes:      job.DownloadBytes,
		DownloadDurationMs: job.DownloadDurationMs,
		DownloadSpeedBPS:   job.DownloadSpeedBPS,
		CreatedAt:          job.CreatedAt,
		UpdatedAt:          job.UpdatedAt,
	}
	if app != nil {
		resp.AppID = app.ID
		resp.AppName = app.Name
		resp.AppEnabled = app.Enabled
		if snapshot, ok := activeSnapshotForHistoryJob(tracker, app.ID, job); ok {
			resp.LiveProgress = progressResponse(snapshot)
		}
	}
	return resp
}

func progressResponse(snapshot *progress.ProgressSnapshot) *adminAPIProgressResponse {
	if snapshot == nil {
		return nil
	}
	return &adminAPIProgressResponse{
		AppID:           snapshot.AppID,
		JobID:           snapshot.JobID,
		Tag:             snapshot.Tag,
		Phase:           snapshot.Phase,
		DownloadedBytes: snapshot.DownloadedBytes,
		TotalBytes:      snapshot.TotalBytes,
		SpeedBPS:        snapshot.SpeedBPS,
		Percent:         snapshot.Percent,
		UpdatedAt:       snapshot.UpdatedAt,
	}
}

func cancelJobResponse(result cancel.CancelJobResult) adminAPICancelJobResponse {
	return adminAPICancelJobResponse{
		JobID:         result.JobID,
		AppID:         result.AppID,
		PreviousState: result.PreviousState,
		Status:        result.Status,
		Outcome:       result.Outcome,
		Message:       result.Message,
	}
}

func cancelAppResponse(result cancel.CancelAppResult) adminAPICancelAppResponse {
	resp := adminAPICancelAppResponse{
		AppID:           result.AppID,
		Total:           result.Total,
		Pending:         result.Pending,
		Active:          result.Active,
		Terminal:        result.Terminal,
		Unknown:         result.Unknown,
		PendingCanceled: result.PendingCanceled,
		ActiveSignaled:  result.ActiveSignaled,
		AlreadyTerminal: result.AlreadyTerminal,
	}
	resp.Requested = make([]adminAPICancelJobResponse, 0, len(result.Requested))
	for _, item := range result.Requested {
		resp.Requested = append(resp.Requested, cancelJobResponse(item))
	}
	return resp
}

func (h *AdminAPIHandler) ListAppsHertz(ctx context.Context, c *app.RequestContext) {
	apps, err := h.store.ListWithLastDeploy()
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to list apps")
		return
	}

	resp := make([]adminAPIAppResponse, 0, len(apps))
	for i := range apps {
		if h.tracker != nil {
			if snapshot, ok := activeSnapshotForApp(h.tracker, apps[i]); ok {
				apps[i].LiveProgress = snapshot
			}
		}
		resp = append(resp, appWithLastDeployResponse(apps[i]))
	}

	c.JSON(consts.StatusOK, map[string]any{"apps": resp})
}

func (h *AdminAPIHandler) CreateAppHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	var body adminAPIAppRequest
	if !decodeAdminAPIJSONHertz(c, &body) {
		return
	}

	if errs := validateAdminAPIAppRequest(body, true); len(errs) > 0 {
		writeAdminAPIValidationErrorHertz(c, errs)
		return
	}

	app, err := h.store.Create(body.Name, body.webhookSecret(), body.BinaryPath, body.ServiceName, body.GithubRepo, body.ArtifactName)
	if err != nil {
		if errors.Is(err, store.ErrDuplicateApp) {
			writeAdminAPIValidationErrorHertz(c, []string{"An app with this binary path, service name, or webhook secret already exists"})
			return
		}
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Internal error creating app")
		return
	}

	c.JSON(consts.StatusCreated, map[string]any{"status": "created", "app": appResponse(*app)})
}

func (h *AdminAPIHandler) GetAppHertz(ctx context.Context, c *app.RequestContext) {
	app, err := h.store.Get(c.Param("id"))
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "store.App not found")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"app": appResponse(*app)})
}

func (h *AdminAPIHandler) UpdateAppHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	id := c.Param("id")
	app, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "store.App not found")
		return
	}

	var body adminAPIAppRequest
	if !decodeAdminAPIJSONHertz(c, &body) {
		return
	}

	if errs := validateAdminAPIAppRequest(body, false); len(errs) > 0 {
		writeAdminAPIValidationErrorHertz(c, errs)
		return
	}

	enabled := app.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	if err := h.updateAppWithOptionalSecret(id, body, enabled); err != nil {
		if errors.Is(err, store.ErrDuplicateApp) {
			writeAdminAPIValidationErrorHertz(c, []string{"An app with this binary path, service name, or webhook secret already exists"})
			return
		}
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Internal error updating app")
		return
	}

	updated, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to load updated app")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"status": "updated", "app": appResponse(*updated)})
}

func (h *AdminAPIHandler) DeleteAppHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	if err := h.store.Delete(c.Param("id")); err != nil {
		if errors.Is(err, store.ErrActiveDeployExists) {
			writeAdminAPIErrorHertz(c, consts.StatusConflict, "Cannot delete app with active deploy")
			return
		}
		if isAdminAPINotFoundError(err) {
			writeAdminAPIErrorHertz(c, consts.StatusNotFound, "store.App not found")
			return
		}
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to delete app")
		return
	}
	c.JSON(consts.StatusOK, adminAPIStatusResponse{Status: "deleted", Message: "store.App deleted successfully"})
}

func (h *AdminAPIHandler) ToggleAppHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	id := c.Param("id")
	app, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "store.App not found")
		return
	}

	enabled := !app.Enabled
	if c.Request.Body() != nil && c.Request.Header.ContentLength() > 0 {
		var body adminAPIToggleRequest
		if !decodeAdminAPIJSONHertz(c, &body) {
			return
		}
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
	}

	if err := h.store.SetEnabled(id, enabled); err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to update app status")
		return
	}
	updated, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to load updated app")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"status": "updated", "app": appResponse(*updated)})
}

func (h *AdminAPIHandler) ManualDeployAppHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	id := c.Param("id")
	app, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "store.App not found")
		return
	}
	if !app.Enabled {
		writeAdminAPIErrorHertz(c, consts.StatusBadRequest, "Cannot deploy disabled app")
		return
	}

	var body adminAPIDeployRequest
	if !decodeAdminAPIJSONHertz(c, &body) {
		return
	}
	if body.Tag == "" {
		writeAdminAPIValidationErrorHertz(c, []string{"Tag is required"})
		return
	}

	if err := h.queue.EnqueueManual(id, body.Tag); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeAdminAPIErrorHertz(c, consts.StatusConflict, "Deploy already queued for this tag")
			return
		}
		if errors.Is(err, store.ErrQueueFull) {
			writeAdminAPIErrorHertz(c, consts.StatusServiceUnavailable, "Queue is full")
			return
		}
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to queue deploy: "+err.Error())
		return
	}

	c.JSON(consts.StatusAccepted, map[string]string{"status": "queued", "tag": body.Tag})
}

func (h *AdminAPIHandler) ListHistoryHertz(ctx context.Context, c *app.RequestContext) {
	if appID := c.Query("appId"); appID != "" {
		app, err := h.store.Get(appID)
		if err != nil {
			writeAdminAPIErrorHertz(c, consts.StatusNotFound, "store.App not found")
			return
		}
		jobs, err := h.queue.ListHistory(app.ID, 50)
		if err != nil {
			writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to list history")
			return
		}
		c.JSON(consts.StatusOK, map[string]any{"app": appResponse(*app), "history": h.jobResponses(app, jobs)})
		return
	}

	apps, err := h.store.List()
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to list apps")
		return
	}
	var history []adminAPIJobResponse
	for i := range apps {
		jobs, err := h.queue.ListHistory(apps[i].ID, 50)
		if err != nil {
			writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to list history")
			return
		}
		app := apps[i]
		history = append(history, h.jobResponses(&app, jobs)...)
	}
	c.JSON(consts.StatusOK, map[string]any{"history": history})
}

func (h *AdminAPIHandler) RetryHistoryJobHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	jobID := c.Param("id")
	job, err := h.queue.GetJob(jobID)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "Job not found")
		return
	}
	app, err := h.store.Get(job.AppID)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "store.App not found")
		return
	}
	if !app.Enabled {
		writeAdminAPIErrorHertz(c, consts.StatusBadRequest, "Cannot retry disabled app")
		return
	}

	retryJobID, err := h.queue.CreateRetryJob(jobID, app.ID, job.Tag)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeAdminAPIErrorHertz(c, consts.StatusConflict, "Deploy already pending for this tag")
			return
		}
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to create retry job: "+err.Error())
		return
	}

	c.JSON(consts.StatusAccepted, map[string]string{"status": "queued", "jobId": retryJobID})
}

func (h *AdminAPIHandler) CancelJobHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	result, err := h.cancel.RequestJobCancel(c.Param("job_id"))
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"status": "ok", "result": cancelJobResponse(result)})
}

func (h *AdminAPIHandler) CancelAppHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	appID := c.Param("app_id")
	if _, err := h.store.Get(appID); err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "store.App not found")
		return
	}
	result, err := h.cancel.RequestAppCancel(appID)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"status": "ok", "result": cancelAppResponse(result)})
}

func (h *AdminAPIHandler) NotFoundHertz(ctx context.Context, c *app.RequestContext) {
	writeAdminAPIErrorHertz(c, consts.StatusNotFound, "Not found")
}

func requireAdminAPIJSONRequestHertz(c *app.RequestContext) bool {
	contentType := string(c.GetHeader("Content-Type"))
	if strings.HasPrefix(contentType, "application/json") {
		return true
	}
	writeAdminAPIErrorHertz(c, consts.StatusUnsupportedMediaType, "Content-Type must be application/json")
	return false
}

func decodeAdminAPIJSONHertz(c *app.RequestContext, dst any) bool {
	if err := c.BindJSON(dst); err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusBadRequest, "Invalid JSON body")
		return false
	}
	return true
}

func writeAdminAPIValidationErrorHertz(c *app.RequestContext, errs []string) {
	c.JSON(consts.StatusBadRequest, adminAPIErrorResponse{Status: "error", Error: "Validation failed", Errors: errs})
}

func writeAdminAPIErrorHertz(c *app.RequestContext, status int, msg string) {
	c.JSON(status, adminAPIErrorResponse{Status: "error", Error: msg})
}

func RegisterAdminAPIRoutesHertz(h *server.Hertz, handler *AdminAPIHandler, auth app.HandlerFunc) {
	api := h.Group("/admin/api", auth)
	api.GET("/apps", handler.ListAppsHertz)
	api.POST("/apps", handler.CreateAppHertz)
	api.GET("/apps/:id", handler.GetAppHertz)
	api.PUT("/apps/:id", handler.UpdateAppHertz)
	api.DELETE("/apps/:id", handler.DeleteAppHertz)
	api.POST("/apps/:id/toggle", handler.ToggleAppHertz)
	api.POST("/apps/:id/deploy", handler.ManualDeployAppHertz)
	api.GET("/history", handler.ListHistoryHertz)
	api.POST("/history/:id/retry", handler.RetryHistoryJobHertz)
	api.POST("/jobs/:job_id/cancel", handler.CancelJobHertz)
	api.POST("/apps/:app_id/cancel", handler.CancelAppHertz)

	notFound := handler.NotFoundHertz
	h.GET("/admin/api", auth, notFound)
	h.POST("/admin/api", auth, notFound)
	h.PUT("/admin/api", auth, notFound)
	h.PATCH("/admin/api", auth, notFound)
	h.DELETE("/admin/api", auth, notFound)
	h.GET("/admin/api/*path", auth, notFound)
	h.POST("/admin/api/*path", auth, notFound)
	h.PUT("/admin/api/*path", auth, notFound)
	h.PATCH("/admin/api/*path", auth, notFound)
	h.DELETE("/admin/api/*path", auth, notFound)
}
