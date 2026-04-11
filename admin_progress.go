package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type ProgressAdminHandler struct {
	tracker *ProgressTracker
}

func NewProgressAdminHandler(tracker *ProgressTracker) *ProgressAdminHandler {
	return &ProgressAdminHandler{tracker: tracker}
}

func (h *ProgressAdminHandler) StreamProgress(w http.ResponseWriter, r *http.Request) {
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

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			snapshots := h.tracker.SnapshotAll()
			if snapshots == nil {
				snapshots = []ProgressSnapshot{}
			}
			data, err := json.Marshal(snapshots)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func RegisterAdminProgressRoutes(mux *http.ServeMux, handler *ProgressAdminHandler, middleware func(http.Handler) http.Handler) {
	mux.Handle("GET /admin/progress/stream", middleware(http.HandlerFunc(handler.StreamProgress)))
}
