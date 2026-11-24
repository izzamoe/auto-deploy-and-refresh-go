package auth

import (
	"context"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

// Principal is the authenticated caller attached to the request context.
type Principal struct {
	Kind               string // "session" or "token"
	UserID             string
	Username           string
	MustChangePassword bool
	SessionID          string
	ExpiresAt          time.Time
	AbsoluteExpiresAt  time.Time
}

// MiddlewareConfig wires the middleware.
type MiddlewareConfig struct {
	Sessions     *Manager
	Tokens       TokenSource
	TokenLimiter *Window
	ClientIP     *ClientIPResolver
	Now          func() time.Time
}

// Middleware authenticates /admin/api/v1 and /admin/events/v1/ws requests.
func Middleware(cfg MiddlewareConfig) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.AbortWithStatus(501)
	}
}

// PrincipalFrom returns the principal set by Middleware.
func PrincipalFrom(c *app.RequestContext) (Principal, bool) { return Principal{}, false }
