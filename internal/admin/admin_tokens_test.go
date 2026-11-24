package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
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

// tokenEnv is the token endpoints behind the real auth middleware, with a probe
// route standing in for any data endpoint.
type tokenEnv struct {
	h      *server.Hertz
	cookie string // valid session cookie of the single admin
}

func newTokenEnv(t *testing.T) *tokenEnv {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
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
	user, err := users.CreateUser("admin", "correct-horse-battery", false)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := auth.NewManager(auth.SessionConfig{Key: bytes.Repeat([]byte{3}, 32), Secure: true}, users, sessions)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := mgr.Issue(user, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	mw := auth.Middleware(auth.MiddlewareConfig{
		Sessions: mgr, Tokens: tokens, TokenLimiter: auth.NewWindow(10, time.Minute, nil), ClientIP: auth.NewClientIPResolver(nil),
	})
	h := server.New()
	h.Use(httpx.RequestID())
	RegisterTokenRoutesHertz(h, NewTokensHandler(tokens), mw)
	h.Group("/admin/api/v1", mw).GET("/probe", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(200, map[string]string{"ok": "yes"})
	})
	return &tokenEnv{h: h, cookie: mgr.CookieName() + "=" + iss.Token}
}

func (e *tokenEnv) call(method, path string, hdr map[string]string, body string) (int, string) {
	var hs []ut.Header
	for k, v := range hdr {
		hs = append(hs, ut.Header{Key: k, Value: v})
	}
	var b *ut.Body
	if body != "" {
		b = &ut.Body{Body: strings.NewReader(body), Len: len(body)}
	}
	w := ut.PerformRequest(e.h.Engine, method, "http://app.test"+path, b, hs...)
	r := w.Result()
	return r.StatusCode(), string(r.Body())
}

func (e *tokenEnv) cookieMutation() map[string]string {
	return map[string]string{
		"Cookie": e.cookie, "X-Admin-Request": "true", "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json",
	}
}

func (e *tokenEnv) createToken(t *testing.T, name string) (id, full string) {
	t.Helper()
	st, body := e.call("POST", "/admin/api/v1/tokens", e.cookieMutation(), `{"name":"`+name+`"}`)
	if st != 201 {
		t.Fatalf("create token: status %d body %s", st, body)
	}
	var out struct{ ID, Token string }
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Token == "" || out.ID == "" {
		t.Fatalf("create token response: %v %s", err, body)
	}
	return out.ID, out.Token
}

func TestAPIToken_CreateReturnsFullValueOnlyOnce(t *testing.T) {
	e := newTokenEnv(t)
	st, body := e.call("POST", "/admin/api/v1/tokens", e.cookieMutation(), `{"name":"  ci runner  "}`)
	if st != 201 {
		t.Fatalf("status = %d body = %s", st, body)
	}
	var created struct {
		ID, Name, Prefix, Token string
		CreatedAt               string
		LastUsedAt, RevokedAt   *string
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "adt_") || len(created.Token) < 40 {
		t.Errorf("token value = %q, want adt_ + 32 random bytes", created.Token)
	}
	if created.Name != "ci runner" || created.Prefix != strings.TrimPrefix(created.Token, "adt_")[:8] {
		t.Errorf("name/prefix = %q/%q", created.Name, created.Prefix)
	}
	if created.LastUsedAt != nil || created.RevokedAt != nil || created.CreatedAt == "" {
		t.Errorf("fresh token fields wrong: %s", body)
	}

	st, list := e.call("GET", "/admin/api/v1/tokens", map[string]string{"Cookie": e.cookie}, "")
	if st != 200 {
		t.Fatalf("list status = %d %s", st, list)
	}
	if strings.Contains(list, created.Token) || strings.Contains(strings.ToLower(list), `"token"`) {
		t.Errorf("list must never contain the token value: %s", list)
	}
	if !strings.Contains(list, created.ID) || !strings.Contains(list, created.Prefix) {
		t.Errorf("list must show id and prefix: %s", list)
	}
}

func TestAPIToken_Validation(t *testing.T) {
	e := newTokenEnv(t)
	for name, body := range map[string]string{
		"empty":      `{"name":""}`,
		"blank":      `{"name":"   "}`,
		"missing":    `{}`,
		"too long":   `{"name":"` + strings.Repeat("x", 65) + `"}`,
		"not a json": `nope`,
	} {
		st, resp := e.call("POST", "/admin/api/v1/tokens", e.cookieMutation(), body)
		if st != 400 {
			t.Errorf("%s: status = %d, want 400 (%s)", name, st, resp)
			continue
		}
		var eb struct{ Code string }
		_ = json.Unmarshal([]byte(resp), &eb)
		want := "validation_failed"
		if name == "not a json" {
			want = "invalid_json"
		}
		if eb.Code != want {
			t.Errorf("%s: code = %q, want %q", name, eb.Code, want)
		}
	}
	// 64 characters is allowed.
	if st, resp := e.call("POST", "/admin/api/v1/tokens", e.cookieMutation(), `{"name":"`+strings.Repeat("y", 64)+`"}`); st != 201 {
		t.Errorf("64-char name: status = %d (%s)", st, resp)
	}
}

func TestAPIToken_UsableThenRevoked(t *testing.T) {
	e := newTokenEnv(t)
	id, token := e.createToken(t, "deploy bot")

	if st, _ := e.call("GET", "/admin/api/v1/probe", map[string]string{"Authorization": "Bearer " + token}, ""); st != 200 {
		t.Fatalf("Bearer token on a data endpoint: status = %d, want 200", st)
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("any-user:"+token))
	if st, _ := e.call("GET", "/admin/api/v1/probe", map[string]string{"Authorization": basic}, ""); st != 200 {
		t.Fatalf("Basic with the token as password: status = %d, want 200", st)
	}
	// Q-6: a token never manages tokens or the account.
	if st, _ := e.call("GET", "/admin/api/v1/tokens", map[string]string{"Authorization": "Bearer " + token}, ""); st != 403 {
		t.Errorf("token listing tokens: status = %d, want 403", st)
	}
	if st, _ := e.call("POST", "/admin/api/v1/tokens", map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}, `{"name":"escalate"}`); st != 403 {
		t.Errorf("token creating tokens: status = %d, want 403", st)
	}

	// Revoke through the session (CSRF headers required).
	if st, body := e.call("DELETE", "/admin/api/v1/tokens/"+id, map[string]string{"Cookie": e.cookie}, ""); st != 403 {
		t.Errorf("revoke without CSRF headers: status = %d (%s), want 403", st, body)
	}
	if st, body := e.call("DELETE", "/admin/api/v1/tokens/"+id, e.cookieMutation(), ""); st != 204 {
		t.Fatalf("revoke: status = %d (%s), want 204", st, body)
	}
	if st, _ := e.call("GET", "/admin/api/v1/probe", map[string]string{"Authorization": "Bearer " + token}, ""); st != 401 {
		t.Errorf("revoked token: status = %d, want 401", st)
	}
	if st, _ := e.call("DELETE", "/admin/api/v1/tokens/"+id, e.cookieMutation(), ""); st != 204 {
		t.Errorf("revoking twice must stay 204, got %d", st)
	}
	if st, _ := e.call("DELETE", "/admin/api/v1/tokens/does-not-exist", e.cookieMutation(), ""); st != 404 {
		t.Errorf("unknown token id: status = %d, want 404", st)
	}
	_, list := e.call("GET", "/admin/api/v1/tokens", map[string]string{"Cookie": e.cookie}, "")
	var out struct {
		Tokens []struct {
			ID        string
			RevokedAt *string
		}
	}
	if err := json.Unmarshal([]byte(list), &out); err != nil || len(out.Tokens) != 1 || out.Tokens[0].RevokedAt == nil {
		t.Errorf("list must keep the revoked token with revokedAt set: %v %s", err, list)
	}
}

// B5: the admin password is never an API credential, whether sent as Bearer or Basic.
func TestAPIToken_BasicAdminPasswordRejected(t *testing.T) {
	e := newTokenEnv(t)
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:correct-horse-battery"))
	if st, _ := e.call("GET", "/admin/api/v1/probe", map[string]string{"Authorization": basic}, ""); st != 401 {
		t.Errorf("Basic with the admin password: status = %d, want 401", st)
	}
	if st, _ := e.call("GET", "/admin/api/v1/probe", map[string]string{"Authorization": "Bearer correct-horse-battery"}, ""); st != 401 {
		t.Errorf("Bearer with the admin password: status = %d, want 401", st)
	}
}
