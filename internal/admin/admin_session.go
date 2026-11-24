package admin

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// SessionHandlerConfig wires the session endpoints (skeleton; implemented in T031).
type SessionHandlerConfig struct {
	Sessions *auth.Manager
	Users    *store.AdminUserStore
	Limiter  *auth.LoginLimiter
	ClientIP *auth.ClientIPResolver
}

// SessionHandler serves POST/GET/DELETE /admin/api/v1/session.
type SessionHandler struct{ cfg SessionHandlerConfig }

// NewSessionHandler builds the handler.
func NewSessionHandler(cfg SessionHandlerConfig) *SessionHandler { return &SessionHandler{cfg: cfg} }

func (h *SessionHandler) notImplemented(ctx context.Context, c *app.RequestContext) {
	c.AbortWithStatus(501)
}

// RegisterSessionRoutesHertz mounts the session endpoints. Login is public; the
// other methods sit behind the auth middleware.
func RegisterSessionRoutesHertz(h *server.Hertz, handler *SessionHandler, authMW app.HandlerFunc) {
	h.POST("/admin/api/v1/session", handler.notImplemented)
	api := h.Group("/admin/api/v1", authMW)
	api.GET("/session", handler.notImplemented)
	api.DELETE("/session", handler.notImplemented)
}
