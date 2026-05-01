package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	adminEventProtocolVersion     = "v1"
	adminEventTypeHello           = "hello"
	adminEventTypeSnapshot        = "snapshot"
	adminEventTypeJobStatus       = "job_status"
	adminEventTypeCancelRequested = "cancel_requested"
	adminEventTypeNotice          = "notice"
	adminEventTypeHeartbeat       = "heartbeat"
	adminEventTypeProgress        = "progress"
	adminEventCompactProgress     = "p"

	adminEventClientBuffer = 8
	adminEventWriteTimeout = 2 * time.Second
)

type AdminEvent struct {
	Version   string    `json:"version"`
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	AppID     string    `json:"appId,omitempty"`
	JobID     string    `json:"jobId,omitempty"`
	Payload   any       `json:"payload,omitempty"`
}

type compactAdminProgressEvent struct {
	Version         string `json:"v"`
	Type            string `json:"t"`
	ID              string `json:"i"`
	Timestamp       string `json:"ts"`
	AppID           string `json:"a"`
	JobID           string `json:"j"`
	Phase           string `json:"ph"`
	Status          string `json:"st"`
	Percent         *int64 `json:"pct,omitempty"`
	DownloadedBytes int64  `json:"db"`
	TotalBytes      *int64 `json:"tb,omitempty"`
	SpeedBPS        int64  `json:"bps"`
	Message         string `json:"msg"`
}

type AdminEventHub struct {
	tracker *ProgressTracker
	now     func() time.Time
	nextID  atomic.Uint64

	mu      sync.Mutex
	clients map[*adminEventClient]struct{}
}

type adminEventClient struct {
	send      chan []byte
	closeOnce sync.Once
}

func NewAdminEventHub(tracker *ProgressTracker) *AdminEventHub {
	return &AdminEventHub{
		tracker: tracker,
		now:     time.Now,
		clients: make(map[*adminEventClient]struct{}),
	}
}

func (h *AdminEventHub) EventsWebSocket(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()

	client := h.addClient()
	defer h.removeClient(client)

	ctx := c.CloseRead(context.Background())
	if !h.writeInitialEvents(ctx, c) {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case payload, ok := <-client.send:
			if !ok {
				return
			}
			if !writeAdminEventPayload(ctx, c, payload) {
				return
			}
		}
	}
}

func (h *AdminEventHub) Broadcast(event AdminEvent) {
	payload, err := h.encodeEvent(event)
	if err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		select {
		case client.send <- payload:
		default:
			delete(h.clients, client)
			client.close()
		}
	}
}

func (h *AdminEventHub) PublishProgress(snapshot ProgressSnapshot) {
	h.Broadcast(AdminEvent{Type: adminEventTypeProgress, AppID: snapshot.AppID, JobID: snapshot.JobID, Payload: progressEventFrame(snapshot)})
}

func (h *AdminEventHub) addClient() *adminEventClient {
	client := &adminEventClient{send: make(chan []byte, adminEventClientBuffer)}
	h.mu.Lock()
	h.clients[client] = struct{}{}
	h.mu.Unlock()
	return client
}

func (h *AdminEventHub) removeClient(client *adminEventClient) {
	h.mu.Lock()
	delete(h.clients, client)
	h.mu.Unlock()
	client.close()
}

func (c *adminEventClient) close() {
	c.closeOnce.Do(func() {
		close(c.send)
	})
}

func (h *AdminEventHub) writeInitialEvents(ctx context.Context, c *websocket.Conn) bool {
	hello, err := h.encodeEvent(AdminEvent{Type: adminEventTypeHello, Payload: map[string]string{"protocol": adminEventProtocolVersion}})
	if err != nil || !writeAdminEventPayload(ctx, c, hello) {
		return false
	}

	snapshot, err := h.encodeEvent(AdminEvent{Type: adminEventTypeSnapshot, Payload: h.snapshotAll()})
	if err != nil {
		return false
	}
	return writeAdminEventPayload(ctx, c, snapshot)
}

func (h *AdminEventHub) snapshotAll() []ProgressSnapshot {
	snapshots := h.tracker.SnapshotAll()
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].AppID == snapshots[j].AppID {
			return snapshots[i].JobID < snapshots[j].JobID
		}
		return snapshots[i].AppID < snapshots[j].AppID
	})
	return snapshots
}

func (h *AdminEventHub) encodeEvent(event AdminEvent) ([]byte, error) {
	if event.Version == "" {
		event.Version = adminEventProtocolVersion
	}
	if event.ID == "" {
		event.ID = h.newEventID()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = h.now().UTC()
	}
	if event.Type == adminEventTypeProgress {
		return h.encodeProgressEvent(event)
	}
	return json.Marshal(event)
}

func (h *AdminEventHub) encodeProgressEvent(event AdminEvent) ([]byte, error) {
	frame, ok := adminEventProgressFrame(event.Payload)
	if !ok {
		return nil, fmt.Errorf("admin progress event payload must be ProgressFrame")
	}
	if frame.AppID == "" {
		frame.AppID = event.AppID
	}
	if frame.JobID == "" {
		frame.JobID = event.JobID
	}
	return json.Marshal(compactAdminProgressEvent{
		Version:         event.Version,
		Type:            adminEventCompactProgress,
		ID:              event.ID,
		Timestamp:       event.Timestamp.Format(time.RFC3339Nano),
		AppID:           frame.AppID,
		JobID:           frame.JobID,
		Phase:           frame.Stage,
		Status:          frame.Status,
		Percent:         compactProgressOptionalPercent(frame),
		DownloadedBytes: frame.DownloadedBytes,
		TotalBytes:      compactProgressOptionalTotal(frame),
		SpeedBPS:        frame.SpeedBPS,
		Message:         frame.Message,
	})
}

func adminEventProgressFrame(payload any) (ProgressFrame, bool) {
	switch frame := payload.(type) {
	case ProgressFrame:
		return frame, true
	case *ProgressFrame:
		if frame == nil {
			return ProgressFrame{}, false
		}
		return *frame, true
	default:
		return ProgressFrame{}, false
	}
}

func progressEventFrame(snapshot ProgressSnapshot) ProgressFrame {
	return ProgressFrame{
		AppID:           snapshot.AppID,
		JobID:           snapshot.JobID,
		Tag:             snapshot.Tag,
		Stage:           snapshot.Phase,
		Status:          progressStatusForStage(snapshot.Phase),
		Percent:         progressPercentInt(snapshot.Percent),
		DownloadedBytes: snapshot.DownloadedBytes,
		TotalBytes:      snapshot.TotalBytes,
		SpeedBPS:        int64(snapshot.SpeedBPS),
	}
}

func compactProgressOptionalTotal(frame ProgressFrame) *int64 {
	if frame.TotalBytes <= 0 {
		return nil
	}
	total := frame.TotalBytes
	return &total
}

func compactProgressOptionalPercent(frame ProgressFrame) *int64 {
	if frame.TotalBytes <= 0 || frame.Percent < 0 {
		return nil
	}
	pct := frame.Percent
	return &pct
}

func (h *AdminEventHub) newEventID() string {
	return fmt.Sprintf("evt-%d", h.nextID.Add(1))
}

func writeAdminEventPayload(ctx context.Context, c *websocket.Conn, payload []byte) bool {
	writeCtx, cancel := context.WithTimeout(ctx, adminEventWriteTimeout)
	err := c.Write(writeCtx, websocket.MessageText, payload)
	cancel()
	return err == nil
}

func RegisterAdminEventRoutes(mux *http.ServeMux, hub *AdminEventHub, middleware func(http.Handler) http.Handler) {
	mux.Handle("GET /admin/events/ws", middleware(http.HandlerFunc(hub.EventsWebSocket)))
}
