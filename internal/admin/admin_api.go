package admin

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/deploy"
	"github.com/izzamoe/auto-deploy/internal/httpx"
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

// adminAPIErrorResponse is the one error envelope of the admin API (FR-021):
// {status:"error", code, error, errors:[{field?, code, message}], requestId}.
type adminAPIErrorResponse = httpx.APIError

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

// getAppOr404 loads an app by id, writing a 404 response and returning false
// when it does not exist.
func (h *AdminAPIHandler) getAppOr404(c *app.RequestContext, id string) (*store.App, bool) {
	found, err := h.store.Get(id)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
		return nil, false
	}
	return found, true
}

func isAdminAPINotFoundError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

// validateAdminAPIAppRequest checks an app create/update body and returns one
// field-level error per problem (errors[].field = the JSON name): required fields,
// then the FR-040 formats (binaryPath, serviceName, githubRepo, artifactName) and the
// Q-11 rule that an app must not point at the auto-deploy binary itself.
func validateAdminAPIAppRequest(body adminAPIAppRequest, requireSecret bool) []httpx.FieldError {
	var errs []httpx.FieldError
	required := func(field, value, message string) {
		if value == "" {
			errs = append(errs, httpx.FieldError{Field: field, Code: httpx.CodeValidationFailed, Message: message})
		}
	}
	required("name", body.Name, "Name is required")
	required("binaryPath", body.BinaryPath, "Binary Path is required")
	required("serviceName", body.ServiceName, "Service Name is required")
	required("githubRepo", body.GithubRepo, "GitHub Repo is required")
	required("artifactName", body.ArtifactName, "Artifact Name is required")
	if requireSecret {
		required("webhookSecret", body.webhookSecret(), "Webhook Secret is required")
	}
	for _, fe := range deploy.ValidateAppInput(deploy.AppInput{
		BinaryPath: body.BinaryPath, ServiceName: body.ServiceName, GithubRepo: body.GithubRepo, ArtifactName: body.ArtifactName,
	}) {
		errs = append(errs, httpx.FieldError{Field: fe.Field, Code: httpx.CodeValidationFailed, Message: fe.Message})
	}
	if body.BinaryPath != "" && !hasFieldError(errs, "binaryPath") {
		// No app is marked self-deploy yet (Q-19), so pointing at the manager's own
		// binary is always refused.
		if err := deploy.ValidateBinaryPathNotSelf(body.BinaryPath, deploy.OwnExecutable(), false); err != nil {
			errs = append(errs, httpx.FieldError{Field: "binaryPath", Code: httpx.CodeValidationFailed, Message: err.Error()})
		}
	}
	return errs
}

func hasFieldError(errs []httpx.FieldError, field string) bool {
	for _, e := range errs {
		if e.Field == field {
			return true
		}
	}
	return false
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
		writeAdminAPIFieldErrorsHertz(c, errs)
		return
	}

	app, err := h.store.Create(body.Name, body.webhookSecret(), body.BinaryPath, body.ServiceName, body.GithubRepo, body.ArtifactName)
	if err != nil {
		if errors.Is(err, store.ErrInvalidAppInput) {
			writeAdminAPIValidationErrorHertz(c, []string{"App fields are invalid"})
			return
		}
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
	app, ok := h.getAppOr404(c, c.Param("id"))
	if !ok {
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"app": appResponse(*app)})
}

func (h *AdminAPIHandler) UpdateAppHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	id := c.Param("id")
	app, ok := h.getAppOr404(c, id)
	if !ok {
		return
	}

	var body adminAPIAppRequest
	if !decodeAdminAPIJSONHertz(c, &body) {
		return
	}

	if errs := validateAdminAPIAppRequest(body, false); len(errs) > 0 {
		writeAdminAPIFieldErrorsHertz(c, errs)
		return
	}

	enabled := app.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	if err := h.updateAppWithOptionalSecret(id, body, enabled); err != nil {
		if errors.Is(err, store.ErrInvalidAppInput) {
			writeAdminAPIValidationErrorHertz(c, []string{"App fields are invalid"})
			return
		}
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

// DeleteAppHertz removes the app with its env vars, args and jobs in ONE
// transaction (B14, FR-035). ?removeUnitFile=true (Q-10, default false) also removes
// the unit file auto-deploy generated for it; a hand-written unit file is never
// removed, and a failure to remove the unit does not undo the deletion.
func (h *AdminAPIHandler) DeleteAppHertz(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	removeUnitFile := false
	if raw := string(c.Query("removeUnitFile")); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			writeAdminAPIValidationErrorHertz(c, []string{"removeUnitFile must be true or false"})
			return
		}
		removeUnitFile = v
	}
	// The service name must be read before the row is gone.
	serviceName := ""
	if removeUnitFile {
		if existing, err := h.store.Get(id); err == nil && existing != nil {
			serviceName = existing.ServiceName
		}
	}

	if err := h.store.Delete(id); err != nil {
		if errors.Is(err, store.ErrActiveDeployExists) {
			writeAdminAPIErrorHertz(c, consts.StatusConflict, "Cannot delete app with active deploy")
			return
		}
		if isAdminAPINotFoundError(err) {
			writeAdminAPIErrorHertz(c, consts.StatusNotFound, "App not found")
			return
		}
		logHandlerError(c, "delete app failed", err)
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to delete app")
		return
	}

	message := "App deleted successfully"
	if removeUnitFile && serviceName != "" {
		removed, err := deploy.RemoveServiceUnit(serviceName)
		switch {
		case err != nil:
			logHandlerError(c, "remove unit file after app delete failed", err)
			message += "; the unit file could not be removed"
		case removed:
			message += "; unit file removed"
		}
	}
	c.JSON(consts.StatusOK, adminAPIStatusResponse{Status: "deleted", Message: message})
}

func (h *AdminAPIHandler) ToggleAppHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	id := c.Param("id")
	app, ok := h.getAppOr404(c, id)
	if !ok {
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
	app, ok := h.getAppOr404(c, id)
	if !ok {
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
		if errors.Is(err, store.ErrInvalidTag) {
			// B15: a malformed tag is the caller's mistake (400 invalid_tag), never a 500,
			// and the validation detail is not echoed back.
			writeError(c, consts.StatusBadRequest, httpx.CodeInvalidTag, "Invalid tag")
			return
		}
		if errors.Is(err, store.ErrDuplicate) {
			writeAdminAPIErrorHertz(c, consts.StatusConflict, "Deploy already queued for this tag")
			return
		}
		if errors.Is(err, store.ErrQueueFull) {
			writeAdminAPIErrorHertz(c, consts.StatusServiceUnavailable, "Queue is full")
			return
		}
		logHandlerError(c, "queue manual deploy failed", err)
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to queue deploy")
		return
	}

	c.JSON(consts.StatusAccepted, map[string]string{"status": "queued", "tag": body.Tag})
}

// historyPageDefaults bound the page size the history endpoint will serve.
const (
	historyDefaultPageSize = 20
	historyMaxPageSize     = 100
)

// parseHistoryPaging reads the page/pageSize query params, applying defaults
// and clamping to sane bounds. Returns 1-based page, pageSize, and the SQL
// offset.
func parseHistoryPaging(c *app.RequestContext) (page, pageSize, offset int) {
	page = 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil && p > 1 {
		page = p
	}
	pageSize = historyDefaultPageSize
	if s, err := strconv.Atoi(c.Query("pageSize")); err == nil && s > 0 {
		pageSize = s
	}
	if pageSize > historyMaxPageSize {
		pageSize = historyMaxPageSize
	}
	offset = (page - 1) * pageSize
	return page, pageSize, offset
}

func (h *AdminAPIHandler) ListHistoryHertz(ctx context.Context, c *app.RequestContext) {
	page, pageSize, offset := parseHistoryPaging(c)

	if appID := c.Query("appId"); appID != "" {
		app, ok := h.getAppOr404(c, appID)
		if !ok {
			return
		}
		total, err := h.queue.CountHistory(app.ID)
		if err != nil {
			writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to list history")
			return
		}
		jobs, err := h.queue.ListHistoryPaged(app.ID, pageSize, offset)
		if err != nil {
			writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to list history")
			return
		}
		c.JSON(consts.StatusOK, map[string]any{
			"app":      appResponse(*app),
			"history":  h.jobResponses(app, jobs),
			"total":    total,
			"page":     page,
			"pageSize": pageSize,
		})
		return
	}

	apps, err := h.store.List()
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to list apps")
		return
	}
	appsByID := make(map[string]*store.App, len(apps))
	for i := range apps {
		appsByID[apps[i].ID] = &apps[i]
	}

	total, err := h.queue.CountAllHistory()
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to list history")
		return
	}
	jobs, err := h.queue.ListAllHistoryPaged(pageSize, offset)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to list history")
		return
	}
	history := make([]adminAPIJobResponse, 0, len(jobs))
	for _, job := range jobs {
		history = append(history, jobResponse(appsByID[job.AppID], job, h.tracker))
	}
	c.JSON(consts.StatusOK, map[string]any{
		"history":  history,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
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
	app, ok := h.getAppOr404(c, job.AppID)
	if !ok {
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
		logHandlerError(c, "create retry job failed", err)
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to create retry job")
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
		logHandlerError(c, "request job cancel failed", err)
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to request cancel")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"status": "ok", "result": cancelJobResponse(result)})
}

func (h *AdminAPIHandler) CancelAppHertz(ctx context.Context, c *app.RequestContext) {
	if !requireAdminAPIJSONRequestHertz(c) {
		return
	}

	appID := c.Param("app_id")
	if _, ok := h.getAppOr404(c, appID); !ok {
		return
	}
	result, err := h.cancel.RequestAppCancel(appID)
	if err != nil {
		logHandlerError(c, "request app cancel failed", err)
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to request cancel")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"status": "ok", "result": cancelAppResponse(result)})
}

func (h *AdminAPIHandler) NotFoundHertz(ctx context.Context, c *app.RequestContext) {
	writeError(c, consts.StatusNotFound, httpx.CodeNotFound, "Not found")
}

// logHandlerError records an internal failure with the request ID so the operator can
// correlate it with the generic error the client received.
func logHandlerError(c *app.RequestContext, what string, err error) {
	slog.Error(what, "err", err, "request_id", httpx.FromContext(c))
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
		writeError(c, consts.StatusBadRequest, httpx.CodeInvalidJSON, "Invalid JSON body")
		return false
	}
	return true
}

// writeError writes the contract error envelope. msg must be a safe, fixed message:
// never the text of an internal error (FR-022); log the detail with the request ID
// instead (TestNoRawErrorInResponses enforces this on the whole package).
func writeError(c *app.RequestContext, status int, code, msg string, fields ...httpx.FieldError) {
	httpx.WriteError(c, status, code, msg, fields...)
}

// codeForStatus is the default contract code of an HTTP status.
func codeForStatus(status int) string {
	switch status {
	case consts.StatusBadRequest:
		return httpx.CodeValidationFailed
	case consts.StatusUnauthorized:
		return httpx.CodeUnauthorized
	case consts.StatusForbidden:
		return httpx.CodeForbidden
	case consts.StatusNotFound:
		return httpx.CodeNotFound
	case consts.StatusMethodNotAllowed:
		return httpx.CodeMethodNotAllowed
	case consts.StatusConflict:
		return httpx.CodeDuplicate
	case consts.StatusRequestEntityTooLarge:
		return httpx.CodePayloadTooLarge
	case consts.StatusUnsupportedMediaType:
		return httpx.CodeUnsupportedMediaType
	case consts.StatusTooManyRequests:
		return httpx.CodeRateLimited
	case consts.StatusBadGateway:
		return httpx.CodeUpstreamError
	case consts.StatusServiceUnavailable:
		return httpx.CodeQueueFull
	}
	return httpx.CodeInternalError
}

func writeAdminAPIErrorHertz(c *app.RequestContext, status int, msg string) {
	writeError(c, status, codeForStatus(status), msg)
}

// writeAdminAPIFieldErrorsHertz answers 400 validation_failed with field-level
// entries (errors[].field names the invalid JSON field).
func writeAdminAPIFieldErrorsHertz(c *app.RequestContext, fields []httpx.FieldError) {
	writeError(c, consts.StatusBadRequest, httpx.CodeValidationFailed, "Validation failed", fields...)
}

// writeAdminAPIValidationErrorHertz answers 400 validation_failed; each message
// becomes one entry of errors[].
func writeAdminAPIValidationErrorHertz(c *app.RequestContext, errs []string) {
	fields := make([]httpx.FieldError, 0, len(errs))
	for _, e := range errs {
		fields = append(fields, httpx.FieldError{Code: httpx.CodeValidationFailed, Message: e})
	}
	writeError(c, consts.StatusBadRequest, httpx.CodeValidationFailed, "Validation failed", fields...)
}

// JobLogsHertz returns the service logs captured for a job when its deploy
// completed (used to review why a deploy failed its health check).
func (h *AdminAPIHandler) JobLogsHertz(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("job_id")
	logText, err := h.queue.GetJobLog(jobID)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to load job logs")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"log": logText})
}

func RegisterAdminAPIRoutesHertz(h *server.Hertz, handler *AdminAPIHandler, auth app.HandlerFunc) {
	api := h.Group("/admin/api/v1", auth)
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
	api.GET("/jobs/:job_id/logs", handler.JobLogsHertz)
	api.POST("/apps/:app_id/cancel", handler.CancelAppHertz)

	// Anything else under the API prefix is a JSON 404 (code not_found), never the SPA
	// HTML. The bare /admin/api prefix is the pre-v1 alias namespace (Q-7).
	notFound := handler.NotFoundHertz
	for _, prefix := range []string{"/admin/api/v1", "/admin/api"} {
		h.GET(prefix, auth, notFound)
		h.POST(prefix, auth, notFound)
		h.PUT(prefix, auth, notFound)
		h.PATCH(prefix, auth, notFound)
		h.DELETE(prefix, auth, notFound)
		h.GET(prefix+"/*path", auth, notFound)
		h.POST(prefix+"/*path", auth, notFound)
		h.PUT(prefix+"/*path", auth, notFound)
		h.PATCH(prefix+"/*path", auth, notFound)
		h.DELETE(prefix+"/*path", auth, notFound)
	}
}
