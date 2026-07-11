package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/config"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/route"
)

func newTestAccountEngine(t *testing.T) (*route.Engine, *store.AdminUserStore, string) {
	t.Helper()
	db := newTestDB(t)
	users, err := store.NewAdminUserStore(db)
	if err != nil {
		t.Fatalf("NewAdminUserStore: %v", err)
	}
	if _, err := users.EnsureSeed("admin", "11"); err != nil {
		t.Fatalf("EnsureSeed: %v", err)
	}

	jwt := testJWTHandler(t)
	account := NewAccountHandler(users, jwt, &config.ServiceConfig{})
	auth := HertzSessionAuthMiddleware(jwt, users)

	h := server.New(server.WithHostPorts(":0"), server.WithDisablePrintRoute(true))
	RegisterAccountRoutesHertz(h, account, auth)
	// A representative gated data route.
	h.GET("/admin/api/apps", auth, func(ctx context.Context, c *app.RequestContext) {
		c.JSON(200, map[string]string{"status": "ok"})
	})

	token, err := jwt.issueJWT("admin")
	if err != nil {
		t.Fatalf("issueJWT: %v", err)
	}
	return h.Engine, users, jwtCookieName + "=" + token
}

func TestForceChangeGateBlocksDataUntilPasswordChanged(t *testing.T) {
	t.Parallel()
	engine, _, cookie := newTestAccountEngine(t)
	cookieHdr := ut.Header{Key: "Cookie", Value: cookie}

	// While must_change_password, a data endpoint is blocked...
	if w := ut.PerformRequest(engine, "GET", "/admin/api/apps", nil, cookieHdr); w.Code != 403 {
		t.Fatalf("gated data route = %d, want 403", w.Code)
	}
	// ...but the account endpoints stay reachable.
	if w := ut.PerformRequest(engine, "GET", "/admin/api/account", nil, cookieHdr); w.Code != 200 {
		t.Fatalf("account route = %d, want 200", w.Code)
	}

	// Change the password, which clears the flag.
	body := `{"currentPassword":"11","newPassword":"new-strong-pass"}`
	w := ut.PerformRequest(engine, "POST", "/admin/api/account/password", &ut.Body{Body: strings.NewReader(body), Len: len(body)},
		cookieHdr, ut.Header{Key: "Content-Type", Value: "application/json"})
	if w.Code != 200 {
		t.Fatalf("change password = %d, want 200: %s", w.Code, w.Body.String())
	}

	// Gate lifted: the data endpoint now passes.
	if w := ut.PerformRequest(engine, "GET", "/admin/api/apps", nil, cookieHdr); w.Code != 200 {
		t.Fatalf("data route after change = %d, want 200", w.Code)
	}
}

func TestChangePasswordRejectsWrongCurrentAndShortNew(t *testing.T) {
	t.Parallel()
	engine, _, cookie := newTestAccountEngine(t)
	cookieHdr := ut.Header{Key: "Cookie", Value: cookie}
	jsonHdr := ut.Header{Key: "Content-Type", Value: "application/json"}

	wrong := `{"currentPassword":"nope","newPassword":"new-strong-pass"}`
	if w := ut.PerformRequest(engine, "POST", "/admin/api/account/password",
		&ut.Body{Body: strings.NewReader(wrong), Len: len(wrong)}, cookieHdr, jsonHdr); w.Code != 401 {
		t.Fatalf("wrong current password = %d, want 401", w.Code)
	}

	short := `{"currentPassword":"11","newPassword":"x"}`
	if w := ut.PerformRequest(engine, "POST", "/admin/api/account/password",
		&ut.Body{Body: strings.NewReader(short), Len: len(short)}, cookieHdr, jsonHdr); w.Code != 400 {
		t.Fatalf("short new password = %d, want 400", w.Code)
	}
}
