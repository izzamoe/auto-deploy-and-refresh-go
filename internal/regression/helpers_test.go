// Package regression holds the audit proof tests (reference/audit/proof-tests)
// promoted to regression tests. Every TestRegression_<ID>_* test encodes the
// CORRECT behaviour, so it is RED on the code that still carries the bug and
// turns GREEN when the matching fix lands (see specs/001-hardening-nextjs-redesign).
package regression

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/izzamoe/auto-deploy/internal/admin"
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

type env struct {
	h     *server.Hertz
	apps  *store.AppStore
	users *store.AdminUserStore
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := openDB(t)
	q, _ := store.NewDeployQueue(db, 10)
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	apps, _ := store.NewAppStore(db)
	users, _ := store.NewAdminUserStore(db)
	if _, err := users.EnsureSeed("admin", "11"); err != nil {
		t.Fatal(err)
	}
	cfg := &config.ServiceConfig{} // CookieSecure defaults to false (COOKIE_SECURE unset)
	jwt, _ := admin.NewJWTHandler()
	login, err := admin.NewLoginHandler(cfg, jwt, users)
	if err != nil {
		t.Fatal(err)
	}
	h := server.New()
	auth := admin.HertzSessionAuthMiddleware(jwt, users)
	admin.RegisterLoginRoutesHertz(h, login)
	admin.RegisterAccountRoutesHertz(h, admin.NewAccountHandler(users, jwt, cfg), auth)
	tracker := progress.NewProgressTracker()
	admin.RegisterAdminAPIRoutesHertz(h, admin.NewAdminAPIHandler(apps, q, tracker, cancel.NewCancelService(q)), auth)
	return &env{h: h, apps: apps, users: users}
}

func cookieFrom(setCookie string) string {
	// "admin_token=xxx; ..." -> "admin_token=xxx"
	return strings.SplitN(setCookie, ";", 2)[0]
}

func (e *env) login(t *testing.T, user, pass string) (int, string, string) {
	t.Helper()
	w := ut.PerformRequest(e.h.Engine, "POST", "/admin/login",
		&ut.Body{Body: strings.NewReader("username=" + user + "&password=" + pass), Len: -1},
		ut.Header{Key: "Content-Type", Value: "application/x-www-form-urlencoded"})
	r := w.Result()
	sc := string(r.Header.Peek("Set-Cookie"))
	return r.StatusCode(), cookieFrom(sc), sc
}

// do returns status, body, WWW-Authenticate header and the cookie from Set-Cookie.
func (e *env) do(method, path, cookie, ctype, body string) (int, string, string, string) {
	hdrs := []ut.Header{}
	if cookie != "" {
		hdrs = append(hdrs, ut.Header{Key: "Cookie", Value: cookie})
	}
	if ctype != "" {
		hdrs = append(hdrs, ut.Header{Key: "Content-Type", Value: ctype})
	}
	var b *ut.Body
	if body != "" {
		b = &ut.Body{Body: strings.NewReader(body), Len: len(body)}
	}
	w := ut.PerformRequest(e.h.Engine, method, path, b, hdrs...)
	r := w.Result()
	return r.StatusCode(), string(r.Body()), string(r.Header.Peek("WWW-Authenticate")), cookieFrom(string(r.Header.Peek("Set-Cookie")))
}

// ungatedSession walks the seeded default account (admin/11) through rename and
// password change (the two calls allowed under the must-change gate) and returns
// the session cookies issued at each step: login, after rename, after password change.
func (e *env) ungatedSession(t *testing.T) (cookie1, cookie2, cookie3 string) {
	t.Helper()
	st, cookie1, raw := e.login(t, "admin", "11")
	if st >= 400 {
		t.Fatalf("seed login failed: %d Set-Cookie=%q", st, raw)
	}
	st, body, _, cookie2 := e.do("POST", "/admin/api/account/username", cookie1, "application/json", `{"currentPassword":"11","newUsername":"renamed"}`)
	if st != 200 {
		t.Fatalf("rename failed: %d %s", st, body)
	}
	st, body, _, cookie3 = e.do("POST", "/admin/api/account/password", cookie2, "application/json", `{"currentPassword":"11","newPassword":"newpass123"}`)
	if st != 200 {
		t.Fatalf("password change failed: %d %s", st, body)
	}
	return cookie1, cookie2, cookie3
}
