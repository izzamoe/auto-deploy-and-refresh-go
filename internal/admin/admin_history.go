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

type HistoryAdminHandler struct {
	store     *store.AppStore
	queue     *store.DeployQueue
	templates map[string]*template.Template
	tracker   *progress.ProgressTracker
}

func NewHistoryAdminHandler(store *store.AppStore, queue *store.DeployQueue, templates map[string]*template.Template, tracker *progress.ProgressTracker) *HistoryAdminHandler {
	return &HistoryAdminHandler{
		store:     store,
		queue:     queue,
		templates: templates,
		tracker:   tracker,
	}
}

type historyData struct {
	App          *store.App
	Rows         []historyRowData
	Flash        string
	FlashMessage string
	FlashIsError bool
}

type historyRowData struct {
	AppEnabled   bool
	Job          store.JobRecord
	LiveProgress *progress.ProgressSnapshot
}

var errHistoryAppNotFound = errors.New("history app not found")

func (h *HistoryAdminHandler) loadHistoryData(id, flash string, flashIsError bool) (historyData, error) {
	app, err := h.store.Get(id)
	if err != nil {
		return historyData{}, errHistoryAppNotFound
	}

	jobs, err := h.queue.ListHistory(id, 50)
	if err != nil {
		return historyData{}, err
	}

	rows := make([]historyRowData, 0, len(jobs))
	if h.tracker != nil {
		for _, job := range jobs {
			var snap *progress.ProgressSnapshot
			if snapVal, ok := activeSnapshotForHistoryJob(h.tracker, app.ID, job); ok {
				snap = snapVal
			}
			rows = append(rows, historyRowData{AppEnabled: app.Enabled, Job: job, LiveProgress: snap})
		}
	} else {
		for _, job := range jobs {
			rows = append(rows, historyRowData{AppEnabled: app.Enabled, Job: job})
		}
	}

	if flash != "" && !flashIsError {
		flashLower := strings.ToLower(flash)
		flashIsError = strings.Contains(flashLower, "failed") || strings.Contains(flashLower, "error") || strings.Contains(flashLower, "cannot")
	}

	return historyData{
		App:          app,
		Rows:         rows,
		Flash:        flash,
		FlashMessage: flash,
		FlashIsError: flashIsError,
	}, nil
}

func historyLocation(id, flash string, flashIsError bool) string {
	values := url.Values{}
	if flash != "" {
		values.Set("flash", flash)
	}
	if flashIsError {
		values.Set("flash_error", "1")
	}
	location := "/admin/apps/" + id + "/history"
	if encoded := values.Encode(); encoded != "" {
		location += "?" + encoded
	}
	return location
}

func activeSnapshotForHistoryJob(tracker *progress.ProgressTracker, appID string, job store.JobRecord) (*progress.ProgressSnapshot, bool) {
	if tracker == nil || !isActiveDeployStatus(job.Status) {
		return nil, false
	}
	snapshot, ok := tracker.Snapshot(appID)
	if !ok || snapshot.JobID != job.ID {
		return nil, false
	}
	return snapshot, true
}

func (h *HistoryAdminHandler) HistoryHandlerHertz(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	data, err := h.loadHistoryData(id, c.Query("flash"), c.Query("flash_error") == "1")
	if err != nil {
		if errors.Is(err, errHistoryAppNotFound) {
			c.String(http.StatusNotFound, "App not found")
			return
		}
		c.String(http.StatusInternalServerError, "Failed to list history")
		return
	}

	if err := renderAdminTemplateHertz(c, h.templates["history.html"], data); err != nil {
		c.String(http.StatusInternalServerError, err.Error())
	}
}

func (h *HistoryAdminHandler) RetryHandlerHertz(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	app, err := h.store.Get(id)
	if err != nil {
		c.String(http.StatusNotFound, "App not found")
		return
	}

	if !app.Enabled {
		h.respondRetryResultHertz(c, id, "Cannot retry disabled app", true)
		return
	}

	jobID := c.Param("jobid")
	job, err := h.queue.GetJob(jobID)
	if err != nil {
		c.String(http.StatusNotFound, "Job not found")
		return
	}
	if job.AppID != app.ID {
		c.String(http.StatusNotFound, "Job not found")
		return
	}

	_, err = h.queue.CreateRetryJob(jobID, app.ID, job.Tag)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			h.respondRetryResultHertz(c, id, "Deploy already pending for this tag", true)
			return
		}
		c.String(http.StatusInternalServerError, "Failed to create retry job: "+err.Error())
		return
	}

	h.respondRetryResultHertz(c, id, "Retry queued", false)
}

func (h *HistoryAdminHandler) respondRetryResultHertz(c *app.RequestContext, id, flash string, flashIsError bool) {
	if !isAdminUIRequestHertz(c) {
		adminUINavigateHertz(c, historyLocation(id, flash, flashIsError))
		return
	}

	data, err := h.loadHistoryData(id, flash, flashIsError)
	if err != nil {
		if errors.Is(err, errHistoryAppNotFound) {
			c.String(http.StatusNotFound, "App not found")
			return
		}
		c.String(http.StatusInternalServerError, "Failed to list history")
		return
	}

	if err := h.templates["history.html"].ExecuteTemplate(c.Response.BodyWriter(), "flash", data); err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.templates["history.html"].ExecuteTemplate(c.Response.BodyWriter(), "history_table_region", data); err != nil {
		c.String(http.StatusInternalServerError, err.Error())
	}
}

func RegisterAdminHistoryRoutesHertz(hz *server.Hertz, handler *HistoryAdminHandler, auth app.HandlerFunc) {
	hz.GET("/admin/apps/:id/history", auth, handler.HistoryHandlerHertz)
	RegisterAdminHistoryActionRoutesHertz(hz, handler, auth)
}

func RegisterAdminHistoryActionRoutesHertz(hz *server.Hertz, handler *HistoryAdminHandler, auth app.HandlerFunc) {
	hz.POST("/admin/apps/:id/retry/:jobid", auth, handler.RetryHandlerHertz)
}
