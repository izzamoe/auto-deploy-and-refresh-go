package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/llmstxt"
	"github.com/izzamoe/auto-deploy/internal/progress"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/coder/websocket"
)

const (
	adminEventProtocolVersion     = "2"
	adminEventTypeHello           = "hello"
	adminEventTypeSnapshot        = "snapshot"
	adminEventTypeJobStatus       = "job_status"
	adminEventTypeCancelRequested = "cancel_requested"
	adminEventTypeNotice          = "notice"
	adminEventTypeHeartbeat       = "heartbeat"
	adminEventTypeProgress        = "progress"
	adminEventCompactProgress     = "p"

	adminEventClientBuffer = 64
	adminEventWriteTimeout = 5 * time.Second
)

// AdminEvent is the envelope of events-ws.md section 2 (protocol "2"): jobId and
// appId are always top-level.
type AdminEvent struct {
	Protocol  string    `json:"protocol"`
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"ts"`
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

// adminEventHelloPayload is the payload of the first event of every connection
// (events-ws.md 3.1).
type adminEventHelloPayload struct {
	ServerVersion    string     `json:"serverVersion"`
	SessionExpiresAt *time.Time `json:"sessionExpiresAt,omitempty"`
	HeartbeatSeconds int        `json:"heartbeatSeconds"`
}

type AdminEventHub struct {
	tracker       *progress.ProgressTracker
	now           func() time.Time
	nextID        atomic.Uint64
	serverVersion string

	mu      sync.Mutex
	clients map[*adminEventClient]struct{}
}

type adminEventClient struct {
	send      chan []byte
	closeOnce sync.Once
}

func NewAdminEventHub(tracker *progress.ProgressTracker) *AdminEventHub {
	return &AdminEventHub{
		tracker:       tracker,
		now:           time.Now,
		serverVersion: llmstxt.Version,
		clients:       make(map[*adminEventClient]struct{}),
	}
}

// SetServerVersion overrides the version reported in the hello event (default:
// the build version stamped into llmstxt.Version by the linker).
func (h *AdminEventHub) SetServerVersion(v string) { h.serverVersion = v }

const adminEventHeartbeatInterval = 30 * time.Second

// EventsWebSocket upgrades the request. The origin is verified by the default of
// coder/websocket (no OriginPatterns, no InsecureSkipVerify): a browser page on
// another origin cannot open this socket with the admin's cookie (K4, FR-012).
func (h *AdminEventHub) EventsWebSocket(w http.ResponseWriter, r *http.Request) {
	h.serveEvents(w, r, nil, nil)
}

// serveEvents is EventsWebSocket plus what only the caller knows: when the signed-in
// session ends (hello.sessionExpiresAt) and any extra response headers for the
// handshake (the deprecation notice of the pre-v1 path).
func (h *AdminEventHub) serveEvents(w http.ResponseWriter, r *http.Request, sessionExpiresAt *time.Time, extra http.Header) {
	for k, vs := range extra {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()

	client := h.addClient()
	defer h.removeClient(client)

	ctx := c.CloseRead(context.Background())
	if !h.writeInitialEvents(ctx, c, sessionExpiresAt) {
		return
	}

	heartbeat := time.NewTicker(adminEventHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			hb, encErr := h.encodeEvent(AdminEvent{Type: adminEventTypeHeartbeat})
			if encErr != nil {
				return
			}
			if !writeAdminEventPayload(ctx, c, hb) {
				return
			}
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

func (h *AdminEventHub) PublishProgress(snapshot progress.ProgressSnapshot) {
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

func (h *AdminEventHub) writeInitialEvents(ctx context.Context, c *websocket.Conn, sessionExpiresAt *time.Time) bool {
	hello, err := h.encodeEvent(AdminEvent{Type: adminEventTypeHello, Payload: adminEventHelloPayload{
		ServerVersion:    h.serverVersion,
		SessionExpiresAt: sessionExpiresAt,
		HeartbeatSeconds: int(adminEventHeartbeatInterval / time.Second),
	}})
	if err != nil || !writeAdminEventPayload(ctx, c, hello) {
		return false
	}

	snapshot, err := h.encodeEvent(AdminEvent{Type: adminEventTypeSnapshot, Payload: h.snapshotAll()})
	if err != nil {
		return false
	}
	return writeAdminEventPayload(ctx, c, snapshot)
}

func (h *AdminEventHub) snapshotAll() []progress.ProgressSnapshot {
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
	if event.Protocol == "" {
		event.Protocol = adminEventProtocolVersion
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
		return nil, fmt.Errorf("admin progress event payload must be progress.ProgressFrame")
	}
	if frame.AppID == "" {
		frame.AppID = event.AppID
	}
	if frame.JobID == "" {
		frame.JobID = event.JobID
	}
	return json.Marshal(compactAdminProgressEvent{
		Version:         event.Protocol,
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

func adminEventProgressFrame(payload any) (progress.ProgressFrame, bool) {
	switch frame := payload.(type) {
	case progress.ProgressFrame:
		return frame, true
	case *progress.ProgressFrame:
		if frame == nil {
			return progress.ProgressFrame{}, false
		}
		return *frame, true
	default:
		return progress.ProgressFrame{}, false
	}
}

func progressEventFrame(snapshot progress.ProgressSnapshot) progress.ProgressFrame {
	return progress.ProgressFrame{
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

func compactProgressOptionalTotal(frame progress.ProgressFrame) *int64 {
	if frame.TotalBytes <= 0 {
		return nil
	}
	total := frame.TotalBytes
	return &total
}

func compactProgressOptionalPercent(frame progress.ProgressFrame) *int64 {
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

// EventsWebSocketHertz runs behind the auth middleware. A session principal tells
// the hello event when the session ends; an API-token principal has no session.
func (h *AdminEventHub) EventsWebSocketHertz(ctx context.Context, c *app.RequestContext) {
	var expires *time.Time
	if p, ok := auth.PrincipalFrom(c); ok && p.Kind == auth.KindSession && !p.ExpiresAt.IsZero() {
		t := p.ExpiresAt.UTC()
		expires = &t
	}
	var extra http.Header
	if path := string(c.Request.URI().Path()); path == adminEventsLegacyPath {
		extra = http.Header{
			"Deprecation": {"true"},
			"Link":        {"<" + adminEventsV1Path + `>; rel="successor-version"`},
		}
	}
	adaptor.HertzHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.serveEvents(w, r, expires, extra)
	}))(ctx, c)
}

const (
	adminEventsV1Path     = "/admin/events/v1/ws"
	adminEventsLegacyPath = "/admin/events/ws"
)

// RegisterAdminEventRoutesHertz mounts the event socket at /admin/events/v1/ws and, for
// ONE release, at the pre-v1 path /admin/events/ws with a Deprecation notice (Q-7).
func RegisterAdminEventRoutesHertz(h *server.Hertz, hub *AdminEventHub, auth app.HandlerFunc) {
	h.GET(adminEventsV1Path, auth, hub.EventsWebSocketHertz)
	h.GET(adminEventsLegacyPath, auth, hub.EventsWebSocketHertz)
}

func (h *AdminEventHub) BroadcastCancelEvent(event cancel.CancelEvent) {
	h.Broadcast(AdminEvent{Type: event.Type, AppID: event.AppID, JobID: event.JobID, Payload: event.Payload})
}
