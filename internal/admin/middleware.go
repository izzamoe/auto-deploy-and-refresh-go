package admin

import (
	"context"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/middlewares/server/recovery"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/izzamoe/auto-deploy/internal/httpx"
)

func SetupMiddleware(h *server.Hertz) {
	h.Use(recovery.Recovery(recovery.WithRecoveryHandler(func(ctx context.Context, c *app.RequestContext, err any, stack []byte) {
		slog.Error("panic recovered", "error", err, "stack", string(stack), "request_id", httpx.FromContext(c))
		c.JSON(500, map[string]string{"status": "error", "error": "internal server error"})
	})))

	h.Use(httpx.RequestID())
}
