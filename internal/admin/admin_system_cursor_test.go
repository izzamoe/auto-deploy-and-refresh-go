package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/izzamoe/auto-deploy/internal/deploy"
)

// T046 (FR-009, contracts/admin-api.v1-revisi.md): GET /system/logs keeps journalctl's
// default text format and adds `cursor`, the journal cursor of its last entry, read with
// --show-cursor; the cursor line is not part of the log. Without a cursor (journalctl
// printed none) the field is absent, so the response is the 001 one.
func TestSystemLogs_Cursor(t *testing.T) {
	cases := []struct {
		name       string
		out        string
		wantLog    string
		wantCursor string
	}{
		{
			name:       "cursor",
			out:        "Nov 26 10:00:00 host auto-deploy[1]: boot\nNov 26 10:00:01 host auto-deploy[1]: ready\n-- cursor: s=abc;i=2a;b=def;m=10;t=5f;x=99\n",
			wantLog:    "Nov 26 10:00:00 host auto-deploy[1]: boot\nNov 26 10:00:01 host auto-deploy[1]: ready\n",
			wantCursor: "s=abc;i=2a;b=def;m=10;t=5f;x=99",
		},
		{name: "only_cursor", out: "-- cursor: s=only\n", wantLog: "", wantCursor: "s=only"},
		{name: "no_cursor", out: "boot\nready\n", wantLog: "boot\nready\n"},
		{name: "no_entries", out: "-- No entries --\n", wantLog: "-- No entries --\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotArgs string
			restore := deploy.SetRunJournalctlForTest(func(args ...string) ([]byte, error) {
				gotArgs = strings.Join(args, " ")
				return []byte(tc.out), nil
			})
			defer restore()

			engine := newTestSystemEngine(t, NewSystemLogsHandler("auto-deploy.service"))
			w := ut.PerformRequest(engine, "GET", "/admin/api/v1/system/logs?lines=50", nil)
			resp := w.Result()
			if resp.StatusCode() != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode())
			}
			// The snapshot keeps the default output format: no "-o json", plus --show-cursor.
			if !strings.Contains(gotArgs, "-u auto-deploy.service") || !strings.Contains(gotArgs, "-n 50") ||
				!strings.Contains(gotArgs, "--show-cursor") || strings.Contains(" "+gotArgs+" ", " -o ") || strings.Contains(gotArgs, "json") {
				t.Fatalf("journalctl args = %q, want -u auto-deploy.service -n 50 --show-cursor and the default format", gotArgs)
			}
			var body map[string]any
			if err := json.Unmarshal(resp.Body(), &body); err != nil {
				t.Fatal(err)
			}
			if body["service"] != "auto-deploy.service" || body["log"] != tc.wantLog || body["truncated"] != false {
				t.Fatalf("body = %s", resp.Body())
			}
			cursor, has := body["cursor"]
			if tc.wantCursor == "" {
				if has {
					t.Fatalf("cursor %v present although journalctl printed none: %s", cursor, resp.Body())
				}
				return
			}
			if cursor != tc.wantCursor {
				t.Fatalf("cursor = %v, want %q", cursor, tc.wantCursor)
			}
		})
	}
}
