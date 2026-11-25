package admin

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// T117 (NFR-006): the security-relevant actions of the real handlers leave structured
// audit lines (msg "audit") with the request ID, and never a password, token or secret.

type auditBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (a *auditBuf) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.b.Write(p)
}
func (a *auditBuf) text() string { a.mu.Lock(); defer a.mu.Unlock(); return a.b.String() }

func (a *auditBuf) events() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, l := range strings.Split(a.text(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == "audit" {
			if ev, ok := m["event"].(string); ok {
				out[ev] = m
			}
		}
	}
	return out
}

func captureAudit(t *testing.T) *auditBuf {
	t.Helper()
	buf := &auditBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

func TestAudit_LoginAndLogoutEventsFromTheRealHandlers(t *testing.T) {
	buf := captureAudit(t)
	e := newFullEnv(t, false)

	bad := e.send("POST", "/admin/api/v1/session", e.browser(""), `{"username":"admin","password":"definitely-wrong-pw"}`)
	if bad.status != 401 {
		t.Fatalf("bad login: %d", bad.status)
	}
	cookie := e.loginAs(t, "admin", sessionTestPassword)

	if r := e.call("DELETE", "/admin/api/v1/session", cookie, ""); r.status != 204 {
		t.Fatalf("logout: %d", r.status)
	}

	events := buf.events()
	for _, want := range []string{"auth.login_failed", "auth.login_succeeded", "auth.logout"} {
		ev, ok := events[want]
		if !ok {
			t.Errorf("no %s audit line; log:\n%s", want, buf.text())
			continue
		}
		if rid, _ := ev["requestId"].(string); rid == "" {
			t.Errorf("%s has no requestId: %v", want, ev)
		}
		if ip, _ := ev["ip"].(string); ip == "" {
			t.Errorf("%s has no ip: %v", want, ev)
		}
	}
	if ev := events["auth.login_succeeded"]; ev != nil && ev["userId"] == "" {
		t.Errorf("a successful login must name the user: %v", ev)
	}
	for _, secret := range []string{"definitely-wrong-pw", sessionTestPassword} {
		if strings.Contains(buf.text(), secret) {
			t.Errorf("the log leaked a secret value (%q...)", secret[:6])
		}
	}
}

func TestAudit_AppAndDeployEventsFromTheRealHandlers(t *testing.T) {
	buf := captureAudit(t)
	h, jwt := newAuditAPIHertz(t)
	secret := strings.Repeat("S", 40)
	rr := serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps", appJSONWith(map[string]any{"webhookSecret": secret})))
	var created appSettingsResp
	_ = json.Unmarshal(rr.Response.Body(), &created)
	if rr.Response.StatusCode() != 201 {
		t.Fatalf("create: %d %s", rr.Response.StatusCode(), rr.Response.Body())
	}
	id := created.App.ID
	_ = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps/"+id+"/webhook-secret/rotate", ""))
	_ = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps/"+id+"/deploy", `{"tag":"v1.0.0"}`))
	_ = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "PUT", "/admin/api/v1/apps/"+id, appJSONWith(map[string]any{"name": "renamed"})))
	_ = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "POST", "/admin/api/v1/apps/"+id+"/cancel", `{}`))
	_ = serveAdminAPIHertz(t, h, adminAPIRequestHertz(jwt, "DELETE", "/admin/api/v1/apps/"+id, ""))

	events := buf.events()
	for _, want := range []string{"app.created", "app.secret_rotated", "deploy.manual", "app.updated", "deploy.cancel"} {
		ev, ok := events[want]
		if !ok {
			t.Errorf("no %s audit line", want)
			continue
		}
		if rid, _ := ev["requestId"].(string); rid == "" {
			t.Errorf("%s has no requestId: %v", want, ev)
		}
	}
	if strings.Contains(buf.text(), secret) {
		t.Error("the webhook secret leaked into the log")
	}
}

// newAuditAPIHertz is newTestAdminAPIHertz plus the request-ID middleware the real server
// installs (admin.SetupMiddleware), so audit lines can carry a requestId.
func newAuditAPIHertz(t *testing.T) (*server.Hertz, *testAuth) {
	t.Helper()
	jwt := testJWTHandler(t)
	db := newTestDB(t)
	appStore, err := store.NewAppStore(db)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := store.NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Migrate(); err != nil {
		t.Fatal(err)
	}
	handler := NewAdminAPIHandler(appStore, queue, progress.NewProgressTracker(), cancel.NewCancelService(queue))
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.Use(httpx.RequestID())
	RegisterAdminAPIRoutesHertz(h, handler, jwt.middleware())
	return h, jwt
}

func TestAudit_TokenEventsFromTheRealHandlers(t *testing.T) {
	buf := captureAudit(t)
	e := newTokenEnv(t)
	id, full := e.createToken(t, "ci-audit")
	if st, body := e.call("DELETE", "/admin/api/v1/tokens/"+id, e.cookieMutation(), ""); st != 204 {
		t.Fatalf("revoke: %d %s", st, body)
	}
	events := buf.events()
	for _, want := range []string{"auth.token_created", "auth.token_revoked"} {
		ev, ok := events[want]
		if !ok {
			t.Errorf("no %s audit line; log:\n%s", want, buf.text())
			continue
		}
		if ev["tokenId"] != id {
			t.Errorf("%s lacks the token id: %v", want, ev)
		}
		if rid, _ := ev["requestId"].(string); rid == "" {
			t.Errorf("%s has no requestId: %v", want, ev)
		}
	}
	if strings.Contains(buf.text(), full) {
		t.Error("the token value leaked into the log")
	}
}
