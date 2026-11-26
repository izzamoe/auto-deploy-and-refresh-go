package events_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/coder/websocket"
	"github.com/izzamoe/auto-deploy/internal/admin"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/deploy"
	"github.com/izzamoe/auto-deploy/internal/events"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// T045 (FR-007, FR-008, Q-3=A): topic service. The subscribed ack carries the state of every
// app; a change seen by the ONE shared watcher reaches every subscribed socket as
// service.status; the watcher is referenced once per subscribed socket and released when the
// socket unsubscribes or goes away.

// ---------------------------------------------------------------------------------------
// A hub behind the real session middleware, on a loopback port.

type topicEnv struct {
	addr   string
	hub    *admin.AdminEventHub
	apps   *store.AppStore
	cookie string
}

func newTopicEnv(t *testing.T) *topicEnv {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "topic.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	q, err := store.NewDeployQueue(db, 10)
	must(t, err)
	must(t, q.Migrate())
	apps, err := store.NewAppStore(db)
	must(t, err)
	users, err := store.NewAdminUserStore(db)
	must(t, err)
	must(t, store.Migrate(db))
	sessions, err := store.NewAdminSessionStore(db)
	must(t, err)
	tokens, err := store.NewAPITokenStore(db)
	must(t, err)
	_, err = users.CreateUser("admin", "topic-test-password", false)
	must(t, err)
	user, err := users.GetByUsername("admin")
	must(t, err)
	mgr, err := auth.NewManager(auth.SessionConfig{Key: bytes.Repeat([]byte{0x45}, 32), Secure: true}, users, sessions)
	must(t, err)

	hub := admin.NewAdminEventHub(progress.NewProgressTracker())
	hub.SetSessionChecker(mgr)
	hub.SetAppStore(apps)
	hub.SetDeployQueue(q)
	mw := auth.Middleware(auth.MiddlewareConfig{Sessions: mgr, Tokens: tokens, TokenLimiter: auth.NewWindow(10, time.Minute, nil), ClientIP: auth.NewClientIPResolver(nil)})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	addr := l.Addr().String()
	_ = l.Close()
	h := server.New(server.WithHostPorts(addr), server.WithDisablePrintRoute(true))
	admin.RegisterAdminEventRoutesHertz(h, hub, mw)
	go h.Spin()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	iss, err := mgr.Issue(user, "127.0.0.1", "topic-test")
	must(t, err)
	return &topicEnv{addr: addr, hub: hub, apps: apps, cookie: mgr.CookieName() + "=" + iss.Token}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type topicFrame struct {
	Raw     []byte
	Seq     float64        `json:"seq"`
	Topic   string         `json:"topic"`
	Type    string         `json:"type"`
	AppID   string         `json:"appId"`
	Payload map[string]any `json:"payload"`
}

type topicSocket struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	frames []topicFrame
	done   chan struct{}
}

func (e *topicEnv) dial(t *testing.T) *topicSocket {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://"+e.addr+"/admin/events/v1/ws?protocol=3", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {e.cookie}}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	must(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	s := &topicSocket{conn: conn, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			_, raw, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var f topicFrame
			_ = json.Unmarshal(raw, &f)
			f.Raw = raw
			s.mu.Lock()
			s.frames = append(s.frames, f)
			s.mu.Unlock()
		}
	}()
	s.expect(t, "hello", func(f topicFrame) bool { return f.Type == "hello" })
	return s
}

func (s *topicSocket) send(t *testing.T, op string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	must(t, s.conn.Write(ctx, websocket.MessageText, []byte(op)))
}

func (s *topicSocket) expect(t *testing.T, what string, match func(topicFrame) bool) topicFrame {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		frames := append([]topicFrame(nil), s.frames...)
		s.mu.Unlock()
		for _, f := range frames {
			if match(f) {
				return f
			}
		}
		if time.Now().After(deadline) {
			var got []string
			for _, f := range frames {
				got = append(got, string(f.Raw))
			}
			t.Fatalf("%s: no matching frame within 2 s; frames: %v", what, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ackOf(ref string) func(topicFrame) bool {
	return func(f topicFrame) bool { return f.Type == "subscribed" && f.Payload["ref"] == ref }
}

// fakeSystemctl answers is-active for the units it knows (one state per unit, in order).
type fakeSystemctl struct {
	mu    sync.Mutex
	state map[string]string
	calls int
}

func (f *fakeSystemctl) set(unit, st string) {
	f.mu.Lock()
	f.state[unit] = st
	f.mu.Unlock()
}

func (f *fakeSystemctl) run(verb string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if verb != "is-active" {
		return nil, nil
	}
	f.calls++
	var out strings.Builder
	all := true
	seenDash := false
	for _, a := range args {
		if !seenDash {
			seenDash = a == "--"
			continue
		}
		st, ok := f.state[a]
		if !ok {
			st = "inactive"
		}
		all = all && st == "active"
		out.WriteString(st + "\n")
	}
	if !all {
		return []byte(out.String()), errors.New("exit status 3")
	}
	return []byte(out.String()), nil
}

// countingWatcher stands in for the shared watcher to count its references.
type countingWatcher struct {
	mu        sync.Mutex
	refs      int
	acquired  int
	refreshes []string
}

func (w *countingWatcher) Acquire() {
	w.mu.Lock()
	w.refs++
	w.acquired++
	w.mu.Unlock()
}

func (w *countingWatcher) Release() {
	w.mu.Lock()
	w.refs--
	w.mu.Unlock()
}

func (w *countingWatcher) Refresh(appID string) {
	w.mu.Lock()
	w.refreshes = append(w.refreshes, appID)
	w.mu.Unlock()
}

func (w *countingWatcher) Snapshot() map[string]string { return map[string]string{"a1": "active"} }

func (w *countingWatcher) count() (refs, acquired int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.refs, w.acquired
}

func waitRefs(t *testing.T, w *countingWatcher, want int, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if refs, _ := w.count(); refs == want {
			return
		}
		if time.Now().After(deadline) {
			refs, _ := w.count()
			t.Fatalf("%s: watcher refs = %d, want %d", what, refs, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------------------

func TestServiceTopic_AckStateIsSortedAndNormalized(t *testing.T) {
	at := time.Date(2026, 11, 26, 10, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	st := events.ServiceAckState(map[string]string{"b": "failed", "a": "active", "c": "maintenance"}, at)
	raw, _ := json.Marshal(st)
	want := `{"statuses":[{"appId":"a","state":"active"},{"appId":"b","state":"failed"},{"appId":"c","state":"unknown"}],"observedAt":"2026-11-26T03:00:00Z"}`
	if string(raw) != want {
		t.Fatalf("ack state = %s\nwant %s", raw, want)
	}
	ev := events.ServiceChanged("a", "refreshing")
	raw, _ = json.Marshal(ev.Payload)
	if ev.Topic != events.TopicService || ev.Type != events.TypeServiceStatus || ev.AppID != "a" || string(raw) != `{"state":"unknown"}` {
		t.Fatalf("ServiceChanged = %+v %s", ev, raw)
	}
}

func TestServiceTopic_AckAndChangeThroughTheSharedWatcher(t *testing.T) {
	sc := &fakeSystemctl{state: map[string]string{}}
	t.Cleanup(deploy.SetRunSystemctlForTest(sc.run))
	e := newTopicEnv(t)
	e.hub.SetServiceWatchOptions(deploy.ServiceWatcherOptions{Interval: time.Hour, Debounce: 10 * time.Millisecond})
	app, err := e.apps.Create("svc", "svc-secret", "/opt/svc/bin", "svc-topic.service", "org/svc", "svc-linux")
	must(t, err)
	sc.set("svc-topic.service", "active")

	s1 := e.dial(t)
	s2 := e.dial(t)
	s1.send(t, `{"op":"sub","ref":"r1","topics":[{"name":"service"}]}`)
	ack := s1.expect(t, "subscribed{service}", ackOf("r1"))
	topics, _ := ack.Payload["topics"].([]any)
	st, _ := topics[0].(map[string]any)["state"].(map[string]any)
	statuses, _ := st["statuses"].([]any)
	if len(statuses) != 1 || statuses[0].(map[string]any)["appId"] != app.ID || statuses[0].(map[string]any)["state"] != "active" {
		t.Fatalf("ack state = %s, want the app active", ack.Raw)
	}
	if _, err := time.Parse(time.RFC3339, st["observedAt"].(string)); err != nil {
		t.Fatalf("observedAt is not RFC 3339: %s", ack.Raw)
	}
	s2.send(t, `{"op":"sub","ref":"r2","topics":[{"name":"service"}]}`)
	s2.expect(t, "subscribed{service} (2nd socket)", ackOf("r2"))
	calls := func() int { sc.mu.Lock(); defer sc.mu.Unlock(); return sc.calls }
	if n := calls(); n != 1 {
		t.Fatalf("two subscribers ran is-active %d times, want 1 (one shared watcher)", n)
	}

	sc.set("svc-topic.service", "failed") // outside the panel
	e.hub.RefreshService(app.ID)
	for _, s := range []*topicSocket{s1, s2} {
		f := s.expect(t, "service.status{failed}", func(f topicFrame) bool {
			return f.Topic == "service" && f.Type == "service.status" && f.AppID == app.ID && f.Payload["state"] == "failed"
		})
		if f.Seq < 1 {
			t.Fatalf("service.status has no seq: %s", f.Raw)
		}
	}
}

func TestServiceTopic_WatcherReferencedPerSocket(t *testing.T) {
	e := newTopicEnv(t)
	w := &countingWatcher{}
	e.hub.SetServiceWatcher(w)

	s1 := e.dial(t)
	s1.send(t, `{"op":"sub","ref":"r1","topics":[{"name":"service"}]}`)
	ack := s1.expect(t, "subscribed{service}", ackOf("r1"))
	if !strings.Contains(string(ack.Raw), `"statuses":[{"appId":"a1","state":"active"}]`) {
		t.Fatalf("ack does not carry the watcher snapshot: %s", ack.Raw)
	}
	s1.send(t, `{"op":"sub","ref":"r2","topics":[{"name":"service"}]}`) // already subscribed
	s1.expect(t, "subscribed (again)", ackOf("r2"))
	if refs, acquired := w.count(); refs != 1 || acquired != 1 {
		t.Fatalf("re-subscribing the same socket: refs=%d acquired=%d, want 1/1", refs, acquired)
	}

	s2 := e.dial(t)
	s2.send(t, `{"op":"sub","ref":"r1","topics":[{"name":"service"},{"name":"apps"}]}`)
	s2.expect(t, "subscribed (2nd socket)", ackOf("r1"))
	waitRefs(t, w, 2, "two subscribed sockets")

	s1.send(t, `{"op":"unsub","ref":"u1","topics":["service"]}`)
	s1.expect(t, "unsubscribed", func(f topicFrame) bool { return f.Type == "unsubscribed" && f.Payload["ref"] == "u1" })
	waitRefs(t, w, 1, "after unsub")
	s1.send(t, `{"op":"unsub","ref":"u2","topics":["service"]}`) // not subscribed any more
	s1.expect(t, "unsubscribed (again)", func(f topicFrame) bool { return f.Type == "unsubscribed" && f.Payload["ref"] == "u2" })
	waitRefs(t, w, 1, "after a second unsub")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = s2.conn.Close(websocket.StatusNormalClosure, "bye")
	<-ctx.Done()
	waitRefs(t, w, 0, "after the last subscribed socket closed")

	e.hub.RefreshService("a1")
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.refreshes) != 1 || w.refreshes[0] != "a1" {
		t.Fatalf("RefreshService reached the watcher as %v", w.refreshes)
	}
}
