package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/izzamoe/auto-deploy/internal/httpx"
)

// NFR-006 / T117: security-relevant actions leave a structured audit line with the event
// name, the request ID, the user and the client IP, and never a secret.

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) lines() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s.b.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}
func (s *syncBuffer) raw() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

func auditEngine(handler app.HandlerFunc) *server.Hertz {
	h := server.New(server.WithHostPorts("127.0.0.1:0"), server.WithDisablePrintRoute(true))
	h.Use(httpx.RequestID())
	h.POST("/x", handler)
	return h
}

func TestAudit_RequestEventCarriesRequestIDUserAndIP(t *testing.T) {
	buf := captureLogs(t)
	h := auditEngine(func(ctx context.Context, c *app.RequestContext) {
		AuditRequest(c, EventLoginSucceeded, "user-42", "note", "hello")
		c.Status(204)
	})
	resp := ut.PerformRequest(h.Engine, "POST", "/x", nil).Result()
	rid := string(resp.Header.Peek("X-Request-ID"))
	if rid == "" {
		t.Fatal("no request ID on the response")
	}
	var audit map[string]any
	for _, l := range buf.lines() {
		if l["event"] == EventLoginSucceeded {
			audit = l
		}
	}
	if audit == nil {
		t.Fatalf("no audit line was logged; output: %s", buf.raw())
	}
	if audit["requestId"] != rid {
		t.Errorf("requestId = %v, want %q", audit["requestId"], rid)
	}
	if audit["userId"] != "user-42" || audit["note"] != "hello" {
		t.Errorf("audit line = %v", audit)
	}
	if ip, _ := audit["ip"].(string); ip == "" {
		t.Errorf("an audit line must carry the client ip: %v", audit)
	}
	if audit["msg"] != "audit" {
		t.Errorf("msg = %v, want audit (a stable key for log filters)", audit["msg"])
	}
}

func TestAudit_NeverLogsSecretValues(t *testing.T) {
	buf := captureLogs(t)
	h := auditEngine(func(ctx context.Context, c *app.RequestContext) {
		AuditRequest(c, EventTokenCreated, "u1",
			"tokenId", "tok-1", "tokenName", "ci",
			"password", "hunter2-PASSWORD", "token", "adt_SECRETTOKENVALUE", "webhookSecret", "WEBHOOK-SECRET-VALUE",
			"Authorization", "Bearer BEARER-VALUE", "newPassword", "NEW-PASSWORD-VALUE")
		c.Status(204)
	})
	ut.PerformRequest(h.Engine, "POST", "/x", nil)
	out := buf.raw()
	for _, secret := range []string{"hunter2-PASSWORD", "adt_SECRETTOKENVALUE", "WEBHOOK-SECRET-VALUE", "BEARER-VALUE", "NEW-PASSWORD-VALUE"} {
		if strings.Contains(out, secret) {
			t.Errorf("the audit log leaked %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "tok-1") || !strings.Contains(out, `"tokenName":"ci"`) {
		t.Errorf("non-secret identifiers must be kept: %s", out)
	}
}

func TestAudit_BackgroundEventsHaveNoRequestID(t *testing.T) {
	buf := captureLogs(t)
	AuditBackground(EventDeployRollback, "appId", "a1", "jobId", "j1")
	var found bool
	for _, l := range buf.lines() {
		if l["event"] == EventDeployRollback && l["appId"] == "a1" && l["jobId"] == "j1" && l["msg"] == "audit" {
			found = true
		}
	}
	if !found {
		t.Errorf("no deploy.rollback audit line: %s", buf.raw())
	}
}

func TestAudit_EventNamesAreStable(t *testing.T) {
	want := []string{
		"auth.login_succeeded", "auth.login_failed", "auth.rate_limited", "auth.logout", "auth.password_changed",
		"auth.username_changed", "auth.sessions_revoked", "auth.token_created", "auth.token_revoked",
		"app.created", "app.updated", "app.deleted", "deploy.manual", "deploy.cancel", "deploy.rollback",
	}
	got := []string{EventLoginSucceeded, EventLoginFailed, EventRateLimited, EventLogout, EventPasswordChanged, EventUsernameChanged,
		EventSessionsRevoked, EventTokenCreated, EventTokenRevoked, EventAppCreated, EventAppUpdated, EventAppDeleted,
		EventDeployManual, EventDeployCancel, EventDeployRollback}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}
