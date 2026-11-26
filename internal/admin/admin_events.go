package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/deploy"
	"github.com/izzamoe/auto-deploy/internal/events"
	"github.com/izzamoe/auto-deploy/internal/llmstxt"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"

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
	tokens        TokenChecker
	// bus carries protocol 3 (topics, seq/epoch, resume); protocol 2 keeps clients below.
	bus *events.Bus
	// apps is the app store of the protocol-3 sources (SetAppStore).
	apps atomic.Pointer[store.AppStore]
	// summary publishes topic summary when its state changes (SetDeployQueue).
	summary atomic.Pointer[events.Summary]
	// statsClock publishes stats.changed{window} while topic stats has subscribers.
	statsClock atomic.Pointer[events.StatsClock]
	// svc is the shared systemd watcher of topic service (Q-3=A), created on first use
	// from the app store; svcOpts are its interval and debounce.
	svcMu   sync.Mutex
	svc     events.ServiceWatcher
	svcOpts deploy.ServiceWatcherOptions
	// journal is the shared follower of topic logs:system (Q-5=A), nil until
	// SetSystemLogUnit/SetJournalFollower.
	journalMu sync.Mutex
	journal   events.JournalFollower

	mu      sync.Mutex
	clients map[*adminEventClient]struct{}
}

type adminEventClient struct {
	send      chan []byte
	closeOnce sync.Once
	// overflowed is set by Broadcast when send was full and the client was dropped; the
	// handler then closes the socket with 4408 instead of ending it silently (FR-013).
	overflowed atomic.Bool
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

// adminEventHeartbeatTick is the period of the protocol-3 heartbeat (and of its ping, CR-8);
// adminEventPingTimeout is how long a ping may wait for its pong. Tests shorten both.
var (
	adminEventHeartbeatTick = adminEventHeartbeatInterval
	adminEventPingTimeout   = 10 * time.Second
)

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

// TokenChecker is what a socket opened with an API token needs to notice that the token
// was revoked or removed (T089 CR-4): a re-check of the token row and a revocation signal.
type TokenChecker interface {
	RecheckToken(userID, tokenID string) error
	SubscribeTokens() (<-chan struct{}, func())
}

// SetTokenChecker enables 4401 closes for API-token sockets (T089 CR-4).
func (h *AdminEventHub) SetTokenChecker(tc TokenChecker) { h.tokens = tc }

// eventToken identifies the API token a socket was opened with.
type eventToken struct {
	userID  string
	tokenID string
}

// credentialWatch re-checks the credential of an open socket every
// adminEventSessionCheckInterval and on the in-process revocation signal: the session row
// (B12, FR-025) or the API-token row (T089 CR-4). Zero channels when nothing is checked.
type credentialWatch struct {
	tick   <-chan time.Time
	signal <-chan struct{}
	ok     func() bool
	stop   func()
}

func (h *AdminEventHub) watchCredential(sess *eventSession, tok *eventToken) credentialWatch {
	switch {
	case sess != nil && h.sessions != nil:
		t := time.NewTicker(adminEventSessionCheckInterval)
		ch, unsubscribe := h.sessions.Subscribe()
		return credentialWatch{tick: t.C, signal: ch, stop: func() { t.Stop(); unsubscribe() }, ok: func() bool {
			return h.sessions.Recheck(sess.sessionID, sess.userID, sess.tokenVersion) == nil
		}}
	case tok != nil && h.tokens != nil:
		t := time.NewTicker(adminEventSessionCheckInterval)
		ch, unsubscribe := h.tokens.SubscribeTokens()
		return credentialWatch{tick: t.C, signal: ch, stop: func() { t.Stop(); unsubscribe() }, ok: func() bool {
			return h.tokens.RecheckToken(tok.userID, tok.tokenID) == nil
		}}
	}
	return credentialWatch{ok: func() bool { return true }, stop: func() {}}
}

// EventsWebSocket upgrades the request. The origin is verified by the default of
// coder/websocket (no OriginPatterns, no InsecureSkipVerify): a browser page on
// another origin cannot open this socket with the admin's cookie (K4, FR-012).
func (h *AdminEventHub) EventsWebSocket(w http.ResponseWriter, r *http.Request) {
	h.serveEvents(w, r, nil, nil, events.Principal{}, nil)
}

// Close codes of events-ws.md section 5.
const (
	closeSessionEnded        websocket.StatusCode = 4401
	closeLagging             websocket.StatusCode = 4408 // 002: the client could not keep up
	closeAbuse               websocket.StatusCode = 4429 // 002: repeated op policy violations
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
func (h *AdminEventHub) serveEvents(w http.ResponseWriter, r *http.Request, sess *eventSession, tok *eventToken, principal events.Principal, extra http.Header) {
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
			h.serveV3(c, sess, tok, principal, sessionExpiresAt)
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

	// B12 / FR-025: a socket must not outlive its session (or, T089 CR-4, its API token).
	// Re-check the credential periodically and on the in-process revocation signal.
	cred := h.watchCredential(sess, tok)
	defer cred.stop()
	recheck, revoked, sessionOK := cred.tick, cred.signal, cred.ok

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
				if client.overflowed.Load() {
					_ = c.Close(closeLagging, "lagging")
				}
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
			client.overflowed.Store(true)
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
	var tok *eventToken
	var principal events.Principal
	if p, ok := auth.PrincipalFrom(c); ok {
		principal = events.Principal{Kind: p.Kind, UserID: p.UserID, SessionID: p.SessionID}
		switch p.Kind {
		case auth.KindSession:
			sess = &eventSession{sessionID: p.SessionID, userID: p.UserID, tokenVersion: p.TokenVersion, expiresAt: p.ExpiresAt}
		case auth.KindToken:
			tok = &eventToken{userID: p.UserID, tokenID: p.TokenID}
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
		h.serveEvents(w, r, sess, tok, principal, extra)
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
	h.publishJobsV3(event)
}

// publishJobsV3 maps the existing job sources (cancel service, coordinator job_status) to
// topic jobs of protocol 3 (FR-015). The protocol-2 copy of jobId in the payload is not
// sent on protocol 3 (events-ws.v3.md section 0); protocol-2 sockets keep receiving
// everything through Broadcast, unchanged.
func (h *AdminEventHub) publishJobsV3(event cancel.CancelEvent) {
	payload := make(map[string]any, len(event.Payload))
	for k, v := range event.Payload {
		if k == "jobId" || (k == "previousStatus" && v == "") {
			continue
		}
		payload[k] = v
	}
	_, _ = h.bus.Publish(events.Event{Topic: events.TopicJobs, Type: event.Type, AppID: event.AppID, JobID: event.JobID, Payload: payload})
	h.refreshSummary() // a cancel or a finished job changes running/queued
	// One stats.changed per transition: every job_status here is a terminal one (the
	// coordinator's MarkDone, a cancel); a cancel_requested only when it flagged a running
	// job (a queued job's cancel_requested{canceled} comes with its job_status).
	if event.Type == cancel.CancelEventTypeJobStatus || (event.Type == cancel.CancelEventTypeCancelRequested && event.Payload["status"] == cancel.JobStatusCancelRequested) {
		h.jobStatsChanged()
	}
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

	progressTimer *time.Timer
	progressBuf   [][]byte
	limits        *events.Limiter
	// held are the shared watchers this socket keeps running (stats clock zone, service
	// watcher, journal follower), with the option they were acquired with; all are
	// released when the socket ends, however it ends.
	held map[events.Topic]string
	// logsJoin is what the journal follower gave this socket's new logs:system
	// subscription; written after the subscribed ack.
	logsJoin *events.LogsJoin
}

// watchedTopic reports whether subscribing to t holds a shared watcher, and its key.
func watchedTopic(s events.TopicSub) (string, bool) {
	switch s.Topic {
	case events.TopicStats:
		return s.Opts.TZ, true
	case events.TopicService:
		return "", true
	case events.TopicLogs:
		return s.Opts.After, true
	}
	return "", false
}

// hold acquires the watcher of a newly accepted subscription; a sub that changes the
// option (stats tz) moves the reference.
func (vc *v3Conn) hold(s events.TopicSub) {
	key, ok := watchedTopic(s)
	if !ok {
		return
	}
	if old, held := vc.held[s.Topic]; held {
		if old == key {
			return
		}
		vc.h.releaseTopic(s.Topic, old)
	}
	if vc.held == nil {
		vc.held = make(map[events.Topic]string)
	}
	vc.held[s.Topic] = key
	if s.Topic == events.TopicLogs {
		if f := vc.h.journalFollower(); f != nil {
			join := f.Acquire(key)
			vc.logsJoin = &join
		}
		return
	}
	vc.h.acquireTopic(s.Topic, key)
}

func (vc *v3Conn) unhold(t events.Topic) {
	if key, held := vc.held[t]; held {
		delete(vc.held, t)
		vc.h.releaseTopic(t, key)
	}
}

func (vc *v3Conn) unholdAll() {
	for t := range vc.held {
		vc.unhold(t)
	}
}

// acquireTopic and releaseTopic count the subscribers of the topics backed by a shared
// watcher (refcount over every socket, subscription.md section 3).
func (h *AdminEventHub) acquireTopic(t events.Topic, key string) {
	switch t {
	case events.TopicStats:
		if c := h.statsClock.Load(); c != nil {
			c.Acquire(key)
		}
	case events.TopicService:
		if w := h.serviceWatcher(); w != nil {
			w.Acquire() // the first subscriber runs a round before the ack is written
		}
	}
}

func (h *AdminEventHub) releaseTopic(t events.Topic, key string) {
	switch t {
	case events.TopicStats:
		if c := h.statsClock.Load(); c != nil {
			c.Release(key)
		}
	case events.TopicService:
		if w := h.serviceWatcher(); w != nil {
			w.Release()
		}
	case events.TopicLogs:
		if f := h.journalFollower(); f != nil {
			f.Release()
		}
	}
}

func (vc *v3Conn) write(payload []byte) bool { return writeAdminEventPayload(vc.ctx, vc.c, payload) }

func (vc *v3Conn) writeFrame(typ string, payload any) bool {
	raw, err := vc.h.encodeV3(typ, payload)
	if err != nil {
		return false
	}
	events.M().FrameSent(typ, len(raw))
	return vc.write(raw)
}

// serveV3 runs one protocol-3 socket: hello, then a loop that writes the client's queue,
// heartbeats and session checks, and handles the client's ops read by a second goroutine.
func (h *AdminEventHub) serveV3(c *websocket.Conn, sess *eventSession, tok *eventToken, principal events.Principal, sessionExpiresAt *time.Time) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := &v3Conn{h: h, c: c, ctx: ctx, client: events.NewClient(principal), limits: events.NewLimiter(h.now())}
	h.bus.Register(vc.client)
	defer h.bus.Unregister(vc.client)
	defer vc.unholdAll() // runs on every way out: close, error, 4401/4408/4429

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
	c.SetReadLimit(events.MaxReadBytes)
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

	heartbeat := time.NewTicker(adminEventHeartbeatTick)
	defer heartbeat.Stop()
	// The session (B12) or the API token (T089 CR-4) is re-checked like on protocol 2.
	cred := h.watchCredential(sess, tok)
	defer cred.stop()
	recheck, revoked, sessionOK := cred.tick, cred.signal, cred.ok

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
		case <-vc.client.Wake():
			if !vc.flush() {
				return
			}
		case <-vc.progressDue():
			if !vc.flush() {
				return
			}
		case payload := <-vc.client.Queue():
			if vc.client.Overflowed() {
				// payload is older than the resync about to be sent: drop it with the rest.
				if !vc.flush() {
					return
				}
				continue
			}
			if !vc.write(payload) {
				return
			}
		}
	}
}

// flush handles what the bus flagged besides the queue. Overflow (FR-013, events-ws.v3.md
// section 6): the queue is emptied and one resync{overflow} names every subscription; the
// third overflow within 60 s closes the socket with 4408 (the client reconnects and
// resumes). The client is never dropped without a sign.
func (vc *v3Conn) flush() bool {
	now := vc.h.now()
	if overflowed, lagging := vc.client.TakeOverflow(now); overflowed {
		if lagging {
			vc.audit("closed_4408")
			_ = vc.c.Close(closeLagging, "lagging")
			return false
		}
		events.M().ResyncTotal.Add(events.ResyncOverflow, 1)
		if !vc.writeFrame(adminEventTypeResync, adminEventResyncPayload{Topics: vc.client.TopicList(), Reason: events.ResyncOverflow}) {
			return false
		}
	}
	// Progress (NFR-003): the newest frame per job, at most one per job per 500 ms; a held
	// frame is sent when its time comes (progressTimer).
	frames, wait := vc.client.TakeProgress(now, vc.progressBuf[:0])
	vc.progressBuf = frames[:0]
	for _, f := range frames {
		events.M().FrameSent(adminEventCompactProgress, len(f))
		if !vc.write(f) {
			return false
		}
	}
	if wait > 0 {
		if vc.progressTimer == nil {
			vc.progressTimer = time.NewTimer(wait)
		} else {
			vc.progressTimer.Reset(wait)
		}
	}
	return true
}

// progressDue is the channel of the pending progress timer (nil when none).
func (vc *v3Conn) progressDue() <-chan time.Time {
	if vc.progressTimer == nil {
		return nil
	}
	return vc.progressTimer.C
}

// handleOp applies one client frame and writes the replies; false ends the connection.
//
// Abuse policy (FR-016, events-ws.v3.md section 6): a frame over 4096 B, an op over the
// 20 per 10 s bucket, a sub listing more than 16 topics and every 5th invalid JSON frame are
// violations; each gets its error frame and the third within 60 s closes the socket with
// 4429. Refusals are audited without the frame content (FR-026).
func (vc *v3Conn) handleOp(data []byte) bool {
	now := vc.h.now()
	if len(data) > events.MaxFrameBytes {
		return vc.violate(now, events.OpError{Code: events.CodeFrameTooLarge})
	}
	op, perr := events.ParseOp(data)
	ref := op.Ref
	if perr != nil {
		ref = perr.Ref
	}
	if !vc.limits.Allow(now) {
		return vc.violate(now, events.OpError{Ref: ref, Code: events.CodeRateLimited})
	}
	if perr != nil {
		switch {
		case perr.Code == events.CodeTooManyTopics:
			return vc.violate(now, *perr)
		case perr.InvalidJSON && vc.limits.InvalidJSON():
			return vc.violate(now, *perr)
		}
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
		for _, t := range op.Unsubs {
			vc.unhold(t)
		}
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
	for _, s := range res.Accepted {
		vc.hold(s)
	}
	for _, t := range res.Refused {
		vc.audit(string(events.CodeForbiddenTopic), "topic", string(t))
		if !vc.writeError(events.OpError{Ref: op.Ref, Code: events.CodeForbiddenTopic, Topic: string(t)}) {
			return false
		}
	}
	if len(res.Accepted) == 0 {
		return true
	}
	topics := make([]adminEventSubscribedTopic, 0, len(res.Accepted))
	for _, s := range res.Accepted {
		topics = append(topics, adminEventSubscribedTopic{Name: s.Topic, State: vc.h.topicState(s.Topic)})
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
	if join := vc.logsJoin; join != nil {
		vc.logsJoin = nil
		if !vc.writeLogsJoin(*join) {
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

// writeLogsJoin sends this socket what it missed of logs:system before the live stream: the
// recent lines after its cursor, or logs.gap{cursor_behind} (stream frames: no seq).
func (vc *v3Conn) writeLogsJoin(join events.LogsJoin) bool {
	if join.Behind {
		ev := events.LogsGap(0, events.GapCursorBehind)
		return vc.writeTopicFrame(ev.Topic, ev.Type, ev.Payload)
	}
	for _, b := range join.Batches {
		if !vc.writeTopicFrame(events.TopicLogs, events.TypeLogsLines, b) {
			return false
		}
	}
	return true
}

// writeTopicFrame writes a stream frame of a topic to this socket only.
func (vc *v3Conn) writeTopicFrame(topic events.Topic, typ string, payload any) bool {
	raw, err := json.Marshal(adminEventV3{
		Protocol: adminEventProtocolV3, ID: vc.h.newEventID(), Topic: string(topic), Type: typ,
		Timestamp: vc.h.now().UTC(), Payload: payload,
	})
	if err != nil {
		return false
	}
	events.M().FrameSent(typ, len(raw))
	return vc.write(raw)
}

// topicState is the state a subscribed ack carries for the topic (events-ws.v3.md
// section 3): summary and service; nil for the others. It is computed after the bus
// subscription, so every later event of the topic is at least as new as this state.
func (h *AdminEventHub) topicState(t events.Topic) any {
	switch t {
	case events.TopicSummary:
		s := h.summary.Load()
		if s == nil {
			return nil
		}
		st, err := s.Current()
		if err != nil {
			slog.Warn("events: summary state failed", "err", err)
			return nil
		}
		return st
	case events.TopicService:
		w := h.serviceWatcher()
		if w == nil {
			return events.ServiceAckState(nil, h.now())
		}
		return events.ServiceAckState(w.Snapshot(), h.now())
	}
	return nil
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
	events.M().FrameSent(adminEventTypeSnapshot, len(raw))
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

// PublishEvent implements events.Publisher: a committed mutation (plan.md section 4)
// becomes one protocol-3 state event. Protocol-2 sockets do not receive these (their
// contract has no such types).
func (h *AdminEventHub) PublishEvent(e events.Event) {
	_, _ = h.bus.Publish(e)
	if e.Topic == events.TopicApps || e.Topic == events.TopicJobs {
		h.refreshSummary()
	}
}

// SetDeployQueue gives the hub the queue whose Summary (the apps and active fields of GET
// /stats) is the state of topic summary (T042, FR-002).
func (h *AdminEventHub) SetDeployQueue(q *store.DeployQueue) {
	h.summary.Store(events.NewSummary(func() (events.SummaryState, error) {
		s, err := q.Summary()
		return events.SummaryState{AppsTotal: s.AppsTotal, AppsEnabled: s.AppsEnabled, Running: s.Running, Queued: s.Queued}, err
	}, func(e events.Event) { _, _ = h.bus.Publish(e) }))
	h.statsClock.Store(events.NewStatsClock(func(e events.Event) { _, _ = h.bus.Publish(e) }, events.StatsClockOptions{
		WindowExit: func(now time.Time) (time.Time, bool) {
			exit, ok, err := q.NextWindowExit(now)
			if err != nil {
				slog.Warn("events: stats window boundary failed", "err", err)
				return time.Time{}, false
			}
			return exit, ok
		},
	}))
}

// jobStatsChanged announces that a job transition changed GET /stats (T043, FR-003) and
// re-arms the StatsClock (the oldest job of the 24-hour window may have changed).
func (h *AdminEventHub) jobStatsChanged() {
	_, _ = h.bus.Publish(events.StatsChanged(events.ReasonJob))
	if c := h.statsClock.Load(); c != nil {
		c.Reschedule()
	}
}

// refreshSummary publishes topic summary when an app mutation or a job transition changed
// it (published only on change).
func (h *AdminEventHub) refreshSummary() {
	if s := h.summary.Load(); s != nil {
		s.Refresh()
	}
}

// RefreshService implements events.Publisher: a systemd action may have changed the state
// of the app's unit; the service watcher runs a round at once (T045).
func (h *AdminEventHub) RefreshService(appID string) {
	h.svcMu.Lock()
	w := h.svc
	h.svcMu.Unlock()
	if w != nil {
		w.Refresh(appID)
	}
}

// SetServiceWatchOptions sets the interval (SERVICE_WATCH_INTERVAL) and debounce of the
// service watcher; call it before the first subscriber.
func (h *AdminEventHub) SetServiceWatchOptions(opts deploy.ServiceWatcherOptions) {
	h.svcMu.Lock()
	h.svcOpts = opts
	h.svcMu.Unlock()
}

// SetSystemLogUnit creates the shared journal follower of topic logs:system for the unit of
// auto-deploy itself (SELF_SERVICE_NAME). Its batches and gaps are stream frames of the
// bus; a follower that cannot start (no journalctl) is announced once as a notice.
func (h *AdminEventHub) SetSystemLogUnit(unit string) {
	f := deploy.NewJournalFollower(deploy.JournalFollowerOptions{Unit: unit}, func(lines []deploy.JournalLine, cursor string) {
		_, _ = h.bus.Publish(events.LogsLines(logLines(lines), cursor))
	}, func(dropped int, reason string) {
		_, _ = h.bus.Publish(events.LogsGap(dropped, reason))
	}, func(err error) {
		slog.Warn("events: the system log stream cannot start", "err", err)
		_, _ = h.bus.Publish(events.Event{Topic: events.TopicSummary, Type: events.TypeNotice, Payload: noticePayload{
			Level: "warning", Code: "logs_unavailable", Message: "The live system log is not available on this server.",
		}})
	})
	h.SetJournalFollower(journalAdapter{f})
}

// SetJournalFollower replaces the journal follower of topic logs:system (tests).
func (h *AdminEventHub) SetJournalFollower(f events.JournalFollower) {
	h.journalMu.Lock()
	h.journal = f
	h.journalMu.Unlock()
}

func (h *AdminEventHub) journalFollower() events.JournalFollower {
	h.journalMu.Lock()
	defer h.journalMu.Unlock()
	return h.journal
}

// Close stops the shared watchers that run a process (the journal follower) and waits for
// them; called at server shutdown.
func (h *AdminEventHub) Close() {
	if f := h.journalFollower(); f != nil {
		f.Close()
	}
}

// noticePayload is the payload of a summary notice (events-ws.v3.md 2.2).
type noticePayload struct {
	Level   string `json:"level"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// journalAdapter turns the deploy follower into the follower of topic logs:system.
type journalAdapter struct{ f *deploy.JournalFollower }

func (a journalAdapter) Acquire(after string) events.LogsJoin {
	join := a.f.Acquire(after)
	out := events.LogsJoin{Behind: join.Behind}
	for start := 0; start < len(join.Replay); start += events.MaxLogBatch {
		end := min(start+events.MaxLogBatch, len(join.Replay))
		part := join.Replay[start:end]
		out.Batches = append(out.Batches, events.LogsLinesPayload{Lines: logLines(part), Cursor: part[len(part)-1].Cursor})
	}
	return out
}

func (a journalAdapter) Release() { a.f.Release() }
func (a journalAdapter) Close()   { a.f.Close() }

func logLines(lines []deploy.JournalLine) []events.LogLine {
	out := make([]events.LogLine, len(lines))
	for i, l := range lines {
		out[i] = events.LogLine{TS: l.TS, Priority: l.Priority, Message: l.Message}
	}
	return out
}

// SetServiceWatcher replaces the systemd watcher of topic service (tests).
func (h *AdminEventHub) SetServiceWatcher(w events.ServiceWatcher) {
	h.svcMu.Lock()
	h.svc = w
	h.svcMu.Unlock()
}

// serviceWatcher returns the shared watcher of topic service, creating it from the app
// store on first use (nil without an app store). It observes every app's unit and
// publishes each change as service.status.
func (h *AdminEventHub) serviceWatcher() events.ServiceWatcher {
	h.svcMu.Lock()
	defer h.svcMu.Unlock()
	if h.svc != nil {
		return h.svc
	}
	apps := h.appStore()
	if apps == nil {
		return nil
	}
	h.svc = deploy.NewServiceWatcher(func() map[string]string {
		list, err := apps.List()
		if err != nil {
			slog.Warn("events: list apps for the service watcher failed", "err", err)
			return nil
		}
		units := make(map[string]string, len(list))
		for _, a := range list {
			units[a.ID] = a.ServiceName
		}
		return units
	}, func(appID, state string) {
		_, _ = h.bus.Publish(events.ServiceChanged(appID, state))
	}, h.svcOpts)
	return h.svc
}

// SetAppStore gives the hub the apps, for the public App of app.upserted after a deploy
// (lastDeploy*) and for the summary topic.
func (h *AdminEventHub) SetAppStore(apps *store.AppStore) { h.apps.Store(apps) }

func (h *AdminEventHub) appStore() *store.AppStore { return h.apps.Load() }

// jobStatusPayload is the payload of a protocol-3 job_status the hub builds from a queue
// transition (events-ws.v3.md 2.2). The terminal job_status of a deploy comes from the
// coordinator (it knows rolledBack and the error code) through BroadcastCancelEvent.
type jobStatusPayload struct {
	Status         string     `json:"status"`
	PreviousStatus string     `json:"previousStatus,omitempty"`
	Tag            string     `json:"tag,omitempty"`
	Trigger        string     `json:"trigger,omitempty"`
	ErrorCode      string     `json:"errorCode,omitempty"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
}

// OnJobTransition implements store.JobObserver (T041, FR-006): every committed transition
// of deploy_jobs becomes its protocol-3 events. It runs on the writer's goroutine after the
// commit and never blocks (the bus only queues).
func (h *AdminEventHub) OnJobTransition(t store.JobTransition) {
	now := h.now().UTC()
	switch t.Kind {
	case store.JobEnqueued:
		h.PublishEvent(events.Event{Topic: events.TopicJobs, Type: events.TypeJobStatus, AppID: t.AppID, JobID: t.JobID,
			Payload: jobStatusPayload{Status: t.Status, Tag: t.Tag, Trigger: t.Trigger}})
		h.jobStatsChanged()
	case store.JobStarted:
		h.PublishEvent(events.Event{Topic: events.TopicJobs, Type: events.TypeJobStatus, AppID: t.AppID, JobID: t.JobID,
			Payload: jobStatusPayload{Status: t.Status, PreviousStatus: t.PreviousStatus, Tag: t.Tag, Trigger: t.Trigger, StartedAt: &now}})
		h.jobStatsChanged()
	case store.JobFinished:
		// job_status and stats.changed: after the coordinator's job_status, which follows
		// MarkRolledBack (publishJobsV3), so the rollback count is already stored.
		h.publishAppWithLastDeploy(t.AppID)
		h.RefreshService(t.AppID)
	case store.JobRecovered:
		h.PublishEvent(events.Event{Topic: events.TopicJobs, Type: events.TypeJobStatus, AppID: t.AppID, JobID: t.JobID,
			Payload: jobStatusPayload{Status: t.Status, PreviousStatus: t.PreviousStatus, Tag: t.Tag, Trigger: t.Trigger, ErrorCode: "recovered", FinishedAt: &now}})
		h.publishAppWithLastDeploy(t.AppID)
		h.jobStatsChanged()
	case store.JobsPruned:
		h.jobStatsChanged()
	}
}

// publishAppWithLastDeploy announces the app with its lastDeploy*/lastJob* fields (the
// row of GET /apps, without liveProgress: progress only travels on topic jobs).
func (h *AdminEventHub) publishAppWithLastDeploy(appID string) {
	apps := h.appStore()
	if apps == nil || appID == "" {
		return
	}
	list, err := apps.ListWithLastDeploy()
	if err != nil {
		slog.Warn("events: load app for app.upserted failed", "appId", appID, "err", err)
		return
	}
	for i := range list {
		if list[i].ID == appID {
			list[i].LiveProgress = nil
			resp := appWithLastDeployResponse(list[i])
			resp.LiveProgress = nil
			h.PublishEvent(events.AppUpserted(appID, resp))
			return
		}
	}
}

func (vc *v3Conn) writeError(e events.OpError) bool {
	events.M().OpsRejected.Add(string(e.Code), 1)
	return vc.writeFrame(adminEventTypeError, adminEventErrorPayload{Ref: e.Ref, Code: e.Code, Topic: e.Topic})
}

// violate answers a policy violation with its error frame and counts it; the third within
// 60 s closes the socket with 4429.
func (vc *v3Conn) violate(now time.Time, e events.OpError) bool {
	vc.audit(string(e.Code))
	if !vc.writeError(e) {
		return false
	}
	if vc.limits.Violation(now) {
		vc.audit("closed_4429")
		_ = vc.c.Close(closeAbuse, "policy violation")
		return false
	}
	return true
}

// audit logs a policy decision for this socket: the reason and the client id, never the
// frame content (FR-026).
func (vc *v3Conn) audit(reason string, attrs ...any) {
	slog.Warn("events: websocket policy", append([]any{"event", "ws_policy", "reason", reason, "clientId", vc.client.ID}, attrs...)...)
}
