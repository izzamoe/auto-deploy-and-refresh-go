package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// T030 (FR-019, FR-021; contracts/events-ws.v3.md sections 1-2, subscription.md section 1):
// every frame a protocol-3 socket receives is valid against
// internal/admin/testdata/events-ws.v3.schema.json: hello, the control frames (subscribed,
// unsubscribed, resumed, resync, error) and the events of every topic.
//
// RV2-18: the per-topic subtests publish FIXTURES straight to the hub's bus (not events of
// the REST handlers), so they are green once the protocol core exists (T031/T032), before
// the event sources of T040-T046. The fixture publisher is found at run time (the
// v3FixturePublisher interface below), so this file compiles before the bus exists and the
// RED failure is the 4409 of the old server.

// v3FixturePublisher is the hub's direct publish entry point (bus event with seq, or a stream
// frame for logs:system).
type v3FixturePublisher interface {
	PublishTopic(topic, eventType, appID, jobID, userID string, payload any)
}

func compileEventsV3Schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "events-ws.v3.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	const url = "https://auto-deploy.invalid/events-ws.v3.schema.json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// v3cSocket is a protocol-3 test client: a reader goroutine feeds frames to a channel (a
// coder/websocket Read whose context expires closes the connection, so reads never time out).
type v3cSocket struct {
	conn   *websocket.Conn
	sch    *jsonschema.Schema
	hello  map[string]any
	frames chan []byte
	errc   chan error
	seen   []map[string]any
}

func v3cDial(t *testing.T, env *wsSessionEnv, sch *jsonschema.Schema) *v3cSocket {
	t.Helper()
	conn, resp, err := dialEvents(t, env.addr, "/admin/events/v1/ws?protocol=3", env.header())
	closeBody(resp)
	if err != nil {
		t.Fatalf("dial ?protocol=3: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	s := &v3cSocket{conn: conn, sch: sch, frames: make(chan []byte, 4096), errc: make(chan error, 1)}
	go func() {
		for {
			_, raw, err := conn.Read(context.Background())
			if err != nil {
				s.errc <- err
				return
			}
			s.frames <- raw
		}
	}()
	select {
	case raw := <-s.frames:
		s.hello = s.check(t, raw)
	case err := <-s.errc:
		t.Fatalf("no hello on ?protocol=3 (close status %d): %v", websocket.CloseStatus(err), err)
	case <-time.After(3 * time.Second):
		t.Fatal("no hello on ?protocol=3 within 3 s")
	}
	if s.hello["type"] != "hello" || s.hello["protocol"] != "3" {
		t.Fatalf("first frame on ?protocol=3 = %v, want a protocol-3 hello", s.hello["_raw"])
	}
	return s
}

// check validates one frame against the v3 schema and records it.
func (s *v3cSocket) check(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("frame is not JSON: %s", raw)
	}
	if err := s.sch.Validate(v); err != nil {
		t.Errorf("frame violates events-ws.v3.schema.json: %s\n%v", raw, err)
	}
	m, _ := v.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	m["_raw"] = string(raw)
	s.seen = append(s.seen, m)
	return m
}

func (s *v3cSocket) send(t *testing.T, op any) {
	t.Helper()
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("send %s: %v", raw, err)
	}
}

// await reads (and validates) frames until one matches, at most 3 s.
func (s *v3cSocket) await(t *testing.T, what string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw := <-s.frames:
			if m := s.check(t, raw); match(m) {
				return m
			}
		case err := <-s.errc:
			t.Fatalf("%s: socket ended (close status %d): %v", what, websocket.CloseStatus(err), err)
		case <-deadline:
			var got []string
			for _, m := range s.seen {
				got = append(got, m["_raw"].(string))
			}
			t.Fatalf("%s: no matching frame within 3 s; frames so far: %v", what, got)
		}
	}
}

func v3cPayload(m map[string]any) map[string]any {
	p, _ := m["payload"].(map[string]any)
	if p == nil {
		return map[string]any{}
	}
	return p
}

func v3cIs(typ string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["type"] == typ }
}

func v3cAck(typ, ref string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["type"] == typ && v3cPayload(m)["ref"] == ref }
}

func v3cSub(ref string, resume map[string]any, topics ...map[string]any) map[string]any {
	op := map[string]any{"op": "sub", "ref": ref, "topics": topics}
	if resume != nil {
		op["resume"] = resume
	}
	return op
}

func v3cTopic(name string) map[string]any { return map[string]any{"name": name} }

func v3cPublisher(t *testing.T, hub *AdminEventHub) v3FixturePublisher {
	t.Helper()
	p, ok := any(hub).(v3FixturePublisher)
	if !ok {
		t.Fatal("AdminEventHub has no PublishTopic(topic, eventType, appID, jobID, userID string, payload any)")
	}
	return p
}

func v3cNum(m map[string]any, key string) int {
	f, _ := m[key].(float64)
	return int(f)
}

func v3cFixtureApp(id string) map[string]any {
	return map[string]any{
		"id": id, "name": "fixture", "binaryPath": "/opt/fixture/app", "serviceName": "fixture.service",
		"githubRepo": "owner/fixture", "artifactName": "fixture-linux", "enabled": true,
		"createdAt": "2026-11-25T10:00:00Z", "updatedAt": "2026-11-25T10:00:01Z",
		"lastJobStatus": "succeeded", "lastDeployTag": "v1.0.0",
	}
}

func TestEventsV3_Contract(t *testing.T) {
	sch := compileEventsV3Schema(t)
	env := func(t *testing.T) *wsSessionEnv {
		t.Helper()
		name := "v3c_" + strings.NewReplacer("/", "_", ":", "_").Replace(t.Name())
		// The server already runs here: the hub's fields are only read from now on (the
		// tracker's sink is set under its own lock).
		e := newWSSessionEnv(t, name, time.Hour)
		e.hub.tracker.SetProgressSink(e.hub)
		return e
	}

	t.Run("schema_rejects", func(t *testing.T) {
		for _, bad := range []string{
			`{"protocol":"2","id":"evt-1","type":"heartbeat","ts":"2026-11-25T10:00:00Z"}`,                                                                                // protocol 2
			`{"protocol":"3","id":"evt-1","topic":"apps","type":"app.deleted","ts":"2026-11-25T10:00:00Z","appId":"a1","payload":{}}`,                                     // state event without seq
			`{"protocol":"3","id":"evt-1","seq":1,"topic":"jobs","type":"job_status","ts":"2026-11-25T10:00:00Z","jobId":"j","payload":{"jobId":"j","status":"pending"}}`, // v3 drops payload.jobId
			`{"protocol":"3","id":"evt-1","seq":1,"topic":"apps","type":"app.upserted","ts":"2026-11-25T10:00:00Z","appId":"a1","payload":{"app":{"webhookSecret":"x"}}}`, // secret field
			`{"protocol":"3","id":"evt-1","seq":1,"topic":"summary","type":"stats.changed","ts":"2026-11-25T10:00:00Z","payload":{"reasons":["job"]}}`,                    // wrong topic
			`{"protocol":"3","id":"evt-1","type":"error","ts":"2026-11-25T10:00:00Z","payload":{"code":"nope"}}`,                                                          // unknown error code
			`{"protocol":"3","id":"evt-1","seq":3,"topic":"logs:system","type":"logs.lines","ts":"2026-11-25T10:00:00Z","payload":{"lines":[],"cursor":"c"}}`,             // stream with seq
		} {
			var v any
			_ = json.Unmarshal([]byte(bad), &v)
			if err := sch.Validate(v); err == nil {
				t.Errorf("schema accepted an invalid frame: %s", bad)
			}
		}
	})

	t.Run("hello", func(t *testing.T) {
		e := env(t)
		s := v3cDial(t, e, sch)
		p := v3cPayload(s.hello)
		if p["serverVersion"] != e.hub.serverVersion || v3cNum(p, "heartbeatSeconds") != 30 {
			t.Errorf("hello serverVersion/heartbeatSeconds: %v", s.hello["_raw"])
		}
		if _, ok := p["sessionExpiresAt"].(string); !ok {
			t.Errorf("hello of a session principal has no sessionExpiresAt: %v", s.hello["_raw"])
		}
		s2 := v3cDial(t, e, sch)
		p2 := v3cPayload(s2.hello)
		if p["epoch"] != p2["epoch"] {
			t.Errorf("two sockets of one process see different epochs: %v / %v", p["epoch"], p2["epoch"])
		}
		if p["clientId"] == p2["clientId"] {
			t.Errorf("two sockets got the same clientId %v", p["clientId"])
		}
	})

	t.Run("subscribed", func(t *testing.T) {
		e := env(t)
		s := v3cDial(t, e, sch)
		s.send(t, v3cSub("r1", nil, v3cTopic("summary"), v3cTopic("apps")))
		ack := s.await(t, "subscribed{r1}", v3cAck("subscribed", "r1"))
		names := map[string]bool{}
		for _, tp := range v3cPayload(ack)["topics"].([]any) {
			names[tp.(map[string]any)["name"].(string)] = true
		}
		if !names["summary"] || !names["apps"] {
			t.Errorf("subscribed topics = %v, want summary and apps", names)
		}
		s.send(t, map[string]any{"op": "unsub", "ref": "r2", "topics": []string{"apps"}})
		un := s.await(t, "unsubscribed{r2}", v3cAck("unsubscribed", "r2"))
		if got := fmt.Sprint(v3cPayload(un)["topics"]); got != "[apps]" {
			t.Errorf("unsubscribed topics = %s, want [apps]", got)
		}
	})

	t.Run("resumed", func(t *testing.T) {
		e := env(t)
		pub := v3cPublisher(t, e.hub)
		s := v3cDial(t, e, sch)
		epoch, seq := v3cPayload(s.hello)["epoch"], v3cNum(v3cPayload(s.hello), "seq")
		// Published while the socket is not subscribed: only the resume can deliver them.
		pub.PublishTopic("apps", "app.deleted", "a1", "", "", map[string]any{})
		pub.PublishTopic("apps", "app.deleted", "a2", "", "", map[string]any{})
		s.send(t, v3cSub("r1", map[string]any{"epoch": epoch, "seq": seq}, v3cTopic("apps")))
		first := s.await(t, "replayed app.deleted a1", func(m map[string]any) bool { return m["type"] == "app.deleted" })
		second := s.await(t, "replayed app.deleted a2", func(m map[string]any) bool { return m["type"] == "app.deleted" })
		if first["appId"] != "a1" || second["appId"] != "a2" || v3cNum(first, "seq") != seq+1 || v3cNum(second, "seq") != seq+2 {
			t.Errorf("replay out of order: %v then %v (hello seq %d)", first["_raw"], second["_raw"], seq)
		}
		r := s.await(t, "resumed{r1}", v3cAck("resumed", "r1"))
		rp := v3cPayload(r)
		if v3cNum(rp, "fromSeq") != seq || v3cNum(rp, "toSeq") != seq+2 || v3cNum(rp, "replayed") != 2 {
			t.Errorf("resumed = %v, want fromSeq %d toSeq %d replayed 2", r["_raw"], seq, seq+2)
		}
	})

	t.Run("resync", func(t *testing.T) {
		e := env(t)
		pub := v3cPublisher(t, e.hub)
		s := v3cDial(t, e, sch)
		epoch, seq := v3cPayload(s.hello)["epoch"], v3cNum(v3cPayload(s.hello), "seq")

		s.send(t, v3cSub("r1", map[string]any{"epoch": strings.Repeat("0", 32), "seq": 1}, v3cTopic("apps")))
		r := s.await(t, "resync{r1}", v3cAck("resync", "r1"))
		if v3cPayload(r)["reason"] != "epoch" || fmt.Sprint(v3cPayload(r)["topics"]) != "[apps]" {
			t.Errorf("resync after another epoch = %v, want reason epoch, topics [apps]", r["_raw"])
		}

		for i := 0; i < 1100; i++ { // more than the ring holds
			pub.PublishTopic("stats", "stats.changed", "", "", "", map[string]any{"reasons": []string{"app"}})
		}
		s.send(t, v3cSub("r2", map[string]any{"epoch": epoch, "seq": seq}, v3cTopic("stats")))
		g := s.await(t, "resync{r2}", v3cAck("resync", "r2"))
		if v3cPayload(g)["reason"] != "gap" || fmt.Sprint(v3cPayload(g)["topics"]) != "[stats]" {
			t.Errorf("resync after a gap = %v, want reason gap, topics [stats]", g["_raw"])
		}
	})

	t.Run("error", func(t *testing.T) {
		e := env(t)
		s := v3cDial(t, e, sch)
		s.send(t, v3cSub("r1", nil, v3cTopic("nope")))
		er := s.await(t, "error{r1}", v3cAck("error", "r1"))
		if v3cPayload(er)["code"] != "unknown_topic" || v3cPayload(er)["topic"] != "nope" {
			t.Errorf("error for an unknown topic = %v", er["_raw"])
		}
		s.send(t, map[string]any{"op": "pub", "ref": "r2"})
		bad := s.await(t, "error{r2}", v3cAck("error", "r2"))
		if v3cPayload(bad)["code"] != "bad_op" {
			t.Errorf("error for an unknown op = %v", bad["_raw"])
		}
	})

	// One subtest per topic (subscription.md section 1): subscribe, publish fixtures, every
	// frame must be valid and arrive.
	t.Run("summary", func(t *testing.T) {
		e := env(t)
		pub := v3cPublisher(t, e.hub)
		s := v3cDial(t, e, sch)
		s.send(t, v3cSub("r1", nil, v3cTopic("summary")))
		s.await(t, "subscribed{r1}", v3cAck("subscribed", "r1"))
		pub.PublishTopic("summary", "summary", "", "", "", map[string]any{"appsTotal": 2, "appsEnabled": 1, "running": 1, "queued": 0})
		pub.PublishTopic("summary", "notice", "", "", "", map[string]any{"level": "info", "code": "fixture", "message": "hello"})
		s.await(t, "summary", v3cIs("summary"))
		s.await(t, "notice", v3cIs("notice"))
	})

	t.Run("jobs", func(t *testing.T) {
		e := env(t)
		pub := v3cPublisher(t, e.hub)
		e.hub.tracker.Start("a1", "j1", "v1.0.0")
		s := v3cDial(t, e, sch)
		s.send(t, v3cSub("r1", nil, map[string]any{"name": "jobs", "progress": true}))
		s.await(t, "subscribed{r1}", v3cAck("subscribed", "r1"))
		snap := s.await(t, "snapshot", v3cIs("snapshot"))
		if jobs, _ := v3cPayload(snap)["jobs"].([]any); len(jobs) != 1 {
			t.Errorf("snapshot = %v, want the one active job", snap["_raw"])
		}
		e.hub.tracker.Update("a1", 10, 100, 5)
		p := s.await(t, "progress p", func(m map[string]any) bool { return m["t"] == "p" })
		if p["v"] != "3" || p["j"] != "j1" {
			t.Errorf("progress frame = %v", p["_raw"])
		}
		pub.PublishTopic("jobs", "job_status", "a1", "j1", "", map[string]any{"status": "in_progress", "previousStatus": "pending", "tag": "v1.0.0", "startedAt": "2026-11-25T10:00:00Z"})
		pub.PublishTopic("jobs", "cancel_requested", "a1", "j1", "", map[string]any{"status": "cancel_requested", "previousStatus": "in_progress", "outcome": "requested", "message": "cancel requested"})
		s.await(t, "job_status", v3cIs("job_status"))
		s.await(t, "cancel_requested", v3cIs("cancel_requested"))
	})

	t.Run("apps", func(t *testing.T) {
		e := env(t)
		pub := v3cPublisher(t, e.hub)
		s := v3cDial(t, e, sch)
		s.send(t, v3cSub("r1", nil, v3cTopic("apps")))
		s.await(t, "subscribed{r1}", v3cAck("subscribed", "r1"))
		pub.PublishTopic("apps", "app.upserted", "a1", "", "", map[string]any{"app": v3cFixtureApp("a1")})
		pub.PublishTopic("apps", "app.config_changed", "a1", "", "", map[string]any{"part": "env", "updatedAt": "2026-11-25T10:00:02Z"})
		pub.PublishTopic("apps", "app.deleted", "a1", "", "", map[string]any{})
		s.await(t, "app.upserted", v3cIs("app.upserted"))
		s.await(t, "app.config_changed", v3cIs("app.config_changed"))
		s.await(t, "app.deleted", v3cIs("app.deleted"))
	})

	t.Run("service", func(t *testing.T) {
		e := env(t)
		pub := v3cPublisher(t, e.hub)
		s := v3cDial(t, e, sch)
		s.send(t, v3cSub("r1", nil, v3cTopic("service")))
		s.await(t, "subscribed{r1}", v3cAck("subscribed", "r1"))
		pub.PublishTopic("service", "service.status", "a1", "", "", map[string]any{"state": "failed"})
		s.await(t, "service.status", v3cIs("service.status"))
	})

	t.Run("stats", func(t *testing.T) {
		e := env(t)
		pub := v3cPublisher(t, e.hub)
		s := v3cDial(t, e, sch)
		s.send(t, v3cSub("r1", nil, map[string]any{"name": "stats", "tz": "Asia/Jakarta"}))
		s.await(t, "subscribed{r1}", v3cAck("subscribed", "r1"))
		pub.PublishTopic("stats", "stats.changed", "", "", "", map[string]any{"reasons": []string{"window"}})
		s.await(t, "stats.changed", v3cIs("stats.changed"))
	})

	t.Run("settings", func(t *testing.T) {
		e := env(t)
		pub := v3cPublisher(t, e.hub)
		s := v3cDial(t, e, sch)
		s.send(t, v3cSub("r1", nil, v3cTopic("settings")))
		s.await(t, "subscribed{r1}", v3cAck("subscribed", "r1"))
		pub.PublishTopic("settings", "settings.changed", "", "", "", map[string]any{"section": "telegram"})
		s.await(t, "settings.changed", v3cIs("settings.changed"))
	})

	t.Run("account", func(t *testing.T) {
		e := env(t)
		pub := v3cPublisher(t, e.hub)
		s := v3cDial(t, e, sch)
		s.send(t, v3cSub("r1", nil, v3cTopic("account")))
		s.await(t, "subscribed{r1}", v3cAck("subscribed", "r1"))
		pub.PublishTopic("account", "account.changed", "", "", "another-user", map[string]any{"section": "sessions"})
		pub.PublishTopic("account", "account.changed", "", "", e.user.ID, map[string]any{"section": "tokens"})
		got := s.await(t, "account.changed of the same user", v3cIs("account.changed"))
		if v3cPayload(got)["section"] != "tokens" {
			t.Errorf("FR-017: the socket received the account event of another user: %v", got["_raw"])
		}
		if strings.Contains(got["_raw"].(string), e.user.ID) {
			t.Errorf("the internal userId must not be serialized: %v", got["_raw"])
		}
	})

	t.Run("logs:system", func(t *testing.T) {
		e := env(t)
		pub := v3cPublisher(t, e.hub)
		s := v3cDial(t, e, sch)
		s.send(t, v3cSub("r1", nil, map[string]any{"name": "logs:system", "after": "s=1;i=2"}))
		s.await(t, "subscribed{r1}", v3cAck("subscribed", "r1"))
		pub.PublishTopic("logs:system", "logs.lines", "", "", "", map[string]any{
			"lines":  []map[string]any{{"ts": "2026-11-25T10:00:00Z", "priority": 6, "message": "started"}},
			"cursor": "s=1;i=3",
		})
		pub.PublishTopic("logs:system", "logs.gap", "", "", "", map[string]any{"dropped": 3, "reason": "rate"})
		for _, typ := range []string{"logs.lines", "logs.gap"} {
			m := s.await(t, typ, v3cIs(typ))
			if _, ok := m["seq"]; ok {
				t.Errorf("%s is a stream frame and must carry no seq: %v", typ, m["_raw"])
			}
		}
	})
}
