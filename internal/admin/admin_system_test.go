package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/deploy"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/route"
)

func newTestSystemEngine(t *testing.T, handler *SystemLogsHandler) *route.Engine {
	t.Helper()
	h := server.New(server.WithHostPorts(":0"), server.WithDisablePrintRoute(true))
	RegisterSystemRoutesHertz(h, handler, func(ctx context.Context, c *app.RequestContext) { c.Next(ctx) })
	return h.Engine
}

func TestSelfLogsHertzReturnsServiceJournal(t *testing.T) {
	var gotArgs string
	restore := deploy.SetRunJournalctlForTest(func(args ...string) ([]byte, error) {
		gotArgs = strings.Join(args, " ")
		return []byte("boot\nready\n"), nil
	})
	defer restore()

	engine := newTestSystemEngine(t, NewSystemLogsHandler("auto-deploy.service"))
	w := ut.PerformRequest(engine, "GET", "/admin/api/v1/system/logs", nil)
	resp := w.Result()

	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode())
	}
	if !strings.Contains(gotArgs, "-u auto-deploy.service") {
		t.Errorf("journalctl args = %q, want -u auto-deploy.service", gotArgs)
	}
	// Default line window (no ?lines=) should apply.
	if !strings.Contains(gotArgs, "-n 100") {
		t.Errorf("journalctl args = %q, want default -n 100", gotArgs)
	}
	var parsed struct {
		Service string `json:"service"`
		Log     string `json:"log"`
	}
	if err := json.Unmarshal(resp.Body(), &parsed); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, resp.Body())
	}
	if parsed.Service != "auto-deploy.service" || parsed.Log != "boot\nready\n" {
		t.Errorf("response = %+v", parsed)
	}
}

func TestSelfLogsHertzLinesParam(t *testing.T) {
	tests := []struct {
		query    string
		wantArgs string // substring expected in journalctl args
		wantNoN  bool   // when true, args must not contain "-n "
	}{
		{query: "?lines=50", wantArgs: "-n 50"},
		{query: "?lines=all", wantNoN: true},
		{query: "?lines=0", wantNoN: true},
		{query: "?lines=999999", wantArgs: "-n 5000"}, // clamped to maxLiveLogLines
		{query: "?lines=abc", wantArgs: "-n 100"},     // invalid → default
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			var gotArgs string
			restore := deploy.SetRunJournalctlForTest(func(args ...string) ([]byte, error) {
				gotArgs = strings.Join(args, " ")
				return []byte("x"), nil
			})
			defer restore()

			engine := newTestSystemEngine(t, NewSystemLogsHandler("svc"))
			w := ut.PerformRequest(engine, "GET", "/admin/api/v1/system/logs"+tc.query, nil)
			if w.Result().StatusCode() != http.StatusOK {
				t.Fatalf("status = %d", w.Result().StatusCode())
			}
			if tc.wantNoN && strings.Contains(gotArgs, "-n ") {
				t.Errorf("args = %q, want no -n limit", gotArgs)
			}
			if tc.wantArgs != "" && !strings.Contains(gotArgs, tc.wantArgs) {
				t.Errorf("args = %q, want substring %q", gotArgs, tc.wantArgs)
			}
		})
	}
}

// B25 / FR-058: a live log response is bounded in bytes (2 MiB) and says so.
func TestSelfLogsHertzLogCapTruncated(t *testing.T) {
	huge := strings.Repeat("0123456789abcdef\n", (3<<20)/17+1)
	restore := deploy.SetRunJournalctlForTest(func(args ...string) ([]byte, error) { return []byte(huge), nil })
	defer restore()

	engine := newTestSystemEngine(t, NewSystemLogsHandler("auto-deploy.service"))
	w := ut.PerformRequest(engine, "GET", "/admin/api/v1/system/logs?lines=all", nil)
	resp := w.Result()
	var parsed struct {
		Log       string `json:"log"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(resp.Body(), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Log) > 2<<20+256 {
		t.Fatalf("B25: the response carries %d bytes of log, want at most 2 MiB (+ marker)", len(parsed.Log))
	}
	if !parsed.Truncated {
		t.Error("a truncated live log must set truncated=true")
	}
	if !strings.HasSuffix(parsed.Log, huge[len(huge)-500:]) {
		t.Error("the newest lines (the tail) must be kept")
	}
}

func TestSelfLogsHertzSmallLogIsNotTruncated(t *testing.T) {
	restore := deploy.SetRunJournalctlForTest(func(args ...string) ([]byte, error) { return []byte("boot\n"), nil })
	defer restore()
	engine := newTestSystemEngine(t, NewSystemLogsHandler("auto-deploy.service"))
	w := ut.PerformRequest(engine, "GET", "/admin/api/v1/system/logs", nil)
	var parsed struct {
		Log       string `json:"log"`
		Truncated bool   `json:"truncated"`
	}
	_ = json.Unmarshal(w.Result().Body(), &parsed)
	if parsed.Truncated || parsed.Log != "boot\n" {
		t.Errorf("response = %+v", parsed)
	}
}
