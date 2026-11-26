package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// T085 (FR-005, FR-016, FR-026, NFR-008, Q-6=A): security of the event stream.
//   - no frame of a complete mutation scenario carries a secret value that was sent
//     (substring search of every value), on protocol 3 (all session topics) or 2;
//   - an API token cannot subscribe account, settings, stats or logs:system (Q-6=A) and
//     can subscribe summary, jobs, apps, service;
//   - a foreign Origin is refused at the handshake (protocol 3 as well);
//   - a flood of 100 ops closes the socket with 4429 and the audit lines carry no frame content.
// FR-005 option a (RV-24/RV2-14): the log stream has NO new redaction; it shows what
// GET /system/logs shows (same unit, filter, journalctl source). REPORT.md lists it as a risk.

// secDial opens ?protocol=3 with the given headers and reads the hello.
func secDial(t *testing.T, e *mutevEnv, header http.Header) *mutevSocket {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://"+e.addr+"/admin/events/v1/ws?protocol=3", &websocket.DialOptions{HTTPHeader: header})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial ?protocol=3: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("protocol 3: no hello (close status %d): %v", websocket.CloseStatus(err), err)
	}
	s := &mutevSocket{conn: conn, done: make(chan struct{})}
	if err := json.Unmarshal(raw, &s.Hello); err != nil {
		t.Fatalf("hello is not JSON: %v (%s)", err, raw)
	}
	s.Hello.Raw = raw
	if s.Hello.Type != "hello" || s.Hello.Protocol != "3" {
		t.Fatalf("first frame = %s, want a protocol-3 hello", raw)
	}
	go s.readLoop()
	return s
}

func TestEventsSecurity_NoSecretValueInAnyFrame(t *testing.T) {
	e := newMutevEnv(t)
	v2 := compatDialV2(t, e, e.cookie)
	v3 := e.dial(t, e.cookie)
	v3.sub(t, "r1", nil, mutevTopic("summary"), mutevTopic("jobs"), mutevTopic("apps"), mutevTopic("service"),
		mutevTopic("stats"), mutevTopic("settings"), mutevTopic("account"))

	secrets := map[string]string{
		"webhook secret":     "Sx4qW8eR2tY6uI0oP3aS7dF1gH5jK9lZ",
		"env name":           "SEC_SECRET_NAME_7d1e",
		"env value":          "sec-env-value-0b9c2f7a",
		"env value 2":        "sec-env-value-2-5e8d1c",
		"args value":         "--sec-arg-value-3a6f",
		"args value 2":       "--sec-arg-value-2-8c4b",
		"github token":       "ghp_secSecSecSec0123456789abcdefABCD",
		"telegram bot token": "987654:SEC-bot-token-4f7a2c9e",
		"telegram appHash":   "secapphash0123456789abcdef9876",
	}
	id := e.createApp(t, map[string]any{
		"name": "sec-app", "binaryPath": "/opt/sec/app", "serviceName": "sec-app.service",
		"githubRepo": "owner/sec", "artifactName": "sec-linux", "webhookSecret": secrets["webhook secret"],
		"envVars": []map[string]string{{"name": secrets["env name"], "value": secrets["env value"]}},
		"args":    []string{secrets["args value"]},
	})
	e.mustDo(t, "PUT", "/apps/"+id, e.cookie, map[string]any{
		"name": "sec-app-2", "binaryPath": "/opt/sec/app", "serviceName": "sec-app.service",
		"githubRepo": "owner/sec", "artifactName": "sec-linux",
	}, http.StatusOK)
	_, rotated, _ := e.mustDo(t, "POST", "/apps/"+id+"/webhook-secret/rotate", e.cookie, nil, http.StatusOK)
	secrets["rotated webhook secret"] = mutevJSONString(t, rotated, "secret")
	e.mustDo(t, "PUT", "/apps/"+id+"/env", e.cookie, map[string]any{"envVars": []map[string]string{{"name": secrets["env name"], "value": secrets["env value 2"]}}}, http.StatusOK)
	e.mustDo(t, "PUT", "/apps/"+id+"/args", e.cookie, map[string]any{"args": []string{secrets["args value 2"]}}, http.StatusOK)
	e.mustDo(t, "POST", "/apps/"+id+"/service-unit/apply", e.cookie, nil, http.StatusOK)
	e.webhook(t, secrets["rotated webhook secret"], "v1.0.0")
	job := e.jobID(t, id, "v1.0.0")
	e.waitJob(t, job, "in_progress")
	e.runner.finish("v1.0.0", errors.New("sec: deploy failed"))
	e.waitJob(t, job, "failed")
	e.mustDo(t, "POST", "/github", e.cookie, map[string]any{"token": secrets["github token"]}, http.StatusOK)
	e.mustDo(t, "POST", "/telegram", e.cookie, map[string]any{
		"appId": 4242, "appHash": secrets["telegram appHash"], "botToken": secrets["telegram bot token"], "chatUsername": "@sec", "enabled": true,
	}, http.StatusOK)
	_, created, _ := e.mustDo(t, "POST", "/tokens", e.cookie, map[string]string{"name": "sec-ci"}, http.StatusCreated)
	secrets["api token"] = mutevJSONString(t, created, "token")
	e.mustDo(t, "DELETE", "/tokens/"+mutevJSONString(t, created, "id"), e.cookie, nil, http.StatusNoContent)
	e.mustDo(t, "DELETE", "/apps/"+id, e.cookie, nil, http.StatusOK)
	v3.expect(t, 0, "v3: app.deleted (end of the scenario)", func(f mutevFrame) bool { return f.Type == "app.deleted" && f.AppID == id })
	time.Sleep(300 * time.Millisecond)

	// The scenario produced the events whose payloads could have leaked something.
	for _, want := range []string{"app.upserted", "app.config_changed", "settings.changed", "account.changed", "job_status"} {
		if !v3.has(0, func(f mutevFrame) bool { return f.Type == want }, 0) {
			t.Errorf("v3 socket never received %q: the scenario did not cover it", want)
		}
	}
	mutevNoSecrets(t, []*mutevSocket{v3, v2}, secrets)
	// The session cookie value never travels on the socket either.
	cookieValue := e.cookie[strings.Index(e.cookie, "=")+1:]
	mutevNoSecrets(t, []*mutevSocket{v3, v2}, map[string]string{"session cookie": cookieValue})
}

func TestEventsSecurity_APITokenTopics(t *testing.T) {
	e := newMutevEnv(t)
	_, created, _ := e.mustDo(t, "POST", "/tokens", e.cookie, map[string]string{"name": "sec-topics"}, http.StatusCreated)
	token := mutevJSONString(t, created, "token")
	s := secDial(t, e, http.Header{"Authorization": {"Bearer " + token}})

	all := []string{"summary", "jobs", "apps", "service", "account", "settings", "stats", "logs:system"}
	topics := make([]map[string]any, 0, len(all))
	for _, n := range all {
		topics = append(topics, mutevTopic(n))
	}
	m := s.mark()
	ack := s.sub(t, "r1", nil, topics...)
	accepted := map[string]bool{}
	if list, ok := ack.Payload["topics"].([]any); ok {
		for _, item := range list {
			if o, ok := item.(map[string]any); ok {
				if n, ok := o["name"].(string); ok {
					accepted[n] = true
				}
			}
		}
	}
	for _, n := range []string{"summary", "jobs", "apps", "service"} {
		if !accepted[n] {
			t.Errorf("Q-6=A: an API token must be able to subscribe %q (ack %s)", n, ack.Raw)
		}
	}
	for _, n := range []string{"account", "settings", "stats", "logs:system"} {
		if accepted[n] {
			t.Errorf("Q-6=A: an API token subscribed %q (ack %s)", n, ack.Raw)
		}
		topic := n
		s.expect(t, m, "error{forbidden_topic} for "+n, func(f mutevFrame) bool {
			return f.Type == "error" && f.Payload["code"] == "forbidden_topic" && f.Payload["topic"] == topic
		})
	}
}

func TestEventsSecurity_ForeignOriginRefused(t *testing.T) {
	e := newMutevEnv(t)
	for _, q := range []string{"?protocol=3", "?protocol=2", ""} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		conn, resp, err := websocket.Dial(ctx, "ws://"+e.addr+"/admin/events/v1/ws"+q, &websocket.DialOptions{
			HTTPHeader: http.Header{"Cookie": {e.cookie}, "Origin": {"https://evil.example"}},
		})
		cancel()
		if err == nil {
			_ = conn.CloseNow()
			t.Fatalf("%q: a handshake with Origin https://evil.example must be refused", q)
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%q: foreign origin answered %+v, want 403", q, resp)
		}
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	// The same cookie from the panel's own origin works.
	s := secDial(t, e, http.Header{"Cookie": {e.cookie}, "Origin": {"http://" + e.addr}})
	s.sub(t, "r1", nil, mutevTopic("summary"))
}

func TestEventsSecurity_Flood100OpsCloses4429WithoutFrameContentInAudit(t *testing.T) {
	logs := captureSlog(t)
	e := newMutevEnv(t)
	s := secDial(t, e, http.Header{"Cookie": {e.cookie}})
	clientID, _ := s.Hello.Payload["clientId"].(string)
	const marker = "fl00dM4rk"
	sent := 0
	for i := 1; i <= 100; i++ {
		raw := []byte(fmt.Sprintf(`{"op":"sub","ref":"%s%d","topics":[{"name":"summary"}]}`, marker, i))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := s.conn.Write(ctx, websocket.MessageText, raw)
		cancel()
		if err != nil {
			break // the server closed the socket mid-flood
		}
		sent++
	}
	s.expectClose(t, 4429, fmt.Sprintf("a flood of %d ops", sent))

	out := logs.String()
	for _, want := range []string{"event=ws_policy", "reason=rate_limited", "reason=closed_4429", "clientId=" + clientID} {
		if !strings.Contains(out, want) {
			t.Errorf("FR-026: audit log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, marker) {
		t.Errorf("FR-026: the audit log carries frame content (%s):\n%s", marker, out)
	}
	limited := 0
	for _, f := range s.snapshot() {
		if f.Type == "error" && f.Payload["code"] == "rate_limited" {
			limited++
		}
	}
	if limited < 1 || limited > 3 {
		t.Errorf("rate_limited errors before 4429 = %d, want 1..3 (the third violation closes)", limited)
	}
}
