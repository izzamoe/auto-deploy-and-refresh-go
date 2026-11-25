package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/store"
)

const apiBase = "http://app.test/admin/api/v1"

type apiEnv struct {
	*testEnv
	h *server.Hertz
}

// newAPIEnv mounts the middleware on a v1-like group with a handful of probe routes.
func newAPIEnv(t *testing.T, mustChange bool) *apiEnv {
	t.Helper()
	e := newTestEnv(t, mustChange)
	h := server.New()
	h.Use(httpx.RequestID())
	mw := Middleware(MiddlewareConfig{
		Sessions:     e.mgr,
		Tokens:       e.be,
		TokenLimiter: NewWindow(10, time.Minute, e.clk.Now),
		ClientIP:     NewClientIPResolver(nil),
		Now:          e.clk.Now,
	})
	probe := func(ctx context.Context, c *app.RequestContext) {
		p, ok := PrincipalFrom(c)
		if !ok {
			c.JSON(500, map[string]string{"error": "no principal"})
			return
		}
		c.JSON(200, map[string]string{"user": p.Username, "kind": p.Kind})
	}
	api := h.Group("/admin/api/v1", mw)
	api.GET("/apps", probe)
	api.POST("/apps", probe)
	api.GET("/account", probe)
	api.POST("/account/username", probe)
	api.GET("/session", probe)
	api.GET("/tokens", probe)
	return &apiEnv{testEnv: e, h: h}
}

type resp struct {
	status int
	body   string
	hdr    func(string) string
}

type errBody struct {
	Status string            `json:"status"`
	Code   string            `json:"code"`
	Error  string            `json:"error"`
	Errors []json.RawMessage `json:"errors"`
}

func (r resp) errBody(t *testing.T) errBody {
	t.Helper()
	var eb errBody
	if err := json.Unmarshal([]byte(r.body), &eb); err != nil {
		t.Fatalf("response is not the JSON error contract: %v (status %d body %q)", err, r.status, r.body)
	}
	return eb
}

func (e *apiEnv) do(method, path string, hdrs map[string]string, body string) resp {
	var hs []ut.Header
	for k, v := range hdrs {
		hs = append(hs, ut.Header{Key: k, Value: v})
	}
	var b *ut.Body
	if body != "" {
		b = &ut.Body{Body: strings.NewReader(body), Len: len(body)}
	}
	w := ut.PerformRequest(e.h.Engine, method, apiBase+path, b, hs...)
	r := w.Result()
	return resp{status: r.StatusCode(), body: string(r.Body()), hdr: func(k string) string { return string(r.Header.Peek(k)) }}
}

func (e *apiEnv) cookie(iss *Issued) string { return e.mgr.CookieName() + "=" + iss.Token }

func sameOrigin(extra map[string]string) map[string]string {
	h := map[string]string{"X-Admin-Request": "true", "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func TestNoWWWAuthenticateWithoutBasic(t *testing.T) {
	e := newAPIEnv(t, false)

	r := e.do("GET", "/apps", nil, "")
	if r.status != 401 {
		t.Fatalf("no credentials: status = %d, want 401", r.status)
	}
	if got := r.hdr("WWW-Authenticate"); got != "" {
		t.Errorf("K1: 401 without Authorization carries WWW-Authenticate=%q", got)
	}
	if eb := r.errBody(t); eb.Code != "session_expired" {
		t.Errorf("code = %q, want session_expired", eb.Code)
	}

	r = e.do("GET", "/apps", map[string]string{"Authorization": "Bearer adt_notatoken"}, "")
	if r.status != 401 || r.hdr("WWW-Authenticate") != "" {
		t.Errorf("bad Bearer: status=%d WWW-Authenticate=%q (want 401 without challenge)", r.status, r.hdr("WWW-Authenticate"))
	}
	if eb := r.errBody(t); eb.Code != "unauthorized" {
		t.Errorf("bad Bearer code = %q, want unauthorized", eb.Code)
	}

	r = e.do("GET", "/apps", map[string]string{"Cookie": e.mgr.CookieName() + "=garbage"}, "")
	if r.status != 401 || r.hdr("WWW-Authenticate") != "" {
		t.Errorf("garbage cookie: status=%d WWW-Authenticate=%q", r.status, r.hdr("WWW-Authenticate"))
	}
}

func TestExpiredSession401JSONCode(t *testing.T) {
	e := newAPIEnv(t, false)
	iss := e.issue(t)
	if r := e.do("GET", "/apps", map[string]string{"Cookie": e.cookie(iss)}, ""); r.status != 200 {
		t.Fatalf("valid session: status = %d body=%s", r.status, r.body)
	}

	e.clk.Advance(8 * 24 * time.Hour)
	r := e.do("GET", "/apps", map[string]string{"Cookie": e.cookie(iss)}, "")
	if r.status != 401 {
		t.Fatalf("expired session: status = %d, want 401", r.status)
	}
	if ct := r.hdr("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	eb := r.errBody(t)
	if eb.Status != "error" || eb.Code != "session_expired" || eb.Error == "" || eb.Errors == nil {
		t.Errorf("error contract violated: %+v", eb)
	}
	if r.hdr("WWW-Authenticate") != "" {
		t.Error("expired session must not trigger a Basic challenge")
	}

	// A revoked session reports session_revoked instead.
	e2 := newAPIEnv(t, false)
	iss2 := e2.issue(t)
	if err := e2.mgr.Revoke(iss2.SessionID); err != nil {
		t.Fatal(err)
	}
	r = e2.do("GET", "/apps", map[string]string{"Cookie": e2.cookie(iss2)}, "")
	if r.status != 401 || r.errBody(t).Code != "session_revoked" {
		t.Errorf("revoked session: status=%d body=%s (want 401 session_revoked)", r.status, r.body)
	}
}

func TestUnknownUserFailsClosed(t *testing.T) {
	e := newAPIEnv(t, false)
	iss := e.issue(t)
	e.be.mu.Lock()
	delete(e.be.users, "u1")
	e.be.mu.Unlock()
	r := e.do("GET", "/apps", map[string]string{"Cookie": e.cookie(iss)}, "")
	if r.status != 401 {
		t.Fatalf("session of a deleted user: status = %d, want 401 (fail closed); body=%s", r.status, r.body)
	}
	if c := r.errBody(t).Code; c != "session_revoked" && c != "session_expired" {
		t.Errorf("code = %q, want session_revoked or session_expired", c)
	}

	// A database failure while loading the user must also fail closed, never pass the request.
	e2 := newAPIEnv(t, false)
	iss2 := e2.issue(t)
	e2.be.mu.Lock()
	e2.be.userErr = errors.New("database is locked")
	e2.be.mu.Unlock()
	r = e2.do("GET", "/apps", map[string]string{"Cookie": e2.cookie(iss2)}, "")
	if r.status != 401 {
		t.Fatalf("user lookup error: status = %d, want 401 (fail closed); body=%s", r.status, r.body)
	}
	if strings.Contains(r.body, "database is locked") {
		t.Error("internal error text leaked to the client")
	}
}

func TestRenameDoesNotBypassMustChangeGate(t *testing.T) {
	e := newAPIEnv(t, true)
	iss1 := e.issue(t)
	cookie1 := e.cookie(iss1)
	ck := map[string]string{"Cookie": cookie1}

	r := e.do("GET", "/apps", ck, "")
	if r.status != 403 || r.errBody(t).Code != "password_change_required" {
		t.Fatalf("gated data endpoint: status=%d body=%s (want 403 password_change_required)", r.status, r.body)
	}
	for _, p := range []string{"/account", "/session"} {
		if r := e.do("GET", p, ck, ""); r.status != 200 {
			t.Errorf("%s must stay reachable under the gate, got %d", p, r.status)
		}
	}

	// Rename: username changes and token_version is bumped (what the account handler does).
	e.be.setUser(func(u *store.AdminUser) { u.Username = "renamed"; u.TokenVersion++ })
	r = e.do("GET", "/apps", ck, "")
	if r.status != 401 || r.errBody(t).Code != "session_revoked" {
		t.Errorf("B3: pre-rename token after rename: status=%d body=%s (want 401 session_revoked)", r.status, r.body)
	}

	// The reissued session is valid but the gate is still evaluated from the user row.
	u, _ := e.be.GetByID("u1")
	again, err := e.mgr.Reissue(iss1.SessionID, u)
	if err != nil {
		t.Fatalf("Reissue: %v", err)
	}
	ck2 := map[string]string{"Cookie": e.cookie(again)}
	if r := e.do("GET", "/apps", ck2, ""); r.status != 403 {
		t.Errorf("reissued session while password still default: status = %d, want 403", r.status)
	}
	e.be.setUser(func(u *store.AdminUser) { u.MustChangePassword = false })
	if r := e.do("GET", "/apps", ck2, ""); r.status != 200 {
		t.Errorf("after the password change the gate must open, got %d", r.status)
	}
}

func TestCSRFRejectsMissingHeader(t *testing.T) {
	e := newAPIEnv(t, false)
	ck := e.cookie(e.issue(t))

	h := sameOrigin(map[string]string{"Cookie": ck})
	delete(h, "X-Admin-Request")
	r := e.do("POST", "/apps", h, `{"a":1}`)
	if r.status != 403 || r.errBody(t).Code != "csrf_rejected" {
		t.Errorf("mutation without X-Admin-Request: status=%d body=%s (want 403 csrf_rejected)", r.status, r.body)
	}
	h["X-Admin-Request"] = "false"
	if r := e.do("POST", "/apps", h, `{"a":1}`); r.status != 403 {
		t.Errorf("X-Admin-Request: false must be rejected, got %d", r.status)
	}
	h["X-Admin-Request"] = "true"
	if r := e.do("POST", "/apps", h, `{"a":1}`); r.status != 200 {
		t.Errorf("valid mutation: status = %d body=%s", r.status, r.body)
	}
	// Reads need no CSRF header.
	if r := e.do("GET", "/apps", map[string]string{"Cookie": ck}, ""); r.status != 200 {
		t.Errorf("GET with cookie only: status = %d", r.status)
	}
}

func TestCSRFRejectsForeignOrigin(t *testing.T) {
	e := newAPIEnv(t, false)
	ck := e.cookie(e.issue(t))
	base := func(extra map[string]string) map[string]string {
		h := map[string]string{"X-Admin-Request": "true", "Content-Type": "application/json", "Cookie": ck}
		for k, v := range extra {
			h[k] = v
		}
		return h
	}
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"foreign Origin", base(map[string]string{"Origin": "https://evil.example"}), 403},
		{"Sec-Fetch-Site cross-site", base(map[string]string{"Sec-Fetch-Site": "cross-site"}), 403},
		{"Sec-Fetch-Site same-site", base(map[string]string{"Sec-Fetch-Site": "same-site"}), 403},
		{"neither Origin nor Sec-Fetch-Site", base(nil), 403},
		{"Origin equals Host", base(map[string]string{"Origin": "http://app.test"}), 200},
		{"Origin equals Host (https at the edge, scheme ignored)", base(map[string]string{"Origin": "https://app.test"}), 200},
		{"Sec-Fetch-Site same-origin", base(map[string]string{"Sec-Fetch-Site": "same-origin"}), 200},
		{"cross-site beats a matching Origin", base(map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://app.test"}), 403},
	}
	for _, tc := range cases {
		r := e.do("POST", "/apps", tc.hdr, `{"a":1}`)
		if r.status != tc.want {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, r.status, tc.want, r.body)
		}
		if tc.want == 403 && r.status == 403 && r.errBody(t).Code != "csrf_rejected" {
			t.Errorf("%s: code = %q, want csrf_rejected", tc.name, r.errBody(t).Code)
		}
	}
}

// B16: a CORS-simple request (text/plain) must not reach a handler, even with valid CSRF headers.
func TestCSRFRejectsNonJSONBody(t *testing.T) {
	e := newAPIEnv(t, false)
	ck := e.cookie(e.issue(t))
	h := sameOrigin(map[string]string{"Cookie": ck, "Content-Type": "text/plain"})
	r := e.do("POST", "/apps", h, `{"a":1}`)
	if r.status != 415 || r.errBody(t).Code != "unsupported_media_type" {
		t.Errorf("text/plain mutation: status=%d body=%s (want 415 unsupported_media_type)", r.status, r.body)
	}
	h["Content-Type"] = "application/json; charset=utf-8"
	if r := e.do("POST", "/apps", h, `{"a":1}`); r.status != 200 {
		t.Errorf("application/json with charset must pass, got %d", r.status)
	}
	// A body-less mutation (e.g. DELETE/rotate) has no Content-Type to check.
	delete(h, "Content-Type")
	if r := e.do("POST", "/apps", h, ""); r.status != 200 {
		t.Errorf("body-less mutation must pass, got %d", r.status)
	}
}

func TestBasicAdminPasswordRejected(t *testing.T) {
	e := newAPIEnv(t, false)
	// An admin password sent as Basic is never accepted: it is only ever tried as an API token.
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:correct-horse-battery"))
	r := e.do("GET", "/apps", map[string]string{"Authorization": basic}, "")
	if r.status != 401 {
		t.Fatalf("Basic with an admin password: status = %d, want 401", r.status)
	}
	if got := r.hdr("WWW-Authenticate"); !strings.HasPrefix(got, "Basic ") {
		t.Errorf("script client using Basic must get a challenge, got %q", got)
	}
	if eb := r.errBody(t); eb.Code != "unauthorized" {
		t.Errorf("code = %q, want unauthorized", eb.Code)
	}

	// Failures are rate limited (10/min per IP, Q-4): the 11th is a 429 with Retry-After.
	var last resp
	for i := 0; i < 10; i++ {
		last = e.do("GET", "/apps", map[string]string{"Authorization": basic}, "")
	}
	if last.status != 429 {
		t.Fatalf("after repeated failures status = %d, want 429", last.status)
	}
	if last.hdr("Retry-After") == "" || last.errBody(t).Code != "rate_limited" {
		t.Errorf("429 must carry Retry-After and code rate_limited: %q %s", last.hdr("Retry-After"), last.body)
	}
	// ...and the window frees up again.
	e.clk.Advance(2 * time.Minute)
	if r := e.do("GET", "/apps", map[string]string{"Authorization": basic}, ""); r.status != 401 {
		t.Errorf("after the window: status = %d, want 401", r.status)
	}
}

func TestAPITokenBearerAndBasicAccepted(t *testing.T) {
	e := newAPIEnv(t, false)
	token, id := e.be.addToken(t)

	r := e.do("GET", "/apps", map[string]string{"Authorization": "Bearer " + token}, "")
	if r.status != 200 || !strings.Contains(r.body, `"kind":"token"`) {
		t.Fatalf("Bearer token: status=%d body=%s", r.status, r.body)
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("whoever:"+token))
	if r := e.do("GET", "/apps", map[string]string{"Authorization": basic}, ""); r.status != 200 {
		t.Errorf("Basic with token as password: status = %d body=%s", r.status, r.body)
	}
	// Token requests are exempt from CSRF: no X-Admin-Request / Origin needed.
	if r := e.do("POST", "/apps", map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}, `{"a":1}`); r.status != 200 {
		t.Errorf("token mutation without CSRF headers: status = %d body=%s", r.status, r.body)
	}
	if e.be.used[id].IsZero() {
		t.Error("last_used_at must be recorded for a used token")
	}
	// Q-6: tokens never reach the account, token and session endpoints.
	for _, p := range []string{"/account", "/tokens", "/session"} {
		r := e.do("GET", p, map[string]string{"Authorization": "Bearer " + token}, "")
		if r.status != 403 || r.errBody(t).Code != "forbidden" {
			t.Errorf("token on %s: status=%d body=%s (want 403 forbidden)", p, r.status, r.body)
		}
	}
	// A revoked token stops working immediately.
	e.be.mu.Lock()
	now := time.Now()
	for _, tk := range e.be.tokens {
		tk.RevokedAt = &now
	}
	e.be.mu.Unlock()
	if r := e.do("GET", "/apps", map[string]string{"Authorization": "Bearer " + token}, ""); r.status != 401 {
		t.Errorf("revoked token: status = %d, want 401", r.status)
	}
}

// RV-6/7: credentials cached by an old browser dialog must neither override a valid cookie
// nor re-trigger the native dialog, and must not eat the token rate limit.
func TestCachedBrowserBasicDoesNotTriggerChallenge(t *testing.T) {
	e := newAPIEnv(t, false)
	stale := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:11"))
	ck := e.cookie(e.issue(t))

	// (1) valid cookie wins; Basic ignored; no challenge.
	r := e.do("GET", "/apps", map[string]string{"Cookie": ck, "Authorization": stale}, "")
	if r.status != 200 || r.hdr("WWW-Authenticate") != "" {
		t.Errorf("cookie + stale Basic: status=%d WWW-Authenticate=%q (want 200, none)", r.status, r.hdr("WWW-Authenticate"))
	}
	// (2)/(3) browser request (X-Admin-Request or Sec-Fetch-Site), no cookie: 401 JSON, no challenge.
	for _, hdr := range []map[string]string{
		{"Authorization": stale, "X-Admin-Request": "true"},
		{"Authorization": stale, "Sec-Fetch-Site": "same-origin"},
	} {
		r := e.do("GET", "/apps", hdr, "")
		if r.status != 401 {
			t.Fatalf("stale Basic from a browser, no cookie: status = %d, want 401", r.status)
		}
		if r.hdr("WWW-Authenticate") != "" {
			t.Errorf("stale Basic from a browser must not trigger a challenge, got %q", r.hdr("WWW-Authenticate"))
		}
		if c := r.errBody(t).Code; c != "session_expired" {
			t.Errorf("code = %q, want session_expired", c)
		}
	}
	// None of that counted against the token limiter: a real token still works afterwards.
	for i := 0; i < 15; i++ {
		e.do("GET", "/apps", map[string]string{"Authorization": stale, "X-Admin-Request": "true"}, "")
	}
	token, _ := e.be.addToken(t)
	if r := e.do("GET", "/apps", map[string]string{"Authorization": "Bearer " + token}, ""); r.status != 200 {
		t.Errorf("stale browser Basic exhausted the token limiter: status = %d", r.status)
	}
}
