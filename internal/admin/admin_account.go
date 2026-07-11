package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/izzamoe/auto-deploy/internal/config"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const minAdminPasswordLen = 6

// AccountHandler serves the authenticated admin's own account endpoints:
// reading account state and changing username/password.
type AccountHandler struct {
	users *store.AdminUserStore
	jwt   *JWTHandler
	cfg   *config.ServiceConfig
}

func NewAccountHandler(users *store.AdminUserStore, jwt *JWTHandler, cfg *config.ServiceConfig) *AccountHandler {
	return &AccountHandler{users: users, jwt: jwt, cfg: cfg}
}

func (h *AccountHandler) GetAccountHertz(ctx context.Context, c *app.RequestContext) {
	user, err := h.users.GetByUsername(currentAdminUsername(c))
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusNotFound, "Account not found")
		return
	}
	c.JSON(consts.StatusOK, map[string]any{
		"username":           user.Username,
		"mustChangePassword": user.MustChangePassword,
	})
}

func (h *AccountHandler) ChangePasswordHertz(ctx context.Context, c *app.RequestContext) {
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeAdminAPIJSONHertz(c, &body) {
		return
	}
	if len(body.NewPassword) < minAdminPasswordLen {
		writeAdminAPIValidationErrorHertz(c, []string{fmt.Sprintf("New password must be at least %d characters", minAdminPasswordLen)})
		return
	}

	username := currentAdminUsername(c)
	user, ok := h.verifyCurrent(c, username, body.CurrentPassword)
	if !ok {
		return
	}
	if err := h.users.UpdatePassword(user.ID, body.NewPassword); err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to update password")
		return
	}
	h.reissueCookie(c, username)
	c.JSON(consts.StatusOK, adminAPIStatusResponse{Status: "ok", Message: "Password updated"})
}

func (h *AccountHandler) ChangeUsernameHertz(ctx context.Context, c *app.RequestContext) {
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewUsername     string `json:"newUsername"`
	}
	if !decodeAdminAPIJSONHertz(c, &body) {
		return
	}
	newUsername := strings.TrimSpace(body.NewUsername)
	if newUsername == "" {
		writeAdminAPIValidationErrorHertz(c, []string{"New username is required"})
		return
	}

	username := currentAdminUsername(c)
	user, ok := h.verifyCurrent(c, username, body.CurrentPassword)
	if !ok {
		return
	}
	if err := h.users.UpdateUsername(user.ID, newUsername); err != nil {
		if errors.Is(err, store.ErrDuplicateApp) {
			writeAdminAPIValidationErrorHertz(c, []string{"Username is already taken"})
			return
		}
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "Failed to update username")
		return
	}
	h.reissueCookie(c, newUsername)
	c.JSON(consts.StatusOK, adminAPIStatusResponse{Status: "ok", Message: "Username updated"})
}

// verifyCurrent checks the caller's current password, writing the appropriate
// error and returning ok=false when it does not match.
func (h *AccountHandler) verifyCurrent(c *app.RequestContext, username, currentPassword string) (*store.AdminUser, bool) {
	user, ok, err := h.users.VerifyPassword(username, currentPassword)
	if err != nil {
		writeAdminAPIErrorHertz(c, consts.StatusInternalServerError, "internal error")
		return nil, false
	}
	if !ok {
		writeAdminAPIErrorHertz(c, consts.StatusUnauthorized, "Current password is incorrect")
		return nil, false
	}
	return user, true
}

func (h *AccountHandler) reissueCookie(c *app.RequestContext, username string) {
	token, err := h.jwt.issueJWT(username)
	if err != nil {
		slog.Error("account: reissue jwt failed", "err", err)
		return
	}
	c.SetCookie(jwtCookieName, token, int(jwtTTL.Seconds()), "/", "", protocol.CookieSameSiteLaxMode, h.cfg.CookieSecure, true)
}

func RegisterAccountRoutesHertz(h *server.Hertz, handler *AccountHandler, auth app.HandlerFunc) {
	h.GET("/admin/api/account", auth, handler.GetAccountHertz)
	h.POST("/admin/api/account/password", auth, handler.ChangePasswordHertz)
	h.POST("/admin/api/account/username", auth, handler.ChangeUsernameHertz)
}
