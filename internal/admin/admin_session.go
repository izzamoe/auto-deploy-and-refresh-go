package admin

import (
	"context"
	"log/slog"
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
	maxLoginUsernameLen  = 64
	maxLoginPasswordLen  = 1024
	maxLoggedUsernameLen = 64
)

// SessionHandlerConfig wires the session endpoints.
type SessionHandlerConfig struct {
	Sessions *auth.Manager
	Users    *store.AdminUserStore
	Limiter  *auth.LoginLimiter
	ClientIP *auth.ClientIPResolver
}

// SessionHandler serves POST/GET/DELETE /admin/api/v1/session (FR-007, FR-015,
// FR-016), replacing the server-rendered login form.
type SessionHandler struct{ cfg SessionHandlerConfig }

// NewSessionHandler builds the handler.
func NewSessionHandler(cfg SessionHandlerConfig) *SessionHandler {
	if cfg.ClientIP == nil {
		cfg.ClientIP = auth.NewClientIPResolver(nil)
	}
	if cfg.Limiter == nil {
		cfg.Limiter = auth.NewLoginLimiter(nil)
	}
	return &SessionHandler{cfg: cfg}
}

// sessionJSON is the contract schema Session.
type sessionJSON struct {
	Username           string    `json:"username"`
	MustChangePassword bool      `json:"mustChangePassword"`
	ExpiresAt          time.Time `json:"expiresAt"`
	AbsoluteExpiresAt  time.Time `json:"absoluteExpiresAt"`
}

// CreateHertz handles POST /session (login). It is public and ignores any
// Authorization header, so credentials cached by an old browser dialog cannot
// interfere (RV2-14). The login limiter runs before any bcrypt work (Q-4, B5).
func (h *SessionHandler) CreateHertz(ctx context.Context, c *app.RequestContext) {
	trustedProto := len(c.GetHeader("X-Forwarded-Proto")) > 0 && h.cfg.ClientIP.TrustsRemote(c.RemoteAddr())
	switch auth.CheckLoginCSRF(c, trustedProto) {
	case auth.CSRFRejected:
		httpx.WriteError(c, 403, httpx.CodeCSRFRejected, "Cross-site request rejected")
		return
	case auth.CSRFUnsupportedMedia:
		httpx.WriteError(c, 415, httpx.CodeUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !bindJSON(c, &body) {
		return
	}
	username := strings.TrimSpace(body.Username)
	var fields []httpx.FieldError
	if username == "" || utf8.RuneCountInString(username) > maxLoginUsernameLen {
		fields = append(fields, httpx.FieldError{Field: "username", Code: httpx.CodeValidationFailed, Message: "Username must be 1-64 characters"})
	}
	if body.Password == "" || len(body.Password) > maxLoginPasswordLen {
		fields = append(fields, httpx.FieldError{Field: "password", Code: httpx.CodeValidationFailed, Message: "Password must be 1-1024 characters"})
	}
	if len(fields) > 0 {
		httpx.WriteError(c, 400, httpx.CodeValidationFailed, "Validation failed", fields...)
		return
	}

	ip := h.cfg.ClientIP.FromContext(c)
	if ok, retry := h.cfg.Limiter.Check(ip, username); !ok {
		auth.AuditRequest(c, auth.EventRateLimited, "", "username", clipForLog(username))
		auth.WriteRateLimited(c, retry)
		return
	}
	h.cfg.Limiter.RecordAttempt(ip)

	user, ok, err := h.cfg.Users.VerifyPassword(username, body.Password)
	if err != nil {
		slog.Error("login verify failed", "err", err, "request_id", httpx.FromContext(c))
		httpx.WriteError(c, 500, httpx.CodeInternalError, "Internal error")
		return
	}
	if !ok {
		h.cfg.Limiter.RecordFailure(ip, username)
		auth.AuditRequest(c, auth.EventLoginFailed, "", "username", clipForLog(username))
		httpx.WriteError(c, 401, httpx.CodeUnauthorized, "Invalid username or password")
		return
	}

	iss, err := h.cfg.Sessions.Issue(user, ip, string(c.UserAgent()))
	if err != nil {
		slog.Error("issue session failed", "err", err, "request_id", httpx.FromContext(c))
		httpx.WriteError(c, 500, httpx.CodeInternalError, "Internal error")
		return
	}
	h.cfg.Limiter.RecordSuccess(ip, username)
	h.cfg.Sessions.SetCookie(c, iss)
	auth.AuditRequest(c, auth.EventLoginSucceeded, user.ID)
	c.JSON(200, sessionJSON{
		Username:           user.Username,
		MustChangePassword: user.MustChangePassword,
		ExpiresAt:          iss.ExpiresAt.UTC(),
		AbsoluteExpiresAt:  iss.AbsoluteExpiresAt.UTC(),
	})
}

// GetHertz handles GET /session: the state of the current session (FR-016).
func (h *SessionHandler) GetHertz(ctx context.Context, c *app.RequestContext) {
	p, ok := auth.PrincipalFrom(c)
	if !ok || p.Kind != auth.KindSession {
		httpx.WriteError(c, 401, httpx.CodeSessionExpired, "Session expired or not signed in")
		return
	}
	c.JSON(200, sessionJSON{
		Username:           p.Username,
		MustChangePassword: p.MustChangePassword,
		ExpiresAt:          p.ExpiresAt.UTC(),
		AbsoluteExpiresAt:  p.AbsoluteExpiresAt.UTC(),
	})
}

// DeleteHertz handles DELETE /session: logout revokes the session on the server, so
// a copied cookie is dead too (FR-007, B4).
func (h *SessionHandler) DeleteHertz(ctx context.Context, c *app.RequestContext) {
	p, ok := auth.PrincipalFrom(c)
	if !ok || p.Kind != auth.KindSession {
		httpx.WriteError(c, 401, httpx.CodeSessionExpired, "Session expired or not signed in")
		return
	}
	if err := h.cfg.Sessions.Revoke(p.SessionID); err != nil {
		slog.Error("logout: revoke failed", "err", err, "request_id", httpx.FromContext(c))
		httpx.WriteError(c, 500, httpx.CodeInternalError, "Internal error")
		return
	}
	h.cfg.Sessions.ClearCookie(c)
	auth.AuditRequest(c, auth.EventLogout, p.UserID)
	c.Status(204)
}

// RegisterSessionRoutesHertz mounts the session endpoints. Login is public
// (OpenAPI security: []); the other methods sit behind the auth middleware.
func RegisterSessionRoutesHertz(h *server.Hertz, handler *SessionHandler, auth app.HandlerFunc) {
	h.POST("/admin/api/v1/session", handler.CreateHertz)
	api := h.Group("/admin/api/v1", auth)
	api.GET("/session", handler.GetHertz)
	api.DELETE("/session", handler.DeleteHertz)
}

func clipForLog(s string) string {
	if len(s) > maxLoggedUsernameLen {
		return s[:maxLoggedUsernameLen]
	}
	return s
}
