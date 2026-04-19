package main

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"
)

type ProgressAdminHandler struct {
	tracker *ProgressTracker
	store   *AppStore
	queue   *DeployQueue
	templates map[string]*template.Template
}

func NewProgressAdminHandler(tracker *ProgressTracker, store *AppStore, queue *DeployQueue, templates map[string]*template.Template) *ProgressAdminHandler {
	return &ProgressAdminHandler{tracker: tracker, store: store, queue: queue, templates: templates}
}

func (h *ProgressAdminHandler) StreamProgress(w http.ResponseWriter, r *http.Request) {
	// SSE stays on the same auth contract as admin HTML routes: unauthorized
	// requests are rejected by middleware with 401 + WWW-Authenticate before the
	// stream starts, while authorized requests always receive an event stream.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	lastPayload := ""

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			payload, err := h.renderStreamPayload(r)
			if err != nil {
				continue
			}
			if payload == lastPayload {
				fmt.Fprint(w, ": keep-alive\n\n")
				flusher.Flush()
				continue
			}
			lastPayload = payload
			writeSSEEvent(w, "progress", payload)
			flusher.Flush()
		}
	}
}

func (h *ProgressAdminHandler) renderStreamPayload(r *http.Request) (string, error) {
	appIDs := uniqueSorted(r.URL.Query()["app_id"])
	jobIDs := uniqueSorted(r.URL.Query()["job_id"])
	if len(appIDs) == 0 && len(jobIDs) == 0 {
		for _, snap := range h.tracker.SnapshotAll() {
			appIDs = append(appIDs, snap.AppID)
			jobIDs = append(jobIDs, snap.JobID)
		}
		appIDs = uniqueSorted(appIDs)
		jobIDs = uniqueSorted(jobIDs)
	}

	var fragments bytes.Buffer
	for _, appID := range appIDs {
		app, err := h.loadAppWithProgress(appID)
		if err != nil || app == nil {
			continue
		}
		if err := h.templates["apps_list.html"].ExecuteTemplate(&fragments, "app_card_oob", app); err != nil {
			return "", err
		}
		historyData, err := h.loadHistoryData(appID)
		if err == nil && historyData != nil {
			if err := h.templates["history.html"].ExecuteTemplate(&fragments, "history_table_region_oob", historyData); err != nil {
				return "", err
			}
		}
	}

	state := progressStreamSubscriptionState(appIDs, jobIDs, h.store, h.queue)
	if err := h.templates["apps_list.html"].ExecuteTemplate(&fragments, "apps_progress_subscription_oob", state); err != nil {
		return "", err
	}
	if err := h.templates["history.html"].ExecuteTemplate(&fragments, "history_progress_subscription_oob", state); err != nil {
		return "", err
	}

	if fragments.Len() == 0 {
		return `<div></div>`, nil
	}
	return fragments.String(), nil
}

func (h *ProgressAdminHandler) loadHistoryData(appID string) (*historyData, error) {
	if h.store == nil || h.queue == nil {
		return nil, nil
	}
	historyHandler := &HistoryAdminHandler{store: h.store, queue: h.queue, tracker: h.tracker}
	data, err := historyHandler.loadHistoryData(appID, "", false)
	if err != nil {
		return nil, err
	}
	return &data, nil
}

type progressSubscriptionState struct {
	URL string
}

func progressStreamSubscriptionState(appIDs, jobIDs []string, store *AppStore, queue *DeployQueue) progressSubscriptionState {
	activeAppIDs := make([]string, 0)
	if store != nil {
		apps, err := store.ListWithLastDeploy()
		if err == nil {
			appIndex := make(map[string]AppWithLastDeploy, len(apps))
			for _, app := range apps {
				appIndex[app.ID] = app
			}
			for _, appID := range appIDs {
				app, ok := appIndex[appID]
				if ok && isActiveDeployStatus(app.LastDeployStatus) {
					activeAppIDs = append(activeAppIDs, appID)
				}
			}
		}
	}

	activeJobIDs := make([]string, 0)
	if queue != nil {
		for _, jobID := range jobIDs {
			job, err := queue.GetJob(jobID)
			if err == nil && isActiveDeployStatus(job.Status) {
				activeJobIDs = append(activeJobIDs, jobID)
			}
		}
	}

	return progressSubscriptionState{URL: buildProgressStreamURL(uniqueSorted(activeAppIDs), uniqueSorted(activeJobIDs))}
}

func (h *ProgressAdminHandler) loadAppWithProgress(appID string) (*AppWithLastDeploy, error) {
	if h.store == nil {
		return nil, nil
	}
	apps, err := h.store.ListWithLastDeploy()
	if err != nil {
		return nil, err
	}
	for i := range apps {
		if apps[i].ID != appID {
			continue
		}
		if snap, ok := activeSnapshotForApp(h.tracker, apps[i]); ok {
			apps[i].LiveProgress = snap
		}
		return &apps[i], nil
	}
	return nil, nil
}

func (h *ProgressAdminHandler) loadHistoryRow(jobID string) (*historyRowData, error) {
	if h.queue == nil || h.store == nil {
		return nil, nil
	}
	job, err := h.queue.GetJob(jobID)
	if err != nil {
		return nil, err
	}
	app, err := h.store.Get(job.AppID)
	if err != nil {
		return nil, err
	}
	row := &historyRowData{AppEnabled: app.Enabled, Job: *job}
	if snap, ok := activeSnapshotForHistoryJob(h.tracker, job.AppID, *job); ok {
		row.LiveProgress = snap
	}
	return row, nil
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func writeSSEEvent(w http.ResponseWriter, eventName, payload string) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		payload = `<div></div>`
	}
	fmt.Fprintf(w, "event: %s\n", eventName)
	for _, line := range strings.Split(payload, "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}

func RegisterAdminProgressRoutes(mux *http.ServeMux, handler *ProgressAdminHandler, middleware func(http.Handler) http.Handler) {
	mux.Handle("GET /admin/progress/stream", middleware(http.HandlerFunc(handler.StreamProgress)))
}
