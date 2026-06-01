package admin

import (
	"sync"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/config"

	"github.com/cloudwego/hertz/pkg/app"
)

var (
	sharedTestJWT     *JWTHandler
	sharedTestJWTOnce sync.Once
)

func testServiceConfig() *config.ServiceConfig {
	return &config.ServiceConfig{AdminUsername: "admin", AdminPassword: "secret"}
}

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
	return HertzSessionAuthMiddleware(testServiceConfig(), sharedJWTHandler(t))
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
