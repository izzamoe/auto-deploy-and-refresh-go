package admin

import (
	"bytes"
	"context"
	"database/sql"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/coder/websocket"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
	_ "modernc.org/sqlite"
)

// fakeClock is a settable clock shared by the session manager under test.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type wsSessionEnv struct {
	addr  string
	hub   *AdminEventHub
	mgr   *auth.Manager
	users *store.AdminUserStore
	user  *store.AdminUser
	clock *fakeClock
	iss   *auth.Issued
}

// newWSSessionEnv serves the event socket behind a private session manager with a
// fake clock, so expiry and revocation can be driven from the test.
func newWSSessionEnv(t *testing.T, name string, checkEvery time.Duration) *wsSessionEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	users, err := store.NewAdminUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := store.NewAdminSessionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	user, err := users.CreateUser("admin", "secret", false)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)}
	mgr, err := auth.NewManager(auth.SessionConfig{Key: bytes.Repeat([]byte{0x2b}, 32), Secure: true, Now: clock.Now}, users, sessions)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := mgr.Issue(user, "127.0.0.1", "go-test")
	if err != nil {
		t.Fatal(err)
	}

	old := adminEventSessionCheckInterval
	adminEventSessionCheckInterval = checkEvery
	t.Cleanup(func() { adminEventSessionCheckInterval = old })

	addr := freeAddr(t)
	hub := NewAdminEventHub(progress.NewProgressTracker())
	hub.SetSessionChecker(mgr)
	h := server.New(server.WithHostPorts(addr), server.WithDisablePrintRoute(true))
	RegisterAdminEventRoutesHertz(h, hub, auth.Middleware(auth.MiddlewareConfig{Sessions: mgr, Now: clock.Now}))
	go h.Spin()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &wsSessionEnv{addr: addr, hub: hub, mgr: mgr, users: users, user: user, clock: clock, iss: iss}
}

func (e *wsSessionEnv) header() http.Header {
	h := http.Header{}
	h.Set("Cookie", e.mgr.CookieName()+"="+e.iss.Token)
	return h
}

// connect dials and consumes hello + snapshot.
func (e *wsSessionEnv) connect(t *testing.T, path string) *websocket.Conn {
	t.Helper()
	conn, resp, err := dialEvents(t, e.addr, path, e.header())
	closeBody(resp)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	readFrame(t, conn) // hello
	readFrame(t, conn) // snapshot
	return conn
}

// expectClose reads until the server closes the socket and returns the close code
// (-1 when no close arrived before the deadline).
func expectClose(conn *websocket.Conn, within time.Duration) websocket.StatusCode {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	for {
		_, _, err := conn.Read(ctx)
		if err == nil {
			continue // heartbeat or other event
		}
		return websocket.CloseStatus(err)
	}
}

// B12 / FR-025: revoking the session closes an open socket with 4401 right away
// (the in-process signal; the periodic check is far away in this test).
func TestEvents_ClosesWith4401OnRevoke(t *testing.T) {
	env := newWSSessionEnv(t, "ws_revoke", time.Hour) // only the signal can close it
	conn := env.connect(t, "/admin/events/v1/ws")
	if err := env.mgr.Revoke(env.iss.SessionID); err != nil {
		t.Fatal(err)
	}
	if code := expectClose(conn, 2*time.Second); code != 4401 {
		t.Fatalf("socket close code after revoke = %d, want 4401", code)
	}
}

// B12 / FR-025: the session runs out (idle window passes, fake clock) -> 4401 at the
// next periodic check.
func TestEvents_ClosesWith4401OnExpiry(t *testing.T) {
	env := newWSSessionEnv(t, "ws_expiry", 40*time.Millisecond)
	conn := env.connect(t, "/admin/events/v1/ws")
	env.clock.Advance(auth.DefaultIdle + time.Minute)
	if code := expectClose(conn, 2*time.Second); code != 4401 {
		t.Fatalf("socket close code after expiry = %d, want 4401", code)
	}
}

// FR-025: a token_version bump (password change / revoke-all) closes sockets of the
// old session with 4401 at the next check.
func TestEvents_ClosesWith4401OnTokenVersionChange(t *testing.T) {
	env := newWSSessionEnv(t, "ws_ver", 40*time.Millisecond)
	conn := env.connect(t, "/admin/events/v1/ws")
	if err := env.users.BumpTokenVersion(env.user.ID); err != nil {
		t.Fatal(err)
	}
	if code := expectClose(conn, 2*time.Second); code != 4401 {
		t.Fatalf("socket close code after token_version bump = %d, want 4401", code)
	}
}

// Control: a valid session keeps its socket open across several checks.
func TestEvents_StaysOpenWhileSessionIsValid(t *testing.T) {
	env := newWSSessionEnv(t, "ws_valid", 40*time.Millisecond)
	conn := env.connect(t, "/admin/events/v1/ws")
	if code := expectClose(conn, 500*time.Millisecond); code != -1 {
		t.Fatalf("a valid session's socket was closed with %d", code)
	}
}

// FR-025 / RV-14: ?protocol=<other than 2> closes with 4409; ?protocol=2 and no query
// are served normally.
func TestEvents_UnsupportedProtocolClosesWith4409(t *testing.T) {
	env := newWSSessionEnv(t, "ws_proto", time.Hour)
	for _, q := range []string{"?protocol=1", "?protocol=4", "?protocol=", "?protocol=abc"} {
		conn, resp, err := dialEvents(t, env.addr, "/admin/events/v1/ws"+q, env.header())
		closeBody(resp)
		if err != nil {
			t.Fatalf("dial %s: %v", q, err)
		}
		if code := expectClose(conn, 2*time.Second); code != 4409 {
			t.Errorf("%s: close code = %d, want 4409", q, code)
		}
		_ = conn.CloseNow()
	}
	for _, q := range []string{"", "?protocol=2"} {
		conn, resp, err := dialEvents(t, env.addr, "/admin/events/v1/ws"+q, env.header())
		closeBody(resp)
		if err != nil {
			t.Fatalf("dial %q: %v", q, err)
		}
		if frame := readFrame(t, conn); len(frame) == 0 {
			t.Errorf("%q: no hello frame", q)
		}
		_ = conn.CloseNow()
	}
}
