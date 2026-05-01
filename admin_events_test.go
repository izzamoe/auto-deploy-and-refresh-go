package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func makeAdminEventsMux(tracker *ProgressTracker, hub **AdminEventHub) *http.ServeMux {
	mux := http.NewServeMux()
	created := NewAdminEventHub(tracker)
	if hub != nil {
		*hub = created
	}
	RegisterAdminEventRoutes(mux, created, BasicAuthMiddleware("admin", "secret"))
	return mux
}

func TestAdminEventsWebSocketRequiresAuth(t *testing.T) {
	srv := httptest.NewServer(makeAdminEventsMux(NewProgressTracker(), nil))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, resp, err := websocket.Dial(ctx, progressWSURL(srv, "/admin/events/ws"), nil)
	if err == nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		t.Fatal("expected unauthorized websocket dial to fail")
	}
	if resp == nil {
		t.Fatal("expected unauthorized response")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAdminEventsWebSocketRejectsWrongPassword(t *testing.T) {
	srv := httptest.NewServer(makeAdminEventsMux(NewProgressTracker(), nil))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:wrong")))

	_, resp, err := websocket.Dial(ctx, progressWSURL(srv, "/admin/events/ws"), &websocket.DialOptions{HTTPHeader: header})
	if err == nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		t.Fatal("expected websocket dial with wrong password to fail")
	}
	if resp == nil {
		t.Fatal("expected unauthorized response")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAdminEventsConnectSendsHelloThenSnapshot(t *testing.T) {
	tracker := NewProgressTracker()
	tracker.Start("app-one", "job-one", "v1.0.0")
	tracker.Update("app-one", 100, 1000, 10)
	tracker.Start("app-two", "job-two", "v2.0.0")
	tracker.Update("app-two", 250, 500, 20)
	srv := httptest.NewServer(makeAdminEventsMux(tracker, nil))
	defer srv.Close()

	conn := dialAdminEventsWebSocket(t, srv, "/admin/events/ws?app_id=app-one&job_id=job-one")
	defer conn.Close(websocket.StatusNormalClosure, "")

	hello := readAdminEventEnvelope(t, conn)
	if hello.Version != adminEventProtocolVersion || hello.Type != adminEventTypeHello || hello.ID == "" || hello.Timestamp.IsZero() {
		t.Fatalf("unexpected hello event: %+v", hello)
	}
	if hello.Payload == nil {
		t.Fatal("hello payload should be present")
	}

	snapshot := readAdminEventEnvelope(t, conn)
	if snapshot.Type != adminEventTypeSnapshot || snapshot.Version != adminEventProtocolVersion {
		t.Fatalf("unexpected snapshot event: %+v", snapshot)
	}
	var payload []struct {
		AppID           string
		JobID           string
		DownloadedBytes int64
		TotalBytes      int64
		SpeedBPS        float64
		Percent         float64
	}
	if err := json.Unmarshal(snapshot.Payload, &payload); err != nil {
		t.Fatalf("decode snapshot payload: %v", err)
	}
	if len(payload) != 2 {
		t.Fatalf("snapshot len = %d, want unfiltered global len 2: %+v", len(payload), payload)
	}
	ids := []string{payload[0].AppID + "/" + payload[0].JobID, payload[1].AppID + "/" + payload[1].JobID}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "app-one/job-one,app-two/job-two" {
		t.Fatalf("snapshot ids = %v, want both global progress entries", ids)
	}
}

func TestAdminEventsBroadcastFanout(t *testing.T) {
	var hub *AdminEventHub
	srv := httptest.NewServer(makeAdminEventsMux(NewProgressTracker(), &hub))
	defer srv.Close()

	connA := dialAdminEventsWebSocket(t, srv, "/admin/events/ws")
	defer connA.Close(websocket.StatusNormalClosure, "")
	connB := dialAdminEventsWebSocket(t, srv, "/admin/events/ws")
	defer connB.Close(websocket.StatusNormalClosure, "")
	drainAdminEventsInitialFrames(t, connA)
	drainAdminEventsInitialFrames(t, connB)

	hub.Broadcast(AdminEvent{Type: adminEventTypeNotice, AppID: "app-broadcast", JobID: "job-broadcast", Payload: map[string]string{"message": "ready"}})

	for name, conn := range map[string]*websocket.Conn{"A": connA, "B": connB} {
		event := readAdminEventEnvelope(t, conn)
		if event.Type != adminEventTypeNotice || event.AppID != "app-broadcast" || event.JobID != "job-broadcast" {
			t.Fatalf("client %s received event %+v", name, event)
		}
	}
}

func TestAdminEventsLowFrequencyEventTypeConstants(t *testing.T) {
	tests := map[string]string{
		"hello":            adminEventTypeHello,
		"snapshot":         adminEventTypeSnapshot,
		"job_status":       adminEventTypeJobStatus,
		"cancel_requested": adminEventTypeCancelRequested,
		"notice":           adminEventTypeNotice,
		"heartbeat":        adminEventTypeHeartbeat,
	}

	for want, got := range tests {
		if got != want {
			t.Fatalf("admin event type constant = %q, want %q", got, want)
		}
	}
}

func TestAdminEventsProgressFrameUsesCompactKeys(t *testing.T) {
	var hub *AdminEventHub
	srv := httptest.NewServer(makeAdminEventsMux(NewProgressTracker(), &hub))
	defer srv.Close()

	conn := dialAdminEventsWebSocket(t, srv, "/admin/events/ws")
	defer conn.Close(websocket.StatusNormalClosure, "")
	drainAdminEventsInitialFrames(t, conn)

	hub.Broadcast(AdminEvent{Type: adminEventTypeProgress, Payload: ProgressFrame{
		AppID:           "app-compact",
		JobID:           "job-compact",
		Stage:           ProgressStageDownloading,
		Status:          ProgressStatusInProgress,
		Percent:         42,
		DownloadedBytes: 420,
		TotalBytes:      1000,
		SpeedBPS:        99,
		Message:         "downloading",
	}})

	raw := readAdminEventsRawPayload(t, conn)
	for _, forbidden := range []string{"downloadedBytes", "speedBps", "payload", "version", "type", "timestamp", "appId", "jobId"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("compact progress payload %s contains verbose key %q", raw, forbidden)
		}
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode compact progress payload: %v", err)
	}
	wantKeys := []string{"a", "bps", "db", "i", "j", "msg", "pct", "ph", "st", "t", "tb", "ts", "v"}
	gotKeys := make([]string, 0, len(frame))
	for key := range frame {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("compact keys = %v, want exactly %v", gotKeys, wantKeys)
	}
	if frame["t"] != adminEventCompactProgress || frame["a"] != "app-compact" || frame["j"] != "job-compact" || frame["ph"] != ProgressStageDownloading {
		t.Fatalf("unexpected compact progress frame: %+v", frame)
	}
}

func TestProgressEventKnownTotalIncludesPercentAndSpeed(t *testing.T) {
	tracker := NewProgressTracker()
	tracker.Start("app-known", "job-known", "v1.2.3")
	hub := NewAdminEventHub(tracker)
	tracker.SetProgressSink(hub)
	client := hub.addClient()
	defer hub.removeClient(client)

	tracker.Update("app-known", 512, 1024, 128)

	frame := readAdminEventClientProgress(t, client)
	if frame["t"] != adminEventCompactProgress {
		t.Fatalf("event type = %v, want %q", frame["t"], adminEventCompactProgress)
	}
	if frame["a"] != "app-known" || frame["j"] != "job-known" || frame["ph"] != ProgressStageDownloading || frame["st"] != ProgressStatusInProgress {
		t.Fatalf("unexpected progress identity/status: %+v", frame)
	}
	if got := int64(frame["db"].(float64)); got != 512 {
		t.Fatalf("db = %d, want 512", got)
	}
	if got := int64(frame["tb"].(float64)); got != 1024 {
		t.Fatalf("tb = %d, want 1024", got)
	}
	pct := int64(frame["pct"].(float64))
	if pct < 0 || pct > 100 || pct != 50 {
		t.Fatalf("pct = %d, want bounded 50", pct)
	}
	if got := int64(frame["bps"].(float64)); got < 0 {
		t.Fatalf("bps = %d, want nonnegative", got)
	}
}

func TestProgressEventUnknownTotalIsIndeterminate(t *testing.T) {
	tracker := NewProgressTracker()
	tracker.Start("app-unknown", "job-unknown", "v9.9.9")
	hub := NewAdminEventHub(tracker)
	tracker.SetProgressSink(hub)
	client := hub.addClient()
	defer hub.removeClient(client)

	tracker.Update("app-unknown", 2048, -1, 256)

	frame := readAdminEventClientProgress(t, client)
	if _, ok := frame["pct"]; ok {
		t.Fatalf("unknown total progress must omit pct: %+v", frame)
	}
	if _, ok := frame["tb"]; ok {
		t.Fatalf("unknown total progress must omit tb instead of fabricating a total: %+v", frame)
	}
	if got := int64(frame["db"].(float64)); got != 2048 {
		t.Fatalf("db = %d, want 2048", got)
	}
	if got := int64(frame["bps"].(float64)); got < 0 {
		t.Fatalf("bps = %d, want nonnegative", got)
	}
}

func TestProgressTrackerUpdateDoesNotBlockWithoutClients(t *testing.T) {
	tracker := NewProgressTracker()
	tracker.Start("app-no-clients", "job-no-clients", "v1")
	hub := NewAdminEventHub(tracker)
	tracker.SetProgressSink(hub)

	done := make(chan struct{})
	go func() {
		tracker.Update("app-no-clients", 10, 100, 5)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("progress update blocked with no event clients")
	}
}

func TestProgressTrackerUpdateDropsSlowEventClient(t *testing.T) {
	tracker := NewProgressTracker()
	tracker.Start("app-slow", "job-slow", "v1")
	hub := NewAdminEventHub(tracker)
	tracker.SetProgressSink(hub)
	client := hub.addClient()
	for i := 0; i < adminEventClientBuffer; i++ {
		client.send <- []byte(`{"queued":true}`)
	}

	done := make(chan struct{})
	go func() {
		tracker.Update("app-slow", 20, 100, 10)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("progress update blocked on slow event client")
	}

	hub.mu.Lock()
	_, stillRegistered := hub.clients[client]
	hub.mu.Unlock()
	if stillRegistered {
		t.Fatal("slow progress client should be dropped from hub")
	}
	for range client.send {
	}
}

func TestAdminEventHubSlowClientDoesNotBlockBroadcast(t *testing.T) {
	hub := NewAdminEventHub(NewProgressTracker())
	client := hub.addClient()
	for i := 0; i < adminEventClientBuffer; i++ {
		client.send <- []byte(`{"queued":true}`)
	}

	done := make(chan struct{})
	go func() {
		hub.Broadcast(AdminEvent{Type: adminEventTypeNotice, Payload: map[string]string{"message": "must not block"}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Broadcast blocked on slow client")
	}

	hub.mu.Lock()
	_, stillRegistered := hub.clients[client]
	hub.mu.Unlock()
	if stillRegistered {
		t.Fatal("slow client should be dropped from hub")
	}
	for range client.send {
	}
}

func readAdminEventClientProgress(t *testing.T, client *adminEventClient) map[string]any {
	t.Helper()
	select {
	case payload, ok := <-client.send:
		if !ok {
			t.Fatal("progress client channel closed before event")
		}
		var frame map[string]any
		if err := json.Unmarshal(payload, &frame); err != nil {
			t.Fatalf("decode progress event %s: %v", payload, err)
		}
		return frame
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for progress event")
	}
	return nil
}

func dialAdminEventsWebSocket(t *testing.T, srv *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:secret")))
	conn, resp, err := websocket.Dial(ctx, progressWSURL(srv, path), &websocket.DialOptions{HTTPHeader: header})
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	return conn
}

func drainAdminEventsInitialFrames(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	_ = readAdminEventEnvelope(t, conn)
	_ = readAdminEventEnvelope(t, conn)
}

func readAdminEventEnvelope(t *testing.T, conn *websocket.Conn) struct {
	Version   string          `json:"version"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	AppID     string          `json:"appId"`
	JobID     string          `json:"jobId"`
	Payload   json.RawMessage `json:"payload"`
} {
	t.Helper()
	payload := readAdminEventsRawPayload(t, conn)
	var event struct {
		Version   string          `json:"version"`
		ID        string          `json:"id"`
		Type      string          `json:"type"`
		Timestamp time.Time       `json:"timestamp"`
		AppID     string          `json:"appId"`
		JobID     string          `json:"jobId"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode admin event %s: %v", payload, err)
	}
	return event
}

func readAdminEventsRawPayload(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	messageType, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("websocket read failed: %v", err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("expected text websocket message, got %v", messageType)
	}
	return payload
}
