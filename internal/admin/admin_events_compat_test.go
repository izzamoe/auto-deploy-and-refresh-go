package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// T084 (FR-019, FR-020, FR-021, Q-11=A): the protocol-2 alias stays what 001 clients
// expect for one release. A `?protocol=2` client gets hello then snapshot, the old job
// events in the 001 envelope (events-ws.schema.json, additionalProperties false), 4401 when
// its session ends; `?protocol=4` is refused with 4409; and NOTHING of protocol 3 (topics,
// seq, new event types) ever reaches it, even during a complete mutation scenario.

// compatDialV2 opens /admin/events/v1/ws?protocol=2 and starts collecting every frame
// (hello included, unlike mutevEnv.dial which consumes it).
func compatDialV2(t *testing.T, e *mutevEnv, cookie string) *mutevSocket {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://"+e.addr+"/admin/events/v1/ws?protocol=2", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {cookie}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial ?protocol=2: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	s := &mutevSocket{conn: conn, done: make(chan struct{})}
	go s.readLoop()
	return s
}

// compatWaitFrames waits until the socket has at least n frames (3 s at most).
func compatWaitFrames(t *testing.T, s *mutevSocket, n int) []mutevFrame {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		frames := s.snapshot()
		if len(frames) >= n {
			return frames
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d frame(s) within 3 s, want >= %d", len(frames), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEventsCompat_V2HelloSnapshotAndJobEvents(t *testing.T) {
	sch := compileEventsSchema(t)
	e := newMutevEnv(t)
	id := e.createApp(t, mutevAppBody("compat", "Cp2vT6xK0qL4wZ8rB3nH7jD1fG5sM9aE"))

	s := compatDialV2(t, e, e.cookie)
	frames := compatWaitFrames(t, s, 2)
	if frames[0].Type != "hello" || frames[0].Protocol != "2" {
		t.Fatalf("first frame on ?protocol=2 = %s, want a protocol-2 hello", frames[0].Raw)
	}
	if frames[1].Type != "snapshot" || frames[1].Protocol != "2" {
		t.Fatalf("second frame on ?protocol=2 = %s, want the 001 snapshot", frames[1].Raw)
	}

	// 001 events-ws.md 3.4: job_status is the coordinator's TERMINAL event, with jobId at the
	// top level and (for one release) in the payload too. The pending/in_progress job_status
	// of 002 (store.JobObserver) is protocol 3 only.
	m := s.mark()
	e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v1.0.0"}, http.StatusAccepted)
	job := e.jobID(t, id, "v1.0.0")
	e.waitJob(t, job, "in_progress")
	e.runner.finish("v1.0.0", nil)
	f := s.expect(t, m, "v2: terminal job_status{succeeded}", func(f mutevFrame) bool {
		return f.Protocol == "2" && f.Type == "job_status" && f.JobID == job && f.Payload["status"] == "succeeded"
	})
	if f.Payload["jobId"] != job {
		t.Errorf("v2 job_status keeps payload.jobId for one release (001 B11): %s", f.Raw)
	}

	for _, f := range s.snapshot() {
		validateEvent(t, sch, string(f.Raw))
		if f.Type == "job_status" && (f.Payload["status"] == "pending" || f.Payload["status"] == "in_progress") {
			t.Errorf("v2 received a non-terminal job_status (a protocol-3 source): %s", f.Raw)
		}
	}
}

func TestEventsCompat_Protocol4Closes4409(t *testing.T) {
	e := newMutevEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://"+e.addr+"/admin/events/v1/ws?protocol=4", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {e.cookie}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial ?protocol=4: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			if got := websocket.CloseStatus(err); got != 4409 {
				t.Fatalf("?protocol=4: CloseStatus = %d, want 4409 (%v)", got, err)
			}
			return
		}
		var f mutevFrame
		_ = json.Unmarshal(raw, &f)
		if f.Type == "hello" || f.Type == "snapshot" {
			t.Fatalf("?protocol=4 got a %s frame before 4409: %s", f.Type, raw)
		}
	}
}

func TestEventsCompat_V2ClosesWith4401WhenTheSessionEnds(t *testing.T) {
	e := newMutevEnv(t)
	gone := e.issueSession(t)
	s := compatDialV2(t, e, gone)
	compatWaitFrames(t, s, 2) // hello + snapshot
	e.mustDo(t, "DELETE", "/session", gone, nil, http.StatusNoContent)
	s.expectClose(t, 4401, "v2: DELETE /session -> 4401")
}

// RV-29: during a complete mutation scenario a protocol-3 socket (all session topics) gets
// the new events, while every frame of the protocol-2 socket is still a valid 001 frame.
func TestEventsCompat_V2NeverReceivesV3Frames(t *testing.T) {
	sch := compileEventsSchema(t)
	e := newMutevEnv(t)
	v2 := compatDialV2(t, e, e.cookie)
	compatWaitFrames(t, v2, 2)

	// apps: create (before the protocol-3 subscription, so the service watcher's first
	// observation of the unit is "active" and the later stop is a change), edit, toggle,
	// rotate, env, args, unit
	secret := "Cv3nW8pQ2tB6nV0rK4zH9sD1fJ5gL7aE"
	id := e.createApp(t, map[string]any{
		"name": "compat-app", "binaryPath": "/opt/compat/app", "serviceName": "compat-app.service",
		"githubRepo": "owner/compat", "artifactName": "compat-linux", "webhookSecret": secret,
		"envVars": []map[string]string{{"name": "COMPAT_ENV", "value": "compat-env-value"}},
		"args":    []string{"--compat-arg"},
	})
	e.systemctl.set("compat-app.service", "active")
	v3 := e.dial(t, e.cookie)
	v3.sub(t, "r1", nil, mutevTopic("summary"), mutevTopic("jobs"), mutevTopic("apps"), mutevTopic("service"),
		mutevTopic("stats"), mutevTopic("settings"), mutevTopic("account"))
	e.mustDo(t, "PUT", "/apps/"+id, e.cookie, map[string]any{
		"name": "compat-app-renamed", "binaryPath": "/opt/compat/app", "serviceName": "compat-app.service",
		"githubRepo": "owner/compat", "artifactName": "compat-linux",
	}, http.StatusOK)
	e.mustDo(t, "POST", "/apps/"+id+"/toggle", e.cookie, map[string]bool{"enabled": false}, http.StatusOK)
	e.mustDo(t, "POST", "/apps/"+id+"/toggle", e.cookie, map[string]bool{"enabled": true}, http.StatusOK)
	e.mustDo(t, "POST", "/apps/"+id+"/webhook-secret/rotate", e.cookie, nil, http.StatusOK)
	e.mustDo(t, "PUT", "/apps/"+id+"/env", e.cookie, map[string]any{"envVars": []map[string]string{{"name": "COMPAT_ENV", "value": "compat-env-value-2"}}}, http.StatusOK)
	e.mustDo(t, "PUT", "/apps/"+id+"/args", e.cookie, map[string]any{"args": []string{"--compat-arg-2"}}, http.StatusOK)
	e.mustDo(t, "POST", "/apps/"+id+"/service-unit/apply", e.cookie, nil, http.StatusOK)

	// jobs: deploy (succeeded), retry (failed), deploy + queued cancel + running cancel
	e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v1.0.0"}, http.StatusAccepted)
	job1 := e.jobID(t, id, "v1.0.0")
	e.waitJob(t, job1, "in_progress")
	e.runner.finish("v1.0.0", nil)
	e.waitJob(t, job1, "succeeded")
	e.mustDo(t, "POST", "/history/"+job1+"/retry", e.cookie, nil, http.StatusAccepted)
	job2 := e.jobID(t, id, "v1.0.0")
	e.waitJob(t, job2, "in_progress")
	e.runner.finish("v1.0.0", errors.New("compat: deploy failed"))
	e.waitJob(t, job2, "failed")
	e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v2.0.0"}, http.StatusAccepted)
	job3 := e.jobID(t, id, "v2.0.0")
	e.waitJob(t, job3, "in_progress")
	e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v3.0.0"}, http.StatusAccepted)
	job4 := e.jobID(t, id, "v3.0.0")
	e.mustDo(t, "POST", "/jobs/"+job4+"/cancel", e.cookie, nil, http.StatusOK)
	e.mustDo(t, "POST", "/apps/"+id+"/cancel", e.cookie, nil, http.StatusOK)
	e.runner.finish("v2.0.0", errors.New("compat: canceled"))

	// service, settings, account
	m := v3.mark()
	e.mustDo(t, "POST", "/apps/"+id+"/service/stop", e.cookie, nil, http.StatusOK)
	// The watcher's round is debounced (250 ms): wait for it before the app goes away.
	v3.expect(t, m, "v3: service.status{inactive} after the stop", mutevServiceStatus(id, "inactive"))
	e.mustDo(t, "POST", "/github", e.cookie, map[string]any{"token": "ghp_compatCompatCompat0123456789abcd"}, http.StatusOK)
	e.mustDo(t, "POST", "/telegram", e.cookie, map[string]any{
		"appId": 12345, "appHash": "compatapphash0123456789abcdef", "botToken": "123456:COMPAT-bot-token", "chatUsername": "@compat", "enabled": true,
	}, http.StatusOK)
	_, created, _ := e.mustDo(t, "POST", "/tokens", e.cookie, map[string]string{"name": "compat-ci"}, http.StatusCreated)
	e.mustDo(t, "DELETE", "/tokens/"+mutevJSONString(t, created, "id"), e.cookie, nil, http.StatusNoContent)

	// apps: delete (last, so the job events above all belong to a live app)
	e.mustDo(t, "DELETE", "/apps/"+id, e.cookie, nil, http.StatusOK)
	v3.expect(t, 0, "v3: app.deleted (the scenario reached its end)", func(f mutevFrame) bool { return f.Type == "app.deleted" && f.AppID == id })
	time.Sleep(300 * time.Millisecond) // anything still in flight to the v2 socket lands

	// The scenario really produced protocol-3 traffic of the new kinds...
	newTypes := map[string]bool{}
	for _, f := range v3.snapshot() {
		if f.Protocol == "3" && f.Topic != "" {
			newTypes[f.Type] = true
		}
	}
	for _, want := range []string{"app.upserted", "app.config_changed", "app.deleted", "summary", "stats.changed", "settings.changed", "account.changed", "service.status", "job_status"} {
		if !newTypes[want] {
			t.Errorf("v3 socket never received %q during the scenario (got %v)", want, newTypes)
		}
	}
	// ...and the v2 socket saw only 001 frames.
	v2Frames := v2.snapshot()
	sawJob := false
	for _, f := range v2Frames {
		raw := string(f.Raw)
		if f.Protocol != "2" && !strings.Contains(raw, `"v":"2"`) {
			t.Errorf("v2 socket received a frame that is not protocol 2: %s", raw)
		}
		if f.Topic != "" || f.Seq != 0 || strings.Contains(raw, `"v":"3"`) {
			t.Errorf("v2 socket received a protocol-3 field (topic/seq/v:3): %s", raw)
		}
		validateEvent(t, sch, raw)
		if f.Type == "job_status" {
			sawJob = true
		}
	}
	if !sawJob {
		t.Errorf("v2 socket received no job_status during the scenario (%d frames)", len(v2Frames))
	}
}
