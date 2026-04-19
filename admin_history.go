package main

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

type HistoryAdminHandler struct {
	store     *AppStore
	queue     *DeployQueue
	templates map[string]*template.Template
	tracker   *ProgressTracker
}

func NewHistoryAdminHandler(store *AppStore, queue *DeployQueue, templates map[string]*template.Template, tracker *ProgressTracker) *HistoryAdminHandler {
	return &HistoryAdminHandler{
		store:     store,
		queue:     queue,
		templates: templates,
		tracker:   tracker,
	}
}

type historyData struct {
	App          *App
	Rows         []historyRowData
	ProgressStreamURL string
	Flash        string
	FlashMessage string
	FlashIsError bool
}

type historyRowData struct {
	AppEnabled   bool
	Job          JobRecord
	LiveProgress *ProgressSnapshot
}

func (h *HistoryAdminHandler) HistoryHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	data, err := h.loadHistoryData(id, r.URL.Query().Get("flash"), r.URL.Query().Get("flash_error") == "1")
	if err != nil {
		if errors.Is(err, errHistoryAppNotFound) {
			http.Error(w, "App not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to list history", http.StatusInternalServerError)
		return
	}

	if err := renderAdminTemplate(w, r, h.templates["history.html"], data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *HistoryAdminHandler) RetryHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	app, err := h.store.Get(id)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	if !app.Enabled {
		h.respondRetryResult(w, r, id, "Cannot retry disabled app", true)
		return
	}

	jobID := r.PathValue("jobid")
	job, err := h.queue.GetJob(jobID)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	if job.AppID != app.ID {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	_, err = h.queue.CreateRetryJob(jobID, app.ID, job.Tag)
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			h.respondRetryResult(w, r, id, "Deploy already pending for this tag", true)
			return
		}
		http.Error(w, "Failed to create retry job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.respondRetryResult(w, r, id, "Retry queued", false)
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
	activeJobIDs := make([]string, 0)
	if h.tracker != nil {
		for _, job := range jobs {
			var snap *ProgressSnapshot
			if isActiveDeployStatus(job.Status) {
				activeJobIDs = append(activeJobIDs, job.ID)
			}
			if snapVal, ok := activeSnapshotForHistoryJob(h.tracker, app.ID, job); ok {
				snap = snapVal
			}
			rows = append(rows, historyRowData{AppEnabled: app.Enabled, Job: job, LiveProgress: snap})
		}
	} else {
		for _, job := range jobs {
			if isActiveDeployStatus(job.Status) {
				activeJobIDs = append(activeJobIDs, job.ID)
			}
			rows = append(rows, historyRowData{AppEnabled: app.Enabled, Job: job})
		}
	}

	if flash != "" && !flashIsError {
		flashLower := strings.ToLower(flash)
		flashIsError = strings.Contains(flashLower, "failed") || strings.Contains(flashLower, "error") || strings.Contains(flashLower, "cannot")
	}

	return historyData{
		App:               app,
		Rows:              rows,
		ProgressStreamURL: buildProgressStreamURL(activeHistoryAppIDs(app.ID, activeJobIDs), activeJobIDs),
		Flash:             flash,
		FlashMessage:      flash,
		FlashIsError:      flashIsError,
	}, nil
}

func (h *HistoryAdminHandler) respondRetryResult(w http.ResponseWriter, r *http.Request, id, flash string, flashIsError bool) {
	if !isHTMXRequest(r) {
		adminNavigate(w, r, historyLocation(id, flash, flashIsError))
		return
	}

	data, err := h.loadHistoryData(id, flash, flashIsError)
	if err != nil {
		if errors.Is(err, errHistoryAppNotFound) {
			http.Error(w, "App not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to list history", http.StatusInternalServerError)
		return
	}

	if err := h.templates["history.html"].ExecuteTemplate(w, "flash", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.templates["history.html"].ExecuteTemplate(w, "history_progress_subscription_oob", progressSubscriptionState{URL: data.ProgressStreamURL}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.templates["history.html"].ExecuteTemplate(w, "history_table_region", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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

func activeSnapshotForHistoryJob(tracker *ProgressTracker, appID string, job JobRecord) (*ProgressSnapshot, bool) {
	if tracker == nil || !isActiveDeployStatus(job.Status) {
		return nil, false
	}
	snapshot, ok := tracker.Snapshot(appID)
	if !ok || snapshot.JobID != job.ID {
		return nil, false
	}
	return snapshot, true
}

func activeHistoryAppIDs(appID string, activeJobIDs []string) []string {
	if appID == "" || len(activeJobIDs) == 0 {
		return nil
	}
	return []string{appID}
}

func RegisterAdminHistoryRoutes(mux *http.ServeMux, handler *HistoryAdminHandler, middleware func(http.Handler) http.Handler) {
	mux.Handle("GET /admin/apps/{id}/history", middleware(http.HandlerFunc(handler.HistoryHandler)))
	mux.Handle("POST /admin/apps/{id}/retry/{jobid}", middleware(http.HandlerFunc(handler.RetryHandler)))
}
