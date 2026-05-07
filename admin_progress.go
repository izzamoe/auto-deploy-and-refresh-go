package main

import (
	"context"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/coder/websocket"
)

type ProgressAdminHandler struct {
	tracker   *ProgressTracker
	store     *AppStore
	queue     *DeployQueue
	templates map[string]*template.Template
}

func NewProgressAdminHandler(tracker *ProgressTracker, store *AppStore, queue *DeployQueue, templates map[string]*template.Template) *ProgressAdminHandler {
	return &ProgressAdminHandler{tracker: tracker, store: store, queue: queue, templates: templates}
}

func (h *ProgressAdminHandler) ProgressWebSocket(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()

	ctx := c.CloseRead(context.Background())
	lastFrames := make(map[string]string)
	if !h.writeMatchingProgressFrames(ctx, c, r, lastFrames) {
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !h.writeMatchingProgressFrames(ctx, c, r, lastFrames) {
				return
			}
		}
	}
}

func (h *ProgressAdminHandler) writeMatchingProgressFrames(ctx context.Context, c *websocket.Conn, r *http.Request, lastFrames map[string]string) bool {
	for _, snap := range h.matchingProgressSnapshots(r) {
		frame, err := progressSnapshotFrame(snap)
		if err != nil {
			continue
		}
		key := snap.AppID + "\x00" + snap.JobID
		if lastFrames[key] == frame {
			continue
		}
		writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = c.Write(writeCtx, websocket.MessageText, []byte(frame))
		cancel()
		if err != nil {
			return false
		}
		lastFrames[key] = frame
	}
	return true
}

func (h *ProgressAdminHandler) matchingProgressSnapshots(r *http.Request) []ProgressSnapshot {
	appIDs := uniqueSorted(r.URL.Query()["app_id"])
	jobIDs := uniqueSorted(r.URL.Query()["job_id"])
	appFilter := makeStringSet(appIDs)
	jobFilter := makeStringSet(jobIDs)

	snapshots := h.tracker.SnapshotAll()
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].AppID == snapshots[j].AppID {
			return snapshots[i].JobID < snapshots[j].JobID
		}
		return snapshots[i].AppID < snapshots[j].AppID
	})

	matched := make([]ProgressSnapshot, 0, len(snapshots))
	for _, snap := range snapshots {
		if len(appFilter) > 0 {
			if _, ok := appFilter[snap.AppID]; !ok {
				continue
			}
		}
		if len(jobFilter) > 0 {
			if _, ok := jobFilter[snap.JobID]; !ok {
				continue
			}
		}
		matched = append(matched, snap)
	}
	return matched
}

func progressSnapshotFrame(snap ProgressSnapshot) (string, error) {
	return EncodeProgressFrame(ProgressFrame{
		AppID:           snap.AppID,
		JobID:           snap.JobID,
		Tag:             snap.Tag,
		Stage:           snap.Phase,
		Status:          progressStatusForStage(snap.Phase),
		Percent:         progressPercentInt(snap.Percent),
		DownloadedBytes: snap.DownloadedBytes,
		TotalBytes:      snap.TotalBytes,
		SpeedBPS:        int64(snap.SpeedBPS),
	})
}

func progressStatusForStage(stage string) string {
	switch stage {
	case ProgressStageQueued:
		return ProgressStatusPending
	case ProgressStageSucceeded:
		return ProgressStatusSucceeded
	case ProgressStageFailed:
		return ProgressStatusFailed
	default:
		return ProgressStatusInProgress
	}
}

func progressPercentInt(percent float64) int64 {
	if percent < 0 {
		return -1
	}
	if percent > 100 {
		return 100
	}
	return int64(percent)
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

func makeStringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func (h *ProgressAdminHandler) ProgressWebSocketHertz(ctx context.Context, c *app.RequestContext) {
	adaptor.HertzHandler(http.HandlerFunc(h.ProgressWebSocket))(ctx, c)
}

func RegisterAdminProgressRoutes(mux *http.ServeMux, handler *ProgressAdminHandler, middleware func(http.Handler) http.Handler) {
	mux.Handle("GET /admin/progress/ws", middleware(http.HandlerFunc(handler.ProgressWebSocket)))
}

func RegisterAdminProgressRoutesHertz(h *server.Hertz, handler *ProgressAdminHandler, auth app.HandlerFunc) {
	h.GET("/admin/progress/ws", auth, handler.ProgressWebSocketHertz)
}
