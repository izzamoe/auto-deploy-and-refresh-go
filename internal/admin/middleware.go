package admin

import (
	"context"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/middlewares/server/recovery"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/izzamoe/auto-deploy/internal/httpx"
)

// SetupMiddleware installs recovery, request IDs and the security headers (no HSTS).
func SetupMiddleware(h *server.Hertz) {
	SetupMiddlewareWithOptions(h, SecurityHeadersConfig{})
}

// SetupMiddlewareWithOptions is SetupMiddleware with the optional security headers.
func SetupMiddlewareWithOptions(h *server.Hertz, sec SecurityHeadersConfig) {
	h.Use(recovery.Recovery(recovery.WithRecoveryHandler(func(ctx context.Context, c *app.RequestContext, err any, stack []byte) {
		slog.Error("panic recovered", "error", err, "stack", string(stack), "request_id", httpx.FromContext(c))
		c.JSON(500, map[string]string{"status": "error", "error": "internal server error"})
	})))

	h.Use(httpx.RequestID())
	h.Use(SecurityHeaders(sec))
}
