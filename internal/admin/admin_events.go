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
	"github.com/izzamoe/auto-deploy/internal/events"
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
	sessions      SessionChecker
	// bus carries protocol 3 (topics, seq/epoch, resume); protocol 2 keeps clients below.
	bus *events.Bus

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
		bus:           events.NewBus(),
		clients:       make(map[*adminEventClient]struct{}),
	}
}

// SetServerVersion overrides the version reported in the hello event (default:
// the build version stamped into llmstxt.Version by the linker).
func (h *AdminEventHub) SetServerVersion(v string) { h.serverVersion = v }

const adminEventHeartbeatInterval = 30 * time.Second

// adminEventSessionCheckInterval is how often an open socket re-checks its session row
// (events-ws.md close codes: every 60 s plus the in-process revocation signal).
var adminEventSessionCheckInterval = 60 * time.Second

// SessionChecker is what an open socket needs from the session manager to notice
// that its session ended: a re-check of the session row and a revocation signal.
type SessionChecker interface {
	Recheck(sessionID, userID string, tokenVersion int) error
	Subscribe() (<-chan struct{}, func())
}

// SetSessionChecker enables 4401 closes for session-authenticated sockets (B12).
func (h *AdminEventHub) SetSessionChecker(sc SessionChecker) { h.sessions = sc }

// EventsWebSocket upgrades the request. The origin is verified by the default of
// coder/websocket (no OriginPatterns, no InsecureSkipVerify): a browser page on
// another origin cannot open this socket with the admin's cookie (K4, FR-012).
func (h *AdminEventHub) EventsWebSocket(w http.ResponseWriter, r *http.Request) {
	h.serveEvents(w, r, nil, events.Principal{}, nil)
}

// Close codes of events-ws.md section 5.
const (
	closeSessionEnded        websocket.StatusCode = 4401
	closeProtocolUnsupported websocket.StatusCode = 4409
)

// eventSession identifies the session a socket was opened with, so the hub can notice
// when it ends. Nil for API-token principals (no session to expire).
type eventSession struct {
	sessionID    string
	userID       string
	tokenVersion int
	expiresAt    time.Time
}

// serveEvents is EventsWebSocket plus what only the caller knows: when the signed-in
// session ends (hello.sessionExpiresAt) and any extra response headers for the
// handshake (the deprecation notice of the pre-v1 path).
func (h *AdminEventHub) serveEvents(w http.ResponseWriter, r *http.Request, sess *eventSession, principal events.Principal, extra http.Header) {
	var sessionExpiresAt *time.Time
	if sess != nil && !sess.expiresAt.IsZero() {
		t := sess.expiresAt.UTC()
		sessionExpiresAt = &t
	}
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

	// FR-019 / FR-025 / RV-14: ?protocol=3 is the topic protocol of 002; ?protocol=2 and no
	// parameter keep the 001 protocol for one release (Q-11); anything else gets 4409
	// (reload banner).
	if q, ok := r.URL.Query()["protocol"]; ok {
		switch {
		case len(q) == 1 && q[0] == adminEventProtocolV3:
			h.serveV3(c, sess, principal, sessionExpiresAt)
			return
		case len(q) == 1 && q[0] == adminEventProtocolVersion:
		default:
			_ = c.Close(closeProtocolUnsupported, "unsupported protocol")
			return
		}
	}

	client := h.addClient()
	defer h.removeClient(client)

	ctx := c.CloseRead(context.Background())
	if !h.writeInitialEvents(ctx, c, sessionExpiresAt) {
		return
	}

	heartbeat := time.NewTicker(adminEventHeartbeatInterval)
	defer heartbeat.Stop()

	// B12 / FR-025: a socket must not outlive its session. Re-check the session row
	// periodically and on the in-process revocation signal.
	var recheck <-chan time.Time
	var revoked <-chan struct{}
	if sess != nil && h.sessions != nil {
		t := time.NewTicker(adminEventSessionCheckInterval)
		defer t.Stop()
		recheck = t.C
		ch, unsubscribe := h.sessions.Subscribe()
		defer unsubscribe()
		revoked = ch
	}
	sessionOK := func() bool {
		return h.sessions.Recheck(sess.sessionID, sess.userID, sess.tokenVersion) == nil
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-recheck:
			if !sessionOK() {
				_ = c.Close(closeSessionEnded, "session ended")
				return
			}
		case <-revoked:
			if !sessionOK() {
				_ = c.Close(closeSessionEnded, "session ended")
				return
			}
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
	// Protocol 3: only to sockets subscribed to jobs with progress on (FR-015).
	h.bus.PublishProgress(snapshot.JobID, func() []byte {
		raw, err := h.encodeEvent(AdminEvent{Protocol: adminEventProtocolV3, Type: adminEventTypeProgress, AppID: snapshot.AppID, JobID: snapshot.JobID, Payload: progressEventFrame(snapshot)})
		if err != nil {
			return nil
		}
		return raw
	})
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

	snapshot, err := h.encodeEvent(AdminEvent{Type: adminEventTypeSnapshot, Payload: adminEventSnapshotPayload{Jobs: h.snapshotJobs()}})
	if err != nil {
		return false
	}
	return writeAdminEventPayload(ctx, c, snapshot)
}

// adminEventSnapshotPayload is the payload of the second event of every connection
// (events-ws.md 3.2).
type adminEventSnapshotPayload struct {
	Jobs []adminEventSnapshotJob `json:"jobs"`
}

// adminEventSnapshotJob is one active deployment. status is a JobStatus (B52: the
// tracker phase must never be shown as the job status); percent/totalBytes are -1
// while the size is unknown, like the OpenAPI Progress schema.
type adminEventSnapshotJob struct {
	AppID           string    `json:"appId"`
	JobID           string    `json:"jobId"`
	Tag             string    `json:"tag"`
	Status          string    `json:"status"`
	Phase           string    `json:"phase"`
	Percent         int64     `json:"percent"`
	DownloadedBytes int64     `json:"downloadedBytes"`
	TotalBytes      int64     `json:"totalBytes"`
	SpeedBPS        int64     `json:"speedBps"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// snapshotJobs lists the ACTIVE deployments only: entries that reached a terminal
// phase are not re-sent (FR-033).
func (h *AdminEventHub) snapshotJobs() []adminEventSnapshotJob {
	jobs := make([]adminEventSnapshotJob, 0)
	for _, snap := range h.snapshotAll() {
		if snap.Phase == progress.ProgressStageSucceeded || snap.Phase == progress.ProgressStageFailed {
			continue
		}
		jobs = append(jobs, adminEventSnapshotJob{
			AppID:           snap.AppID,
			JobID:           snap.JobID,
			Tag:             snap.Tag,
			Status:          progressStatusForStage(snap.Phase),
			Phase:           snap.Phase,
			Percent:         progressPercentInt(snap.Percent),
			DownloadedBytes: snap.DownloadedBytes,
			TotalBytes:      snap.TotalBytes,
			SpeedBPS:        int64(snap.SpeedBPS),
			UpdatedAt:       snap.UpdatedAt.UTC(),
		})
	}
	return jobs
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
	var sess *eventSession
	var principal events.Principal
	if p, ok := auth.PrincipalFrom(c); ok {
		principal = events.Principal{Kind: p.Kind, UserID: p.UserID, SessionID: p.SessionID}
		if p.Kind == auth.KindSession {
			sess = &eventSession{sessionID: p.SessionID, userID: p.UserID, tokenVersion: p.TokenVersion, expiresAt: p.ExpiresAt}
		}
	}
	var extra http.Header
	if path := string(c.Request.URI().Path()); path == adminEventsLegacyPath {
		extra = http.Header{
			"Deprecation": {"true"},
			"Link":        {"<" + adminEventsV1Path + `>; rel="successor-version"`},
		}
	}
	adaptor.HertzHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.serveEvents(w, r, sess, principal, extra)
	}))(ctx, c)
}

const (
	adminEventsV1Path     = "/admin/events/v1/ws"
	adminEventsLegacyPath = "/admin/events/ws"
)

// RegisterAdminEventRoutesHertz mounts the event socket at /admin/events/v1/ws and, for
// ONE release, at the pre-v1 path /admin/events/ws with a Deprecation notice (Q-7).
func RegisterAdminEventRoutesHertz(h *server.Hertz, hub *AdminEventHub, auth app.HandlerFunc) {
	h.GET("/admin/events/v1/ws", auth, hub.EventsWebSocketHertz)
	h.GET("/admin/events/ws", auth, deprecatedAlias, hub.EventsWebSocketHertz)
}

func (h *AdminEventHub) BroadcastCancelEvent(event cancel.CancelEvent) {
	h.Broadcast(AdminEvent{Type: event.Type, AppID: event.AppID, JobID: event.JobID, Payload: event.Payload})
}

func progressStatusForStage(stage string) string {
	switch stage {
	case progress.ProgressStageQueued:
		return progress.ProgressStatusPending
	case progress.ProgressStageSucceeded:
		return progress.ProgressStatusSucceeded
	case progress.ProgressStageFailed:
		return progress.ProgressStatusFailed
	default:
		return progress.ProgressStatusInProgress
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

// ---------------------------------------------------------------------------------------
// Protocol 3 (specs/002 contracts/events-ws.v3.md): topics, client ops, resume.

const adminEventProtocolV3 = "3"

// Frame types of the protocol-3 control channel (events-ws.v3.md 2.1).
const (
	adminEventTypeSubscribed   = "subscribed"
	adminEventTypeUnsubscribed = "unsubscribed"
	adminEventTypeResumed      = "resumed"
	adminEventTypeResync       = "resync"
	adminEventTypeError        = "error"
)

type adminEventResumedPayload struct {
	Ref      string `json:"ref"`
	FromSeq  uint64 `json:"fromSeq"`
	ToSeq    uint64 `json:"toSeq"`
	Replayed int    `json:"replayed"`
}

type adminEventResyncPayload struct {
	Ref    string         `json:"ref,omitempty"`
	Topics []events.Topic `json:"topics"`
	Reason string         `json:"reason"`
}

// adminEventV3 is a server frame of protocol 3 that the hub encodes itself (hello, control
// frames, heartbeat). State events with a seq are encoded by the bus.
type adminEventV3 struct {
	Protocol  string    `json:"protocol"`
	ID        string    `json:"id"`
	Topic     string    `json:"topic,omitempty"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"ts"`
	Payload   any       `json:"payload,omitempty"`
}

type adminEventLimits struct {
	MaxTopics     int `json:"maxTopics"`
	MaxOpsPer10s  int `json:"maxOpsPer10s"`
	MaxFrameBytes int `json:"maxFrameBytes"`
}

type adminEventHelloV3Payload struct {
	ServerVersion    string           `json:"serverVersion"`
	SessionExpiresAt *time.Time       `json:"sessionExpiresAt,omitempty"`
	HeartbeatSeconds int              `json:"heartbeatSeconds"`
	Epoch            string           `json:"epoch"`
	Seq              uint64           `json:"seq"`
	ClientID         string           `json:"clientId"`
	Limits           adminEventLimits `json:"limits"`
}

type adminEventSubscribedTopic struct {
	Name  events.Topic `json:"name"`
	State any          `json:"state,omitempty"`
}

type adminEventSubscribedPayload struct {
	Ref    string                      `json:"ref"`
	Topics []adminEventSubscribedTopic `json:"topics"`
}

type adminEventUnsubscribedPayload struct {
	Ref    string         `json:"ref"`
	Topics []events.Topic `json:"topics"`
}

type adminEventErrorPayload struct {
	Ref   string           `json:"ref,omitempty"`
	Code  events.ErrorCode `json:"code"`
	Topic string           `json:"topic,omitempty"`
}

func (h *AdminEventHub) encodeV3(typ string, payload any) ([]byte, error) {
	return json.Marshal(adminEventV3{Protocol: adminEventProtocolV3, ID: h.newEventID(), Type: typ, Timestamp: h.now().UTC(), Payload: payload})
}

// v3Conn is one protocol-3 socket being served.
type v3Conn struct {
	h      *AdminEventHub
	c      *websocket.Conn
	ctx    context.Context
	client *events.Client
}

func (vc *v3Conn) write(payload []byte) bool { return writeAdminEventPayload(vc.ctx, vc.c, payload) }

func (vc *v3Conn) writeFrame(typ string, payload any) bool {
	raw, err := vc.h.encodeV3(typ, payload)
	if err != nil {
		return false
	}
	return vc.write(raw)
}

// serveV3 runs one protocol-3 socket: hello, then a loop that writes the client's queue,
// heartbeats and session checks, and handles the client's ops read by a second goroutine.
func (h *AdminEventHub) serveV3(c *websocket.Conn, sess *eventSession, principal events.Principal, sessionExpiresAt *time.Time) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := &v3Conn{h: h, c: c, ctx: ctx, client: events.NewClient(principal)}
	h.bus.Register(vc.client)
	defer h.bus.Unregister(vc.client)

	// hello.seq: a later sub with resume{epoch, seq: hello.seq} replays every state event
	// published after this point (events-ws.v3.md section 5).
	if !vc.writeFrame(adminEventTypeHello, adminEventHelloV3Payload{
		ServerVersion:    h.serverVersion,
		SessionExpiresAt: sessionExpiresAt,
		HeartbeatSeconds: int(adminEventHeartbeatInterval / time.Second),
		Epoch:            h.bus.Epoch(),
		Seq:              h.bus.Seq(),
		ClientID:         vc.client.ID,
		Limits:           adminEventLimits{MaxTopics: events.MaxTopics, MaxOpsPer10s: events.MaxOpsPer10s, MaxFrameBytes: events.MaxFrameBytes},
	}) {
		return
	}

	// The read loop: client ops are text frames of at most MaxFrameBytes (events-ws.v3.md
	// section 4). It hands every frame to the writer loop, which handles it in order.
	c.SetReadLimit(events.MaxFrameBytes)
	incoming := make(chan []byte)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				data = nil
			}
			select {
			case incoming <- data:
			case <-ctx.Done():
				return
			}
		}
	}()

	heartbeat := time.NewTicker(adminEventHeartbeatInterval)
	defer heartbeat.Stop()
	var recheck <-chan time.Time
	var revoked <-chan struct{}
	if sess != nil && h.sessions != nil {
		t := time.NewTicker(adminEventSessionCheckInterval)
		defer t.Stop()
		recheck = t.C
		ch, unsubscribe := h.sessions.Subscribe()
		defer unsubscribe()
		revoked = ch
	}
	sessionOK := func() bool {
		return h.sessions.Recheck(sess.sessionID, sess.userID, sess.tokenVersion) == nil
	}

	for {
		select {
		case <-readDone:
			return
		case <-recheck:
			if !sessionOK() {
				_ = c.Close(closeSessionEnded, "session ended")
				return
			}
		case <-revoked:
			if !sessionOK() {
				_ = c.Close(closeSessionEnded, "session ended")
				return
			}
		case <-heartbeat.C:
			if !vc.writeFrame(adminEventTypeHeartbeat, nil) {
				return
			}
		case data := <-incoming:
			if !vc.handleOp(data) {
				return
			}
		case payload := <-vc.client.Queue():
			if !vc.write(payload) {
				return
			}
		}
	}
}

// handleOp applies one client frame and writes the replies; false ends the connection.
func (vc *v3Conn) handleOp(data []byte) bool {
	op, perr := events.ParseOp(data)
	if perr != nil {
		return vc.writeError(*perr)
	}
	for _, e := range op.Errors {
		if !vc.writeError(e) {
			return false
		}
	}
	switch op.Kind {
	case "sub":
		if len(op.Subs) == 0 {
			return true
		}
		return vc.subscribe(op)
	case "unsub":
		if len(op.Unsubs) == 0 {
			return true
		}
		vc.h.bus.Unsubscribe(vc.client, op.Unsubs)
		return vc.writeFrame(adminEventTypeUnsubscribed, adminEventUnsubscribedPayload{Ref: op.Ref, Topics: op.Unsubs})
	}
	return true
}

// subscribe applies a sub op and writes, in this order: an error per refused topic, the
// subscribed ack, the replayed events and resumed (or resync), then the jobs snapshot.
// Live events of the new topics are queued by the bus after the replay point, so they
// follow on the queue (events-ws.v3.md section 5).
func (vc *v3Conn) subscribe(op events.Op) bool {
	res := vc.h.bus.Subscribe(vc.client, op.Subs, op.Resume)
	for _, t := range res.Refused {
		if !vc.writeError(events.OpError{Ref: op.Ref, Code: events.CodeForbiddenTopic, Topic: string(t)}) {
			return false
		}
	}
	if len(res.Accepted) == 0 {
		return true
	}
	topics := make([]adminEventSubscribedTopic, 0, len(res.Accepted))
	for _, s := range res.Accepted {
		topics = append(topics, adminEventSubscribedTopic{Name: s.Topic})
	}
	if !vc.writeFrame(adminEventTypeSubscribed, adminEventSubscribedPayload{Ref: op.Ref, Topics: topics}) {
		return false
	}
	for _, frame := range res.Replay {
		if !vc.write(frame) {
			return false
		}
	}
	switch {
	case res.Resumed:
		if !vc.writeFrame(adminEventTypeResumed, adminEventResumedPayload{Ref: op.Ref, FromSeq: res.FromSeq, ToSeq: res.ToSeq, Replayed: len(res.Replay)}) {
			return false
		}
	case res.Resync != "":
		if !vc.writeFrame(adminEventTypeResync, adminEventResyncPayload{Ref: op.Ref, Topics: res.ResyncTopics, Reason: res.Resync}) {
			return false
		}
	}
	for _, s := range res.Accepted {
		if s.Topic == events.TopicJobs && s.Opts.Progress {
			return vc.writeSnapshot()
		}
	}
	return true
}

// writeSnapshot sends the active deployments to this socket only (no seq: it is the state
// of the progress stream, not a bus event).
func (vc *v3Conn) writeSnapshot() bool {
	raw, err := json.Marshal(adminEventV3{
		Protocol: adminEventProtocolV3, ID: vc.h.newEventID(), Topic: string(events.TopicJobs), Type: adminEventTypeSnapshot,
		Timestamp: vc.h.now().UTC(), Payload: adminEventSnapshotPayload{Jobs: vc.h.snapshotJobs()},
	})
	if err != nil {
		return false
	}
	return vc.write(raw)
}

// PublishTopic publishes one protocol-3 event straight to the bus: a state event (seq,
// ring buffer) or, for logs:system, a stream frame. userID is set only for account events
// (FR-017). Unknown topics are dropped.
func (h *AdminEventHub) PublishTopic(topic, eventType, appID, jobID, userID string, payload any) {
	t, ok := events.LookupTopic(topic)
	if !ok {
		return
	}
	_, _ = h.bus.Publish(events.Event{Topic: t, Type: eventType, AppID: appID, JobID: jobID, UserID: userID, Payload: payload})
}

func (vc *v3Conn) writeError(e events.OpError) bool {
	return vc.writeFrame(adminEventTypeError, adminEventErrorPayload{Ref: e.Ref, Code: e.Code, Topic: e.Topic})
}
