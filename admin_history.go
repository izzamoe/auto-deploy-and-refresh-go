package main

import (
	"errors"
	"html/template"
	"net/http"
	"strings"
)

type HistoryAdminHandler struct {
	store     *AppStore
	queue     *DeployQueue
	templates map[string]*template.Template
}

func NewHistoryAdminHandler(store *AppStore, queue *DeployQueue, templates map[string]*template.Template) *HistoryAdminHandler {
	return &HistoryAdminHandler{
		store:     store,
		queue:     queue,
		templates: templates,
	}
}

type historyData struct {
	App          *App
	Jobs         []JobRecord
	Flash        string
	FlashMessage string
	FlashIsError bool
}

func (h *HistoryAdminHandler) HistoryHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	app, err := h.store.Get(id)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	jobs, err := h.queue.ListHistory(id, 50)
	if err != nil {
		http.Error(w, "Failed to list history", http.StatusInternalServerError)
		return
	}

	flash := r.URL.Query().Get("flash")
	data := historyData{
		App:          app,
		Jobs:         jobs,
		Flash:        flash,
		FlashMessage: flash,
		FlashIsError: strings.Contains(strings.ToLower(flash), "failed") || strings.Contains(strings.ToLower(flash), "error") || strings.Contains(strings.ToLower(flash), "cannot"),
	}

	if err := h.templates["history.html"].ExecuteTemplate(w, "base.html", data); err != nil {
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
		http.Redirect(w, r, "/admin/apps/"+id+"/history?flash=Cannot+retry+disabled+app", http.StatusSeeOther)
		return
	}

	jobID := r.PathValue("jobid")
	job, err := h.queue.GetJob(jobID)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	_, err = h.queue.CreateRetryJob(jobID, app.ID, job.Tag)
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			http.Redirect(w, r, "/admin/apps/"+id+"/history?flash=Deploy+already+pending+for+this+tag", http.StatusSeeOther)
			return
		}
		http.Error(w, "Failed to create retry job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/apps/"+id+"/history?flash=Retry+queued", http.StatusSeeOther)
}

func RegisterAdminHistoryRoutes(mux *http.ServeMux, handler *HistoryAdminHandler, middleware func(http.Handler) http.Handler) {
	mux.Handle("GET /admin/apps/{id}/history", middleware(http.HandlerFunc(handler.HistoryHandler)))
	mux.Handle("POST /admin/apps/{id}/retry/{jobid}", middleware(http.HandlerFunc(handler.RetryHandler)))
}
