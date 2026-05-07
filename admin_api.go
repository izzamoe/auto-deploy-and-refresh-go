package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type AdminAPIHandler struct {
	store   *AppStore
	queue   *DeployQueue
	tracker *ProgressTracker
	cancel  *CancelService
}

func NewAdminAPIHandler(store *AppStore, queue *DeployQueue, tracker *ProgressTracker, cancel *CancelService) *AdminAPIHandler {
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
	JobID         string        `json:"jobId"`
	AppID         string        `json:"appId,omitempty"`
	PreviousState string        `json:"previousState,omitempty"`
	Status        string        `json:"status"`
	Outcome       CancelOutcome `json:"outcome"`
	Message       string        `json:"message,omitempty"`
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

func (h *AdminAPIHandler) ListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.store.ListWithLastDeploy()
	if err != nil {
		writeAdminAPIError(w, http.StatusInternalServerError, "Failed to list apps")
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

	writeJSON(w, http.StatusOK, map[string]any{"apps": resp})
}

func (h *AdminAPIHandler) CreateApp(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAPIJSONRequest(w, r) {
		return
	}

	var body adminAPIAppRequest
	if !decodeAdminAPIJSON(w, r, &body) {
		return
	}

	if errs := validateAdminAPIAppRequest(body, true); len(errs) > 0 {
		writeAdminAPIValidationError(w, errs)
		return
	}

	app, err := h.store.Create(body.Name, body.webhookSecret(), body.BinaryPath, body.ServiceName, body.GithubRepo, body.ArtifactName)
	if err != nil {
		if errors.Is(err, ErrDuplicateApp) {
			writeAdminAPIValidationError(w, []string{"An app with this binary path, service name, or webhook secret already exists"})
			return
		}
		writeAdminAPIError(w, http.StatusInternalServerError, "Internal error creating app")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"status": "created", "app": appResponse(*app)})
}

func (h *AdminAPIHandler) GetApp(w http.ResponseWriter, r *http.Request) {
	app, err := h.store.Get(r.PathValue("id"))
	if err != nil {
		writeAdminAPIError(w, http.StatusNotFound, "App not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": appResponse(*app)})
}

func (h *AdminAPIHandler) UpdateApp(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAPIJSONRequest(w, r) {
		return
	}

	id := r.PathValue("id")
	app, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIError(w, http.StatusNotFound, "App not found")
		return
	}

	var body adminAPIAppRequest
	if !decodeAdminAPIJSON(w, r, &body) {
		return
	}

	if errs := validateAdminAPIAppRequest(body, false); len(errs) > 0 {
		writeAdminAPIValidationError(w, errs)
		return
	}

	enabled := app.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	if err := h.updateAppWithOptionalSecret(id, body, enabled); err != nil {
		if errors.Is(err, ErrDuplicateApp) {
			writeAdminAPIValidationError(w, []string{"An app with this binary path, service name, or webhook secret already exists"})
			return
		}
		writeAdminAPIError(w, http.StatusInternalServerError, "Internal error updating app")
		return
	}

	updated, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIError(w, http.StatusInternalServerError, "Failed to load updated app")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "app": appResponse(*updated)})
}

func (h *AdminAPIHandler) DeleteApp(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAPIJSONRequest(w, r) {
		return
	}

	if err := h.store.Delete(r.PathValue("id")); err != nil {
		if errors.Is(err, ErrActiveDeployExists) {
			writeAdminAPIError(w, http.StatusConflict, "Cannot delete app with active deploy")
			return
		}
		if isAdminAPINotFoundError(err) {
			writeAdminAPIError(w, http.StatusNotFound, "App not found")
			return
		}
		writeAdminAPIError(w, http.StatusInternalServerError, "Failed to delete app")
		return
	}
	writeJSON(w, http.StatusOK, adminAPIStatusResponse{Status: "deleted", Message: "App deleted successfully"})
}

func (h *AdminAPIHandler) ToggleApp(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAPIJSONRequest(w, r) {
		return
	}

	id := r.PathValue("id")
	app, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIError(w, http.StatusNotFound, "App not found")
		return
	}

	enabled := !app.Enabled
	if r.Body != nil && r.ContentLength != 0 {
		var body adminAPIToggleRequest
		if !decodeAdminAPIJSON(w, r, &body) {
			return
		}
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
	}

	if err := h.store.SetEnabled(id, enabled); err != nil {
		writeAdminAPIError(w, http.StatusInternalServerError, "Failed to update app status")
		return
	}
	updated, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIError(w, http.StatusInternalServerError, "Failed to load updated app")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "app": appResponse(*updated)})
}

func (h *AdminAPIHandler) ManualDeployApp(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAPIJSONRequest(w, r) {
		return
	}

	id := r.PathValue("id")
	app, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIError(w, http.StatusNotFound, "App not found")
		return
	}
	if !app.Enabled {
		writeAdminAPIError(w, http.StatusBadRequest, "Cannot deploy disabled app")
		return
	}

	var body adminAPIDeployRequest
	if !decodeAdminAPIJSON(w, r, &body) {
		return
	}
	if body.Tag == "" {
		writeAdminAPIValidationError(w, []string{"Tag is required"})
		return
	}

	if err := h.queue.EnqueueManual(id, body.Tag); err != nil {
		if errors.Is(err, ErrDuplicate) {
			writeAdminAPIError(w, http.StatusConflict, "Deploy already queued for this tag")
			return
		}
		if errors.Is(err, ErrQueueFull) {
			writeAdminAPIError(w, http.StatusServiceUnavailable, "Queue is full")
			return
		}
		writeAdminAPIError(w, http.StatusInternalServerError, "Failed to queue deploy: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "tag": body.Tag})
}

func (h *AdminAPIHandler) ListHistory(w http.ResponseWriter, r *http.Request) {
	if appID := r.URL.Query().Get("appId"); appID != "" {
		app, err := h.store.Get(appID)
		if err != nil {
			writeAdminAPIError(w, http.StatusNotFound, "App not found")
			return
		}
		jobs, err := h.queue.ListHistory(app.ID, 50)
		if err != nil {
			writeAdminAPIError(w, http.StatusInternalServerError, "Failed to list history")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"app": appResponse(*app), "history": h.jobResponses(app, jobs)})
		return
	}

	apps, err := h.store.List()
	if err != nil {
		writeAdminAPIError(w, http.StatusInternalServerError, "Failed to list apps")
		return
	}
	var history []adminAPIJobResponse
	for i := range apps {
		jobs, err := h.queue.ListHistory(apps[i].ID, 50)
		if err != nil {
			writeAdminAPIError(w, http.StatusInternalServerError, "Failed to list history")
			return
		}
		app := apps[i]
		history = append(history, h.jobResponses(&app, jobs)...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": history})
}

func (h *AdminAPIHandler) RetryHistoryJob(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAPIJSONRequest(w, r) {
		return
	}

	jobID := r.PathValue("id")
	job, err := h.queue.GetJob(jobID)
	if err != nil {
		writeAdminAPIError(w, http.StatusNotFound, "Job not found")
		return
	}
	app, err := h.store.Get(job.AppID)
	if err != nil {
		writeAdminAPIError(w, http.StatusNotFound, "App not found")
		return
	}
	if !app.Enabled {
		writeAdminAPIError(w, http.StatusBadRequest, "Cannot retry disabled app")
		return
	}

	retryJobID, err := h.queue.CreateRetryJob(jobID, app.ID, job.Tag)
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			writeAdminAPIError(w, http.StatusConflict, "Deploy already pending for this tag")
			return
		}
		writeAdminAPIError(w, http.StatusInternalServerError, "Failed to create retry job: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "jobId": retryJobID})
}

func (h *AdminAPIHandler) CancelJob(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAPIJSONRequest(w, r) {
		return
	}

	result, err := h.cancel.RequestJobCancel(r.PathValue("job_id"))
	if err != nil {
		writeAdminAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "result": cancelJobResponse(result)})
}

func (h *AdminAPIHandler) CancelApp(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAPIJSONRequest(w, r) {
		return
	}

	appID := r.PathValue("app_id")
	if _, err := h.store.Get(appID); err != nil {
		writeAdminAPIError(w, http.StatusNotFound, "App not found")
		return
	}
	result, err := h.cancel.RequestAppCancel(appID)
	if err != nil {
		writeAdminAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "result": cancelAppResponse(result)})
}

func (h *AdminAPIHandler) NotFound(w http.ResponseWriter, r *http.Request) {
	writeAdminAPIError(w, http.StatusNotFound, "Not found")
}

func (h *AdminAPIHandler) jobResponses(app *App, jobs []JobRecord) []adminAPIJobResponse {
	resp := make([]adminAPIJobResponse, 0, len(jobs))
	for _, job := range jobs {
		resp = append(resp, jobResponse(app, job, h.tracker))
	}
	return resp
}

func (h *AdminAPIHandler) updateAppWithOptionalSecret(id string, body adminAPIAppRequest, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	secret := body.webhookSecret()
	var result interface {
		RowsAffected() (int64, error)
	}
	var err error
	if secret == "" {
		result, err = h.store.db.Exec(
			`UPDATE apps SET name = ?, binary_path = ?, service_name = ?, github_repo = ?, artifact_name = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			body.Name, body.BinaryPath, body.ServiceName, body.GithubRepo, body.ArtifactName, enabledInt, id,
		)
	} else {
		result, err = h.store.db.Exec(
			`UPDATE apps SET name = ?, webhook_secret_hash = ?, binary_path = ?, service_name = ?, github_repo = ?, artifact_name = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			body.Name, HashSecret(secret), body.BinaryPath, body.ServiceName, body.GithubRepo, body.ArtifactName, enabledInt, id,
		)
	}
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateApp
		}
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errHistoryAppNotFound
	}
	return nil
}

func requireAdminAPIJSONRequest(w http.ResponseWriter, r *http.Request) bool {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/json") {
		return true
	}
	writeAdminAPIError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
	return false
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

func decodeAdminAPIJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		writeAdminAPIError(w, http.StatusBadRequest, "Invalid JSON body")
		return false
	}
	return true
}

func writeAdminAPIValidationError(w http.ResponseWriter, errs []string) {
	writeJSON(w, http.StatusBadRequest, adminAPIErrorResponse{Status: "error", Error: "Validation failed", Errors: errs})
}

func writeAdminAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, adminAPIErrorResponse{Status: "error", Error: msg})
}

func appWithLastDeployResponse(app AppWithLastDeploy) adminAPIAppResponse {
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

func appResponse(app App) adminAPIAppResponse {
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

func jobResponse(app *App, job JobRecord, tracker *ProgressTracker) adminAPIJobResponse {
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

func progressResponse(snapshot *ProgressSnapshot) *adminAPIProgressResponse {
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

func cancelJobResponse(result CancelJobResult) adminAPICancelJobResponse {
	return adminAPICancelJobResponse{
		JobID:         result.JobID,
		AppID:         result.AppID,
		PreviousState: result.PreviousState,
		Status:        result.Status,
		Outcome:       result.Outcome,
		Message:       result.Message,
	}
}

func cancelAppResponse(result CancelAppResult) adminAPICancelAppResponse {
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

func RegisterAdminAPIRoutes(mux *http.ServeMux, handler *AdminAPIHandler, middleware func(http.Handler) http.Handler) {
	adminAPINotFoundHandler := middleware(http.HandlerFunc(handler.NotFound))
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		mux.Handle(method+" /admin/api", adminAPINotFoundHandler)
		mux.Handle(method+" /admin/api/", adminAPINotFoundHandler)
	}
	mux.Handle("GET /admin/api/apps", middleware(http.HandlerFunc(handler.ListApps)))
	mux.Handle("POST /admin/api/apps", middleware(http.HandlerFunc(handler.CreateApp)))
	mux.Handle("GET /admin/api/apps/{id}", middleware(http.HandlerFunc(handler.GetApp)))
	mux.Handle("PUT /admin/api/apps/{id}", middleware(http.HandlerFunc(handler.UpdateApp)))
	mux.Handle("DELETE /admin/api/apps/{id}", middleware(http.HandlerFunc(handler.DeleteApp)))
	mux.Handle("POST /admin/api/apps/{id}/toggle", middleware(http.HandlerFunc(handler.ToggleApp)))
	mux.Handle("POST /admin/api/apps/{id}/deploy", middleware(http.HandlerFunc(handler.ManualDeployApp)))
	mux.Handle("GET /admin/api/history", middleware(http.HandlerFunc(handler.ListHistory)))
	mux.Handle("POST /admin/api/history/{id}/retry", middleware(http.HandlerFunc(handler.RetryHistoryJob)))
	mux.Handle("POST /admin/api/jobs/{job_id}/cancel", middleware(http.HandlerFunc(handler.CancelJob)))
	mux.Handle("POST /admin/api/apps/{app_id}/cancel", middleware(http.HandlerFunc(handler.CancelApp)))
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
		if errors.Is(err, ErrDuplicateApp) {
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
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
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
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
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
		if errors.Is(err, ErrDuplicateApp) {
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
		if errors.Is(err, ErrActiveDeployExists) {
			writeAdminAPIErrorHertz(c, consts.StatusConflict, "Cannot delete app with active deploy")
			return
		}
		if isAdminAPINotFoundError(err) {
			writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
			return
		}
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to delete app")
		return
	}
	c.JSON(consts.StatusOK, adminAPIStatusResponse{Status: "deleted", Message: "App deleted successfully"})
}

func (h *AdminAPIHandler) ToggleAppHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	id := c.Param("id")
	app, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
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
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
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
		if errors.Is(err, ErrDuplicate) {
			writeAdminAPIErrorHertz(c, consts.StatusConflict, "Deploy already queued for this tag")
			return
		}
		if errors.Is(err, ErrQueueFull) {
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
			writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
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
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
		return
	}
	if !app.Enabled {
		writeAdminAPIErrorHertz(c, consts.StatusBadRequest, "Cannot retry disabled app")
		return
	}

	retryJobID, err := h.queue.CreateRetryJob(jobID, app.ID, job.Tag)
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
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
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
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
