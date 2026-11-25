// Package regression holds the audit proof tests (reference/audit/proof-tests)
// promoted to regression tests. Every TestRegression_<ID>_* test encodes the
// CORRECT behaviour, so it is RED on the code that still carries the bug and
// turns GREEN when the matching fix lands (see specs/001-hardening-nextjs-redesign).
package regression

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/izzamoe/auto-deploy/internal/admin"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/config"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
	_ "modernc.org/sqlite"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // same as cmd/server/main.go:82
	t.Cleanup(func() { db.Close() })
	return db
}

type fixedResolver struct{ url string }

func (r fixedResolver) ArtifactDownload(ctx context.Context, repo, tag, artifact string) (string, map[string]string, error) {
	return r.url, nil, nil
}

// env is one complete, isolated server: its own database, session key and rate
// limiters, so a test that hammers the login (B5) cannot lock another test out.
type env struct {
	h     *server.Hertz
	apps  *store.AppStore
	users *store.AdminUserStore
	// initialPassword is the random password EnsureInitialAdmin wrote to the
	// initial-admin-password file; the account name is store.InitialAdminUsername.
	initialPassword string
}

// newEnv wires the v1 auth stack exactly like cmd/server/main.go: configuration from
// the real loader (so COOKIE_SECURE has its shipped default), the account created by
// EnsureInitialAdmin, sessions, the auth middleware and the v1 routes.
func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv("COOKIE_SECURE", "") // unset in a real install: the loader's default applies
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	cfg, err := config.LoadServiceConfig()
	if err != nil {
		t.Fatalf("LoadServiceConfig: %v", err)
	}

	db := openDB(t)
	q, err := store.NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	apps, err := store.NewAppStore(db)
	if err != nil {
		t.Fatal(err)
	}
	users, err := store.NewAdminUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
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

	dir := t.TempDir()
	created, passwordPath, err := users.EnsureInitialAdmin(dir)
	if err != nil || !created {
		t.Fatalf("EnsureInitialAdmin: created=%v err=%v", created, err)
	}
	raw, err := os.ReadFile(passwordPath)
	if err != nil {
		t.Fatal(err)
	}
	initial := strings.TrimSpace(string(raw))

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	mgr, err := auth.NewManager(auth.SessionConfig{Key: key, Secure: cfg.CookieSecure}, users, sessions)
	if err != nil {
		t.Fatal(err)
	}
	resolver := auth.NewClientIPResolver(cfg.TrustedProxyCIDRs)
	mw := auth.Middleware(auth.MiddlewareConfig{
		Sessions: mgr, Tokens: tokens, TokenLimiter: auth.NewWindow(auth.TokenFailureMax, auth.TokenFailureWindow, nil), ClientIP: resolver,
	})

	h := server.New()
	admin.SetupMiddleware(h)
	admin.RegisterSessionRoutesHertz(h, admin.NewSessionHandler(admin.SessionHandlerConfig{
		Sessions: mgr, Users: users, Limiter: auth.NewLoginLimiter(nil), ClientIP: resolver,
	}), mw)
	admin.RegisterAccountRoutesHertz(h, admin.NewAccountHandler(users, mgr, passwordPath), mw)
	tracker := progress.NewProgressTracker()
	admin.RegisterAdminAPIRoutesHertz(h, admin.NewAdminAPIHandler(apps, q, tracker, cancel.NewCancelService(q)), mw)
	return &env{h: h, apps: apps, users: users, initialPassword: initial}
}

func cookieFrom(setCookie string) string {
	// "__Host-admin_session=xxx; path=/; ..." -> "__Host-admin_session=xxx"
	return strings.SplitN(setCookie, ";", 2)[0]
}

// browserHeaders are what the SPA's same-origin fetch sends. Without them every
// cookie-authenticated mutation is answered 403 csrf_rejected and a regression test
// would pass or fail for the wrong reason.
func browserHeaders() []ut.Header {
	return []ut.Header{
		{Key: "X-Admin-Request", Value: "true"},
		{Key: "Sec-Fetch-Site", Value: "same-origin"},
		{Key: "Origin", Value: "http://app.test"},
	}
}

// login signs in through POST /admin/api/v1/session (JSON) and returns the status, the
// session cookie ("name=value", empty if none was set) and the raw Set-Cookie header.
func (e *env) login(t *testing.T, user, pass string) (int, string, string) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"username": user, "password": pass})
	if err != nil {
		t.Fatal(err)
	}
	hdrs := append(browserHeaders(), ut.Header{Key: "Content-Type", Value: "application/json"})
	w := ut.PerformRequest(e.h.Engine, "POST", "http://app.test/admin/api/v1/session",
		&ut.Body{Body: bytes.NewReader(payload), Len: len(payload)}, hdrs...)
	r := w.Result()
	sc := string(r.Header.Peek("Set-Cookie"))
	cookie := ""
	if r.StatusCode() == 200 {
		cookie = cookieFrom(sc)
	}
	return r.StatusCode(), cookie, sc
}

// do returns status, body, WWW-Authenticate header and the session cookie from
// Set-Cookie. Requests that carry a cookie also carry the browser CSRF headers.
func (e *env) do(method, path, cookie, ctype, body string) (int, string, string, string) {
	hdrs := []ut.Header{}
	if cookie != "" {
		hdrs = append(hdrs, ut.Header{Key: "Cookie", Value: cookie})
		hdrs = append(hdrs, browserHeaders()...)
	}
	if ctype != "" {
		hdrs = append(hdrs, ut.Header{Key: "Content-Type", Value: ctype})
	}
	var b *ut.Body
	if body != "" {
		b = &ut.Body{Body: strings.NewReader(body), Len: len(body)}
	}
	w := ut.PerformRequest(e.h.Engine, method, "http://app.test"+path, b, hdrs...)
	r := w.Result()
	return r.StatusCode(), string(r.Body()), string(r.Header.Peek("WWW-Authenticate")), cookieFrom(string(r.Header.Peek("Set-Cookie")))
}

// ungatedSession walks the freshly created account (random initial password,
// must-change flag set) through the two calls allowed under the gate, rename and
// password change, and returns the session cookies issued at each step: login, after
// rename, after password change. Every step is asserted, so a test built on it can
// never be green merely because an earlier step silently failed.
func (e *env) ungatedSession(t *testing.T) (cookie1, cookie2, cookie3 string) {
	t.Helper()
	st, cookie1, raw := e.login(t, store.InitialAdminUsername, e.initialPassword)
	if st != 200 || cookie1 == "" {
		t.Fatalf("prerequisite: initial login = %d, Set-Cookie=%q (want 200 and a cookie)", st, raw)
	}
	st, body, _, cookie2 := e.do("POST", "/admin/api/v1/account/username", cookie1, "application/json",
		`{"currentPassword":"`+e.initialPassword+`","newUsername":"renamed"}`)
	if st != 200 {
		t.Fatalf("prerequisite: rename = %d %s (want 200)", st, body)
	}
	if cookie2 == "" {
		t.Fatal("prerequisite: the rename response must carry the reissued session cookie (cookie2 is empty)")
	}
	st, body, _, cookie3 = e.do("POST", "/admin/api/v1/account/password", cookie2, "application/json",
		`{"currentPassword":"`+e.initialPassword+`","newPassword":"newpass123"}`)
	if st != 200 {
		t.Fatalf("prerequisite: password change = %d %s (want 200)", st, body)
	}
	if cookie3 == "" {
		t.Fatal("prerequisite: the password-change response must carry the reissued session cookie (cookie3 is empty)")
	}
	return cookie1, cookie2, cookie3
}
