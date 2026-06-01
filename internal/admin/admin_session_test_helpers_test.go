package admin

import (
	"testing"

	"github.com/izzamoe/auto-deploy/internal/config"

	"github.com/cloudwego/hertz/pkg/app"
)

func testServiceConfig() *config.ServiceConfig {
	return &config.ServiceConfig{AdminUsername: "admin", AdminPassword: "secret"}
}

func testHertzSessionAuth(t *testing.T) app.HandlerFunc {
	t.Helper()
	if err := InitJWTSecret(); err != nil {
		t.Fatalf("initJWTSecret: %v", err)
	}
	return HertzSessionAuthMiddleware(testServiceConfig())
}

func testAdminSessionCookie(t *testing.T) string {
	t.Helper()
	if len(jwtSecret) == 0 {
		if err := InitJWTSecret(); err != nil {
			t.Fatalf("initJWTSecret: %v", err)
		}
	}
	token, err := issueJWT("admin")
	if err != nil {
		t.Fatalf("issueJWT: %v", err)
	}
	return jwtCookieName + "=" + token
}

func setHertzSessionCookie(t *testing.T, c *app.RequestContext) {
	t.Helper()
	c.Request.Header.Set("Cookie", testAdminSessionCookie(t))
}
