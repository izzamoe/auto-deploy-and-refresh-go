package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// fullEnv is the whole v1 auth stack (session, account and token endpoints behind
// the real middleware) with a probe route standing in for a data endpoint.
type fullEnv struct {
	h           *server.Hertz
	users       *store.AdminUserStore
	mgr         *auth.Manager
	initialPath string
}

func newFullEnv(t *testing.T, mustChange bool) *fullEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "full.db"))
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
	tokens, err := store.NewAPITokenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.CreateUser("admin", sessionTestPassword, mustChange); err != nil {
		t.Fatal(err)
	}
	mgr, err := auth.NewManager(auth.SessionConfig{Key: bytes.Repeat([]byte{8}, 32), Secure: true}, users, sessions)
	if err != nil {
		t.Fatal(err)
	}
	initialPath := filepath.Join(dir, "initial-admin-password")
	if err := os.WriteFile(initialPath, []byte("generated-initial-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := auth.NewClientIPResolver(nil)
	mw := auth.Middleware(auth.MiddlewareConfig{
		Sessions: mgr, Tokens: tokens, TokenLimiter: auth.NewWindow(10, time.Minute, nil), ClientIP: resolver,
	})
	h := server.New()
	h.Use(httpx.RequestID())
	RegisterSessionRoutesHertz(h, NewSessionHandler(SessionHandlerConfig{
		Sessions: mgr, Users: users, Limiter: auth.NewLoginLimiter(nil), ClientIP: resolver,
	}), mw)
	RegisterAccountRoutesHertz(h, NewAccountHandler(users, mgr, initialPath), mw)
	h.Group("/admin/api/v1", mw).GET("/probe", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(200, map[string]string{"status": "ok"})
	})
	return &fullEnv{h: h, users: users, mgr: mgr, initialPath: initialPath}
}

type fullResp struct {
	status int
	body   string
	cookie string // value of our session cookie in Set-Cookie ("" if absent or cleared)
	set    bool   // whether Set-Cookie touched the session cookie at all
	hdr    func(string) string
}

func (e *fullEnv) send(method, path string, headers map[string]string, body string) fullResp {
	var hs []ut.Header
	for k, v := range headers {
		hs = append(hs, ut.Header{Key: k, Value: v})
	}
	var b *ut.Body
	if body != "" {
		b = &ut.Body{Body: strings.NewReader(body), Len: len(body)}
	}
	w := ut.PerformRequest(e.h.Engine, method, "http://app.test"+path, b, hs...)
	r := w.Result()
	out := fullResp{status: r.StatusCode(), body: string(r.Body()), hdr: func(k string) string { return string(r.Header.Peek(k)) }}
	// VisitAllCookie yields the cookie name and its full Set-Cookie serialization
	// ("name=value; max-age=...; path=/; ..."); keep only the value.
	r.Header.VisitAllCookie(func(k, v []byte) {
		if string(k) == e.mgr.CookieName() {
			first, _, _ := strings.Cut(string(v), ";")
			out.set, out.cookie = true, strings.TrimPrefix(first, e.mgr.CookieName()+"=")
		}
	})
	return out
}

// browser builds the headers a same-origin SPA fetch sends.
func (e *fullEnv) browser(cookie string) map[string]string {
	h := map[string]string{"X-Admin-Request": "true", "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}
	if cookie != "" {
		h["Cookie"] = e.mgr.CookieName() + "=" + cookie
	}
	return h
}

func (e *fullEnv) loginAs(t *testing.T, user, pass string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	r := e.send("POST", "/admin/api/v1/session", e.browser(""), string(body))
	if r.status != 200 || r.cookie == "" {
		t.Fatalf("login(%s): status %d cookie=%q body=%s", user, r.status, r.cookie, r.body)
	}
	return r.cookie
}

func (e *fullEnv) call(method, path, cookie, body string) fullResp {
	return e.send(method, path, e.browser(cookie), body)
}

func errCode(t *testing.T, r fullResp) string {
	t.Helper()
	var eb struct{ Code string }
	if err := json.Unmarshal([]byte(r.body), &eb); err != nil {
		t.Fatalf("not an error body (status %d): %s", r.status, r.body)
	}
	return eb.Code
}

func pwBody(current, next string) string {
	b, _ := json.Marshal(map[string]string{"currentPassword": current, "newPassword": next})
	return string(b)
}

func TestAccount_ChangePasswordLiftsGateAndReissuesSession(t *testing.T) {
	e := newFullEnv(t, true)
	cookie1 := e.loginAs(t, "admin", sessionTestPassword)

	// Under the must-change gate data endpoints are blocked, account endpoints open.
	if r := e.call("GET", "/admin/api/v1/probe", cookie1, ""); r.status != 403 || errCode(t, r) != "password_change_required" {
		t.Fatalf("gated data route: %d %s", r.status, r.body)
	}
	r := e.call("GET", "/admin/api/v1/account", cookie1, "")
	if r.status != 200 || !strings.Contains(r.body, `"mustChangePassword":true`) {
		t.Fatalf("account route under the gate: %d %s", r.status, r.body)
	}

	r = e.call("POST", "/admin/api/v1/account/password", cookie1, pwBody(sessionTestPassword, "new-strong-pass"))
	if r.status != 200 {
		t.Fatalf("change password: %d %s", r.status, r.body)
	}
	cookie2 := r.cookie
	if cookie2 == "" || cookie2 == cookie1 {
		t.Fatalf("password change must reissue the current session cookie (got %q)", cookie2)
	}
	if _, err := os.Stat(e.initialPath); !os.IsNotExist(err) {
		t.Errorf("initial-admin-password file must be deleted after the change (stat err = %v)", err)
	}

	// B4: the pre-change cookie is dead; the reissued one works and the gate is open.
	if r := e.call("GET", "/admin/api/v1/probe", cookie1, ""); r.status != 401 || errCode(t, r) != "session_revoked" {
		t.Errorf("pre-change cookie: %d %s, want 401 session_revoked", r.status, r.body)
	}
	if r := e.call("GET", "/admin/api/v1/probe", cookie2, ""); r.status != 200 {
		t.Errorf("reissued cookie after the change: %d %s, want 200", r.status, r.body)
	}
	// The new password works for login, the old one does not.
	e.loginAs(t, "admin", "new-strong-pass")
}

func TestAccount_WrongCurrentAndInvalidInput(t *testing.T) {
	e := newFullEnv(t, false)
	cookie := e.loginAs(t, "admin", sessionTestPassword)

	if r := e.call("POST", "/admin/api/v1/account/password", cookie, pwBody("nope", "new-strong-pass")); r.status != 401 || errCode(t, r) != "unauthorized" {
		t.Errorf("wrong current password: %d %s, want 401 unauthorized", r.status, r.body)
	}
	r := e.call("POST", "/admin/api/v1/account/password", cookie, pwBody(sessionTestPassword, "x"))
	if r.status != 400 || errCode(t, r) != "validation_failed" || !strings.Contains(r.body, `"field":"newPassword"`) {
		t.Errorf("short new password: %d %s, want 400 validation_failed on newPassword", r.status, r.body)
	}
	if r := e.call("POST", "/admin/api/v1/account/password", cookie, `{broken`); r.status != 400 || errCode(t, r) != "invalid_json" {
		t.Errorf("bad json: %d %s", r.status, r.body)
	}
	if r := e.call("POST", "/admin/api/v1/account/username", cookie, `{"currentPassword":"`+sessionTestPassword+`","newUsername":"bad`+"\\n"+`name"}`); r.status != 400 {
		t.Errorf("control character in username: %d %s, want 400", r.status, r.body)
	}
	// Nothing above may have invalidated the session.
	if r := e.call("GET", "/admin/api/v1/probe", cookie, ""); r.status != 200 {
		t.Errorf("failed attempts must not revoke the session: %d", r.status)
	}
}

func TestChangeUsernameReissuesCurrentSession(t *testing.T) {
	e := newFullEnv(t, false)
	cookieA := e.loginAs(t, "admin", sessionTestPassword)
	cookieOther := e.loginAs(t, "admin", sessionTestPassword) // a second browser

	body := `{"currentPassword":"` + sessionTestPassword + `","newUsername":"  boss  "}`
	r := e.call("POST", "/admin/api/v1/account/username", cookieA, body)
	if r.status != 200 {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	if !r.set || r.cookie == "" || r.cookie == cookieA {
		t.Fatalf("changeUsername must reissue the current session (Set-Cookie): set=%v cookie=%q", r.set, r.cookie)
	}
	cookieNew := r.cookie

	if r := e.call("GET", "/admin/api/v1/probe", cookieNew, ""); r.status != 200 {
		t.Errorf("reissued session after rename: %d, want 200", r.status)
	}
	r = e.call("GET", "/admin/api/v1/account", cookieNew, "")
	if r.status != 200 || !strings.Contains(r.body, `"username":"boss"`) {
		t.Errorf("account after rename: %d %s", r.status, r.body)
	}
	// Every other session (old cookie of this browser, the other browser) is revoked.
	for name, ck := range map[string]string{"pre-rename cookie": cookieA, "other browser": cookieOther} {
		if r := e.call("GET", "/admin/api/v1/probe", ck, ""); r.status != 401 || errCode(t, r) != "session_revoked" {
			t.Errorf("%s: %d %s, want 401 session_revoked", name, r.status, r.body)
		}
	}
	// Login follows the new name.
	e.loginAs(t, "boss", sessionTestPassword)

	// 409 on a taken username.
	if _, err := e.users.CreateUser("taken", "another-password", false); err != nil {
		t.Fatal(err)
	}
	r = e.call("POST", "/admin/api/v1/account/username", cookieNew, `{"currentPassword":"`+sessionTestPassword+`","newUsername":"taken"}`)
	if r.status != 409 || errCode(t, r) != "duplicate" {
		t.Errorf("duplicate username: %d %s, want 409 duplicate", r.status, r.body)
	}
}

// B3: renaming while the default password is still in place must not open the gate.
func TestAccount_RenameKeepsMustChangeGate(t *testing.T) {
	e := newFullEnv(t, true)
	cookie1 := e.loginAs(t, "admin", sessionTestPassword)
	r := e.call("POST", "/admin/api/v1/account/username", cookie1, `{"currentPassword":"`+sessionTestPassword+`","newUsername":"renamed"}`)
	if r.status != 200 || r.cookie == "" {
		t.Fatalf("rename under the gate: %d %s (cookie %q)", r.status, r.body, r.cookie)
	}
	if r := e.call("GET", "/admin/api/v1/probe", cookie1, ""); r.status == 200 {
		t.Error("B3: pre-rename cookie bypasses the must-change gate")
	}
	if r := e.call("GET", "/admin/api/v1/probe", r.cookie, ""); r.status != 403 {
		t.Errorf("reissued cookie still must be gated: %d, want 403", r.status)
	}
}

func TestAccount_RevokeAllSessionsKeepsCurrent(t *testing.T) {
	e := newFullEnv(t, false)
	a := e.loginAs(t, "admin", sessionTestPassword)
	b := e.loginAs(t, "admin", sessionTestPassword)

	r := e.call("POST", "/admin/api/v1/account/sessions/revoke-all", a, "")
	if r.status != 200 || r.cookie == "" {
		t.Fatalf("revoke-all: %d %s (cookie %q)", r.status, r.body, r.cookie)
	}
	if r := e.call("GET", "/admin/api/v1/probe", b, ""); r.status != 401 || errCode(t, r) != "session_revoked" {
		t.Errorf("other session after revoke-all: %d %s", r.status, r.body)
	}
	if r := e.call("GET", "/admin/api/v1/probe", a, ""); r.status != 401 {
		t.Errorf("the calling session's old cookie must be replaced: %d", r.status)
	}
	if r2 := e.call("GET", "/admin/api/v1/probe", r.cookie, ""); r2.status != 200 {
		t.Errorf("the reissued cookie must keep working: %d", r2.status)
	}
}

func TestAccount_CurrentPasswordGuessesAreRateLimited(t *testing.T) {
	e := newFullEnv(t, false)
	cookie := e.loginAs(t, "admin", sessionTestPassword)
	for i := 0; i < 5; i++ {
		if r := e.call("POST", "/admin/api/v1/account/password", cookie, pwBody("wrong"+strconv.Itoa(i), "new-strong-pass")); r.status != 401 {
			t.Fatalf("guess %d: %d, want 401", i+1, r.status)
		}
	}
	r := e.call("POST", "/admin/api/v1/account/password", cookie, pwBody(sessionTestPassword, "new-strong-pass"))
	if r.status != 429 || r.hdr("Retry-After") == "" || errCode(t, r) != "rate_limited" {
		t.Errorf("6th attempt: %d %s (Retry-After %q), want 429 rate_limited", r.status, r.body, r.hdr("Retry-After"))
	}
}
