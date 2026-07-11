package admin

import (
	"sync"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
)

// fakeAuthenticator is an in-memory adminUserAuthenticator for tests.
type fakeAuthenticator struct {
	username   string
	password   string
	mustChange bool
}

func (f fakeAuthenticator) VerifyPassword(username, password string) (*store.AdminUser, bool, error) {
	if username == f.username && password == f.password {
		return &store.AdminUser{Username: username, MustChangePassword: f.mustChange}, true, nil
	}
	return nil, false, nil
}

func (f fakeAuthenticator) GetByUsername(username string) (*store.AdminUser, error) {
	if username == f.username {
		return &store.AdminUser{Username: username, MustChangePassword: f.mustChange}, nil
	}
	return nil, store.ErrAdminUserNotFound
}

func testAuthenticator() fakeAuthenticator {
	return fakeAuthenticator{username: "admin", password: "secret"}
}

var (
	sharedTestJWT     *JWTHandler
	sharedTestJWTOnce sync.Once
)

func testJWTHandler(t *testing.T) *JWTHandler {
	t.Helper()
	h, err := NewJWTHandler()
	if err != nil {
		t.Fatalf("NewJWTHandler: %v", err)
	}
	return h
}

func sharedJWTHandler(t *testing.T) *JWTHandler {
	t.Helper()
	sharedTestJWTOnce.Do(func() {
		h, err := NewJWTHandler()
		if err != nil {
			panic("NewJWTHandler: " + err.Error())
		}
		sharedTestJWT = h
	})
	return sharedTestJWT
}

func testHertzSessionAuth(t *testing.T) app.HandlerFunc {
	t.Helper()
	return HertzSessionAuthMiddleware(sharedJWTHandler(t), testAuthenticator())
}

func testAdminSessionCookie(t *testing.T) string {
	t.Helper()
	token, err := sharedJWTHandler(t).issueJWT("admin")
	if err != nil {
		t.Fatalf("issueJWT: %v", err)
	}
	return jwtCookieName + "=" + token
}

func setHertzSessionCookie(t *testing.T, c *app.RequestContext) {
	t.Helper()
	c.Request.Header.Set("Cookie", testAdminSessionCookie(t))
}
