package admin

import (
	"context"
	"strconv"
	"strings"

	"github.com/izzamoe/auto-deploy/internal/deploy"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	// defaultLiveLogLines is the line window used by the live log viewers when
	// the request does not specify one.
	defaultLiveLogLines = 100
	// maxLiveLogLines caps an explicit "lines" request so a single call cannot
	// ask journalctl for an unbounded tail; "all"/0 still returns the full
	// journal, which journalctl itself bounds by the unit's retained history.
	maxLiveLogLines = 5000
)

// parseLogLinesParam reads the optional "lines" query parameter shared by the
// live log endpoints. An empty value yields defaultLiveLogLines; "all" or "0"
// yields 0 (meaning the full available journal); any other positive integer is
// clamped to maxLiveLogLines. Invalid values fall back to the default.
func parseLogLinesParam(c *app.RequestContext) int {
	raw := strings.TrimSpace(string(c.Query("lines")))
	if raw == "" {
		return defaultLiveLogLines
	}
	if strings.EqualFold(raw, "all") || raw == "0" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return defaultLiveLogLines
	}
	if n > maxLiveLogLines {
		return maxLiveLogLines
	}
	return n
}

// SystemLogsHandler serves auto-deploy's own systemd journal so operators can
// diagnose the service itself (e.g. why a GitHub releases call failed) from the
// admin UI, instead of needing shell access to run journalctl.
type SystemLogsHandler struct {
	// selfServiceName is the systemd unit name of auto-deploy itself.
	selfServiceName string
}

func NewSystemLogsHandler(selfServiceName string) *SystemLogsHandler {
	return &SystemLogsHandler{selfServiceName: selfServiceName}
}

// SelfLogsHertz returns the recent systemd journal for auto-deploy itself. The
// optional "lines" query parameter behaves as documented on parseLogLinesParam.
func (h *SystemLogsHandler) SelfLogsHertz(ctx context.Context, c *app.RequestContext) {
	lines := parseLogLinesParam(c)
	c.JSON(consts.StatusOK, map[string]any{
		"service": h.selfServiceName,
		"log":     deploy.CaptureServiceLogsLines(h.selfServiceName, lines),
	})
}

// RegisterSystemRoutesHertz registers the system (self) log route under auth.
func RegisterSystemRoutesHertz(h *server.Hertz, handler *SystemLogsHandler, auth app.HandlerFunc) {
	api := h.Group("/admin/api", auth)
	api.GET("/system/logs", handler.SelfLogsHertz)
}
