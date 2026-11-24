package admin

import (
	"bytes"
	"database/sql"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/store"
	_ "modernc.org/sqlite"
)

// testSessionCookieName is the cookie name of the shared test session manager
// (Secure, so __Host- prefixed).
const testSessionCookieName = "__Host-admin_session"

// testAuth is the process-wide v1 auth environment the handler tests run behind: an
// in-memory database holding one admin, a session manager and one long-lived
// session. Many tests run in parallel subtests with their own *testing.T, so the
// environment is shared rather than per test.
type testAuth struct {
	mgr   *auth.Manager
	token string
}

var (
	sharedTestAuth     *testAuth
	sharedTestAuthOnce sync.Once
)

func sharedAuth(t testing.TB) *testAuth {
	t.Helper()
	sharedTestAuthOnce.Do(func() {
		db, err := sql.Open("sqlite", "file:admin_shared_test_auth?mode=memory&cache=shared")
		if err != nil {
			panic("open shared auth db: " + err.Error())
		}
		db.SetMaxOpenConns(1) // also keeps the in-memory database alive
		users, err := store.NewAdminUserStore(db)
		if err != nil {
			panic("shared auth users: " + err.Error())
		}
		sessions, err := store.NewAdminSessionStore(db)
		if err != nil {
			panic("shared auth sessions: " + err.Error())
		}
		user, err := users.CreateUser("admin", "secret", false)
		if err != nil {
			panic("shared auth user: " + err.Error())
		}
		mgr, err := auth.NewManager(auth.SessionConfig{Key: bytes.Repeat([]byte{0x2a}, 32), Secure: true}, users, sessions)
		if err != nil {
			panic("shared auth manager: " + err.Error())
		}
		iss, err := mgr.Issue(user, "127.0.0.1", "go-test")
		if err != nil {
			panic("shared auth session: " + err.Error())
		}
		if mgr.CookieName() != testSessionCookieName {
			panic("unexpected cookie name " + mgr.CookieName())
		}
		sharedTestAuth = &testAuth{mgr: mgr, token: iss.Token}
	})
	return sharedTestAuth
}

// testJWTHandler keeps the old helper name used by many handler tests; it now
// returns the shared v1 auth environment.
func testJWTHandler(t *testing.T) *testAuth { return sharedAuth(t) }

// middleware is the real v1 auth middleware (no API-token source).
func (a *testAuth) middleware() app.HandlerFunc {
	return auth.Middleware(auth.MiddlewareConfig{Sessions: a.mgr})
}

// issueJWT returns the value of the shared test session cookie. The argument is
// ignored (kept so call sites read as before).
func (a *testAuth) issueJWT(string) (string, error) { return a.token, nil }

func testHertzSessionAuth(t *testing.T) app.HandlerFunc { return sharedAuth(t).middleware() }

// testAdminSessionCookie is a "name=value" Cookie header for the shared session.
func testAdminSessionCookie(t *testing.T) string {
	t.Helper()
	return testSessionCookieName + "=" + sharedAuth(t).token
}

// setBrowserHeaders adds what a same-origin SPA fetch sends, so cookie-authenticated
// mutations pass the CSRF check.
func setBrowserHeaders(c *app.RequestContext) {
	c.Request.Header.Set("X-Admin-Request", "true")
	c.Request.Header.Set("Sec-Fetch-Site", "same-origin")
}

// setHertzSessionCookie authenticates c as the shared admin session, with the
// headers of a same-origin browser request.
func setHertzSessionCookie(t *testing.T, c *app.RequestContext) {
	t.Helper()
	c.Request.Header.Set("Cookie", testAdminSessionCookie(t))
	setBrowserHeaders(c)
}
