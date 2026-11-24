package admin

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/store"
)

const (
	minAdminPasswordLen = 6
	maxAdminPasswordLen = 1024
	maxUsernameLen      = 64

	// Wrong-current-password attempts per user are limited like login failures, so a
	// stolen session cannot be used to brute-force the password (Q-4).
	passwordAttemptMax    = 5
	passwordAttemptWindow = 15 * time.Minute
)

// AccountHandler serves the signed-in admin's own account endpoints: reading the
// account, changing username/password and revoking other sessions. A credential
// change bumps token_version (every other session dies) and reissues the current
// session so the caller stays signed in (FR-005).
type AccountHandler struct {
	users    *store.AdminUserStore
	sessions *auth.Manager
	// initialPasswordPath is the file written by EnsureInitialAdmin; it is deleted
	// once the password has been changed (Q-3). Empty disables the cleanup.
	initialPasswordPath string
	failures            *auth.Window
}

// NewAccountHandler builds the handler.
func NewAccountHandler(users *store.AdminUserStore, sessions *auth.Manager, initialPasswordPath string) *AccountHandler {
	return &AccountHandler{
		users:               users,
		sessions:            sessions,
		initialPasswordPath: initialPasswordPath,
		failures:            auth.NewWindow(passwordAttemptMax, passwordAttemptWindow, nil),
	}
}

// accountJSON is the contract schema Account.
type accountJSON struct {
	Username           string `json:"username"`
	MustChangePassword bool   `json:"mustChangePassword"`
}

// GetAccountHertz handles GET /account.
func (h *AccountHandler) GetAccountHertz(ctx context.Context, c *app.RequestContext) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}
	c.JSON(200, accountJSON{Username: user.Username, MustChangePassword: user.MustChangePassword})
}

// ChangePasswordHertz handles POST /account/password.
func (h *AccountHandler) ChangePasswordHertz(ctx context.Context, c *app.RequestContext) {
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !bindJSON(c, &body) {
		return
	}
	if len(body.NewPassword) < minAdminPasswordLen || len(body.NewPassword) > maxAdminPasswordLen {
		httpx.WriteError(c, 400, httpx.CodeValidationFailed, "Validation failed", httpx.FieldError{
			Field: "newPassword", Code: httpx.CodeValidationFailed, Message: "New password must be 6-1024 characters",
		})
		return
	}
	p, user, ok := h.verifyCurrent(c, body.CurrentPassword)
	if !ok {
		return
	}
	if err := h.users.UpdatePassword(user.ID, body.NewPassword); err != nil {
		slog.Error("update password failed", "err", err, "user_id", user.ID, "request_id", httpx.FromContext(c))
		httpx.WriteError(c, 500, httpx.CodeInternalError, "Failed to update password")
		return
	}
	slog.Info("password changed", "user_id", user.ID, "request_id", httpx.FromContext(c))
	h.removeInitialPasswordFile()
	h.reissue(c, p.SessionID, user.ID)
	c.JSON(200, map[string]string{"status": "ok", "message": "Password updated"})
}

// ChangeUsernameHertz handles POST /account/username. The rename bumps token_version
// and reissues the current session; the must-change-password flag is NOT cleared, so
// a pre-rename token cannot slip past the gate (B3).
func (h *AccountHandler) ChangeUsernameHertz(ctx context.Context, c *app.RequestContext) {
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewUsername     string `json:"newUsername"`
	}
	if !bindJSON(c, &body) {
		return
	}
	newUsername := strings.TrimSpace(body.NewUsername)
	if newUsername == "" || utf8.RuneCountInString(newUsername) > maxUsernameLen || hasControlRune(newUsername) {
		httpx.WriteError(c, 400, httpx.CodeValidationFailed, "Validation failed", httpx.FieldError{
			Field: "newUsername", Code: httpx.CodeValidationFailed, Message: "Username must be 1-64 printable characters",
		})
		return
	}
	p, user, ok := h.verifyCurrent(c, body.CurrentPassword)
	if !ok {
		return
	}
	if err := h.users.UpdateUsername(user.ID, newUsername); err != nil {
		if errors.Is(err, store.ErrDuplicateApp) {
			httpx.WriteError(c, 409, httpx.CodeDuplicate, "Username is already taken")
			return
		}
		slog.Error("update username failed", "err", err, "user_id", user.ID, "request_id", httpx.FromContext(c))
		httpx.WriteError(c, 500, httpx.CodeInternalError, "Failed to update username")
		return
	}
	slog.Info("username changed", "user_id", user.ID, "request_id", httpx.FromContext(c))
	h.reissue(c, p.SessionID, user.ID)
	c.JSON(200, map[string]string{"status": "ok", "message": "Username updated"})
}

// RevokeAllSessionsHertz handles POST /account/sessions/revoke-all: every other
// session is rejected from now on, the current one is reissued.
func (h *AccountHandler) RevokeAllSessionsHertz(ctx context.Context, c *app.RequestContext) {
	p, ok := auth.PrincipalFrom(c)
	if !ok || p.Kind != auth.KindSession {
		httpx.WriteError(c, 401, httpx.CodeSessionExpired, "Session expired or not signed in")
		return
	}
	if err := h.users.BumpTokenVersion(p.UserID); err != nil {
		slog.Error("revoke all sessions failed", "err", err, "user_id", p.UserID, "request_id", httpx.FromContext(c))
		httpx.WriteError(c, 500, httpx.CodeInternalError, "Failed to revoke sessions")
		return
	}
	slog.Info("all other sessions revoked", "user_id", p.UserID, "request_id", httpx.FromContext(c))
	h.reissue(c, p.SessionID, p.UserID)
	c.JSON(200, map[string]string{"status": "ok", "message": "Other sessions revoked"})
}

// currentUser loads the signed-in user's row (fail closed on any problem).
func (h *AccountHandler) currentUser(c *app.RequestContext) (*store.AdminUser, bool) {
	p, ok := auth.PrincipalFrom(c)
	if !ok || p.Kind != auth.KindSession {
		httpx.WriteError(c, 401, httpx.CodeSessionExpired, "Session expired or not signed in")
		return nil, false
	}
	user, err := h.users.GetByID(p.UserID)
	if err != nil {
		if errors.Is(err, store.ErrAdminUserNotFound) {
			httpx.WriteError(c, 401, httpx.CodeSessionRevoked, "Session was revoked; sign in again")
		} else {
			slog.Error("load account failed", "err", err, "request_id", httpx.FromContext(c))
			httpx.WriteError(c, 500, httpx.CodeInternalError, "Internal error")
		}
		return nil, false
	}
	return user, true
}

// verifyCurrent checks the caller's current password, rate limited per user.
func (h *AccountHandler) verifyCurrent(c *app.RequestContext, currentPassword string) (auth.Principal, *store.AdminUser, bool) {
	p, ok := auth.PrincipalFrom(c)
	if !ok || p.Kind != auth.KindSession {
		httpx.WriteError(c, 401, httpx.CodeSessionExpired, "Session expired or not signed in")
		return p, nil, false
	}
	if blocked, retry := h.failures.Blocked(p.UserID); blocked {
		auth.WriteRateLimited(c, retry)
		return p, nil, false
	}
	user, ok, err := h.users.VerifyPassword(p.Username, currentPassword)
	if err != nil {
		slog.Error("verify current password failed", "err", err, "request_id", httpx.FromContext(c))
		httpx.WriteError(c, 500, httpx.CodeInternalError, "Internal error")
		return p, nil, false
	}
	if !ok || user.ID != p.UserID {
		h.failures.Add(p.UserID)
		httpx.WriteError(c, 401, httpx.CodeUnauthorized, "Current password is incorrect")
		return p, nil, false
	}
	h.failures.Reset(p.UserID)
	return p, user, true
}

// reissue gives the current session a cookie carrying the new token_version. If that
// is impossible the old cookie is dead anyway, so it is cleared.
func (h *AccountHandler) reissue(c *app.RequestContext, sessionID, userID string) {
	user, err := h.users.GetByID(userID)
	if err == nil {
		var iss *auth.Issued
		if iss, err = h.sessions.Reissue(sessionID, user); err == nil {
			h.sessions.SetCookie(c, iss)
			return
		}
	}
	slog.Error("reissue session after credential change failed", "err", err, "request_id", httpx.FromContext(c))
	h.sessions.ClearCookie(c)
}

func (h *AccountHandler) removeInitialPasswordFile() {
	if h.initialPasswordPath == "" {
		return
	}
	if err := os.Remove(h.initialPasswordPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("could not delete the initial admin password file", "path", h.initialPasswordPath, "err", err)
	}
}

// RegisterAccountRoutesHertz mounts the account endpoints behind the auth middleware.
func RegisterAccountRoutesHertz(h *server.Hertz, handler *AccountHandler, authMW app.HandlerFunc) {
	api := h.Group("/admin/api/v1", authMW)
	api.GET("/account", handler.GetAccountHertz)
	api.POST("/account/password", handler.ChangePasswordHertz)
	api.POST("/account/username", handler.ChangeUsernameHertz)
	api.POST("/account/sessions/revoke-all", handler.RevokeAllSessionsHertz)
}

func hasControlRune(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
