package admin

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/coder/websocket"
	"github.com/izzamoe/auto-deploy/internal/progress"
)

// freeAddr returns a loopback address with a currently free port.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// startEventsServer serves the event socket behind the real v1 auth middleware.
func startEventsServer(t *testing.T) (addr string, hub *AdminEventHub) {
	t.Helper()
	addr = freeAddr(t)
	hub = NewAdminEventHub(progress.NewProgressTracker())
	hub.SetServerVersion("v9.9.9-test")
	h := server.New(server.WithHostPorts(addr), server.WithDisablePrintRoute(true))
	RegisterAdminEventRoutesHertz(h, hub, testHertzSessionAuth(t))
	go h.Spin()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	})
	// wait until the listener answers
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			return addr, hub
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("events server did not start")
	return
}

func dialEvents(t *testing.T, addr, path string, header http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws://"+addr+path, &websocket.DialOptions{HTTPHeader: header})
}

// closeBody closes a handshake response body; a successful upgrade (101) has none.
func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func sessionHeader(t *testing.T) http.Header {
	h := http.Header{}
	h.Set("Cookie", testAdminSessionCookie(t))
	return h
}

// K4 / FR-012: the handshake verifies Origin (the default of coder/websocket). A page
// on another origin must not be able to open the socket with the admin's cookie.
func TestEventsRejectsForeignOrigin(t *testing.T) {
	addr, _ := startEventsServer(t)

	foreign := sessionHeader(t)
	foreign.Set("Origin", "https://evil.example")
	conn, resp, err := dialEvents(t, addr, "/admin/events/v1/ws", foreign)
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("K4: a handshake with Origin https://evil.example must be refused")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: response = %+v, want 403", resp)
	}
	closeBody(resp)

	// The same cookie from a same-origin page and from a non-browser client works.
	same := sessionHeader(t)
	same.Set("Origin", "http://"+addr)
	conn, resp, err = dialEvents(t, addr, "/admin/events/v1/ws", same)
	if resp != nil {
		closeBody(resp)
	}
	if err != nil {
		t.Fatalf("same-origin handshake must succeed: %v", err)
	}
	_ = conn.CloseNow()

	conn, resp, err = dialEvents(t, addr, "/admin/events/v1/ws", sessionHeader(t))
	if resp != nil {
		closeBody(resp)
	}
	if err != nil {
		t.Fatalf("a client without Origin (not a browser) must succeed: %v", err)
	}
	_ = conn.CloseNow()
}

func TestEventsRequireSession(t *testing.T) {
	addr, _ := startEventsServer(t)
	for _, path := range []string{"/admin/events/v1/ws", "/admin/events/ws"} {
		conn, resp, err := dialEvents(t, addr, path, nil)
		if err == nil {
			_ = conn.CloseNow()
			t.Fatalf("%s without a session must be refused", path)
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without a session: response = %+v, want 401", path, resp)
		}
		if resp.Header.Get("WWW-Authenticate") != "" {
			t.Errorf("%s: must not challenge", path)
		}
		closeBody(resp)
	}
}

type helloFrame struct {
	Protocol string `json:"protocol"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Ts       string `json:"ts"`
	Payload  struct {
		ServerVersion    string `json:"serverVersion"`
		SessionExpiresAt string `json:"sessionExpiresAt"`
		HeartbeatSeconds int    `json:"heartbeatSeconds"`
	} `json:"payload"`
}

func readFrame(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return data
}

// FR-023 / events-ws.md 3.1: the first event is hello with the protocol version and
// the server version.
func TestHelloHasProtocolAndServerVersion(t *testing.T) {
	addr, _ := startEventsServer(t)
	conn, resp, err := dialEvents(t, addr, "/admin/events/v1/ws", sessionHeader(t))
	if resp != nil {
		closeBody(resp)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	var hello helloFrame
	raw := readFrame(t, conn)
	if err := json.Unmarshal(raw, &hello); err != nil {
		t.Fatalf("hello is not JSON: %v (%s)", err, raw)
	}
	if hello.Type != "hello" {
		t.Fatalf("first event type = %q, want hello (%s)", hello.Type, raw)
	}
	if hello.Protocol != "2" {
		t.Errorf("protocol = %q, want \"2\" (the old \"v1\" must be distinguishable)", hello.Protocol)
	}
	if hello.ID == "" || hello.Ts == "" {
		t.Errorf("envelope lacks id/ts: %s", raw)
	} else if _, err := time.Parse(time.RFC3339Nano, hello.Ts); err != nil {
		t.Errorf("ts %q is not RFC 3339: %v", hello.Ts, err)
	}
	if hello.Payload.ServerVersion != "v9.9.9-test" {
		t.Errorf("serverVersion = %q, want the hub's version", hello.Payload.ServerVersion)
	}
	if hello.Payload.HeartbeatSeconds != 30 {
		t.Errorf("heartbeatSeconds = %d, want 30", hello.Payload.HeartbeatSeconds)
	}
	exp, err := time.Parse(time.RFC3339Nano, hello.Payload.SessionExpiresAt)
	if err != nil {
		t.Fatalf("sessionExpiresAt %q: %v", hello.Payload.SessionExpiresAt, err)
	}
	if !exp.After(time.Now()) {
		t.Errorf("sessionExpiresAt %v is not in the future", exp)
	}

	// The second frame is the snapshot (shape of the snapshot is a later task).
	var second struct {
		Protocol string `json:"protocol"`
		Type     string `json:"type"`
	}
	if err := json.Unmarshal(readFrame(t, conn), &second); err != nil || second.Type != "snapshot" || second.Protocol != "2" {
		t.Errorf("second event = %+v (%v), want a protocol-2 snapshot", second, err)
	}
}

// Without an explicit version the hello reports the linker-stamped build version.
func TestHelloDefaultsToBuildVersion(t *testing.T) {
	hub := NewAdminEventHub(progress.NewProgressTracker())
	if hub.serverVersion == "" {
		t.Fatal("the hub must default serverVersion to llmstxt.Version")
	}
}

// Q-7: the pre-v1 path keeps working for one release, with a deprecation notice.
func TestEventsLegacyPathIsDeprecatedAlias(t *testing.T) {
	addr, _ := startEventsServer(t)
	conn, resp, err := dialEvents(t, addr, "/admin/events/ws", sessionHeader(t))
	if err != nil {
		if resp != nil {
			closeBody(resp)
		}
		t.Fatalf("legacy path must still upgrade: %v", err)
	}
	defer conn.CloseNow()
	defer closeBody(resp)
	if got := resp.Header.Get("Deprecation"); got != "true" {
		t.Errorf("legacy handshake Deprecation = %q, want true", got)
	}
	if got := resp.Header.Get("Link"); got != `</admin/events/v1/ws>; rel="successor-version"` {
		t.Errorf("legacy handshake Link = %q", got)
	}
	var hello helloFrame
	if err := json.Unmarshal(readFrame(t, conn), &hello); err != nil || hello.Protocol != "2" {
		t.Errorf("the legacy path serves the same protocol: %+v (%v)", hello, err)
	}

	// The canonical path carries no deprecation notice.
	conn2, resp2, err := dialEvents(t, addr, "/admin/events/v1/ws", sessionHeader(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.CloseNow()
	defer closeBody(resp2)
	if got := resp2.Header.Get("Deprecation"); got != "" {
		t.Errorf("canonical path must not be marked deprecated, got %q", got)
	}
}

func TestEventEnvelopeUsesProtocolAndTsKeys(t *testing.T) {
	hub := NewAdminEventHub(progress.NewProgressTracker())
	raw, err := hub.encodeEvent(AdminEvent{Type: adminEventTypeNotice, AppID: "a1", JobID: "j1", Payload: map[string]string{"code": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"protocol", "id", "type", "ts", "appId", "jobId", "payload"} {
		if _, ok := m[k]; !ok {
			t.Errorf("envelope lacks %q: %s", k, raw)
		}
	}
	for _, k := range []string{"version", "timestamp"} {
		if _, ok := m[k]; ok {
			t.Errorf("old envelope key %q must be gone: %s", k, raw)
		}
	}
	if m["protocol"] != "2" || m["id"] != "evt-1" {
		t.Errorf("protocol/id = %v/%v, want 2/evt-1", m["protocol"], m["id"])
	}
	if _, err := strconv.Atoi("2"); err != nil {
		t.Fatal(err)
	}
}
