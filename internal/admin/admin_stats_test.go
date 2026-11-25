package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// T051 (FR-026, Q-8): GET /admin/api/v1/stats.

var statsHandlerNow = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

type statsBody struct {
	GeneratedAt string `json:"generatedAt"`
	Range       string `json:"range"`
	TZ          string `json:"tz"`
	Apps        struct{ Total, Enabled int }
	Deploys24h  struct {
		Total, Succeeded, Failed, Canceled int
		Hourly                             []int
	} `json:"deploys24h"`
	Active      struct{ Running, Queued int }
	SuccessRate struct {
		Succeeded, Total int
		Rate             *float64 `json:"rate"`
		PreviousRate     *float64 `json:"previousRate"`
	} `json:"successRate"`
	Daily []struct {
		Date                        string
		Succeeded, Failed, Canceled int
	}
	Rollbacks struct{ Count int }
	Recent    []struct {
		ID         string `json:"id"`
		AppID      string `json:"appId"`
		AppName    string `json:"appName"`
		Status     string `json:"status"`
		RolledBack bool   `json:"rolledBack"`
	}
}

type statsEnv struct {
	h     *server.Hertz
	apps  *store.AppStore
	queue *store.DeployQueue
	jwt   *testAuth
}

// newStatsEnv is newTestAdminAPIHertz on a MIGRATED database (stats reads
// deploy_jobs.rolled_back, like production) with the handler's clock frozen.
func newStatsEnv(t *testing.T) *statsEnv {
	t.Helper()
	jwt := testJWTHandler(t)
	db := newTestDB(t)
	appStore, err := store.NewAppStore(db)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := store.NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewAdminUserStore(db); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	handler := NewAdminAPIHandler(appStore, queue, progress.NewProgressTracker(), cancel.NewCancelService(queue))
	handler.now = func() time.Time { return statsHandlerNow }
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	RegisterAdminAPIRoutesHertz(h, handler, jwt.middleware())
	return &statsEnv{h: h, apps: appStore, queue: queue, jwt: jwt}
}

func (e *statsEnv) get(t *testing.T, query string) (int, statsBody, string) {
	t.Helper()
	rr := serveAdminAPIHertz(t, e.h, adminAPIRequestHertz(e.jwt, "GET", "/admin/api/v1/stats"+query, ""))
	var b statsBody
	_ = json.Unmarshal(rr.Response.Body(), &b)
	return rr.Response.StatusCode(), b, string(rr.Response.Body())
}

func TestStats_DefaultsAndShape(t *testing.T) {
	t.Parallel()
	e := newStatsEnv(t)
	code, b, raw := e.get(t, "")
	if code != http.StatusOK {
		t.Fatalf("GET /stats = %d %s", code, raw)
	}
	if b.Range != "7d" || b.TZ != "UTC" || len(b.Daily) != 7 || len(b.Deploys24h.Hourly) != 24 {
		t.Errorf("defaults wrong: range=%q tz=%q daily=%d hourly=%d", b.Range, b.TZ, len(b.Daily), len(b.Deploys24h.Hourly))
	}
	if !strings.Contains(raw, `"rate":null`) || !strings.Contains(raw, `"previousRate":null`) {
		t.Errorf("rate and previousRate must be JSON null when nothing finished: %s", raw)
	}
	if !strings.Contains(raw, `"recent":[]`) {
		t.Errorf("recent must be an empty array, not null: %s", raw)
	}
	if _, err := time.Parse(time.RFC3339, b.GeneratedAt); err != nil {
		t.Errorf("generatedAt %q is not RFC 3339", b.GeneratedAt)
	}

	code, b30, raw := e.get(t, "?range=30d&tz=Asia/Jakarta")
	if code != http.StatusOK || b30.Range != "30d" || len(b30.Daily) != 30 || b30.TZ != "Asia/Jakarta" {
		t.Errorf("range=30d&tz=Asia/Jakarta = %d range=%q daily=%d tz=%q: %s", code, b30.Range, len(b30.Daily), b30.TZ, raw)
	}
}

func TestStats_TimeZoneMovesTheDailyBucket(t *testing.T) {
	t.Parallel()
	e := newStatsEnv(t)
	a, err := e.apps.Create("s", "sec-s", "/opt/s/s", "s.service", "o/s", "s")
	if err != nil {
		t.Fatal(err)
	}
	finished := time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05") // 2026-11-25 03:00 in Jakarta
	if _, err := e.queue.DB().Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, created_at, updated_at, rolled_back) VALUES ('j1', 1, ?, 'v1', 'succeeded', ?, ?, 1)`,
		a.ID, finished, finished,
	); err != nil {
		t.Fatal(err)
	}

	dayCount := func(b statsBody, date string) int {
		for _, d := range b.Daily {
			if d.Date == date {
				return d.Succeeded
			}
		}
		return -1
	}
	_, utcBody, raw := e.get(t, "?tz=UTC")
	_, jktBody, rawJkt := e.get(t, "?tz=Asia/Jakarta")
	if dayCount(utcBody, "2026-11-24") != 1 || dayCount(utcBody, "2026-11-25") != 0 {
		t.Errorf("UTC buckets wrong: %s", raw)
	}
	if dayCount(jktBody, "2026-11-24") != 0 || dayCount(jktBody, "2026-11-25") != 1 {
		t.Errorf("Asia/Jakarta must place the job on 2026-11-25: %s", rawJkt)
	}
	if utcBody.Rollbacks.Count != 1 || len(utcBody.Recent) != 1 || !utcBody.Recent[0].RolledBack || utcBody.Recent[0].AppName != "s" {
		t.Errorf("rollbacks/recent wrong: rollbacks=%d recent=%+v", utcBody.Rollbacks.Count, utcBody.Recent)
	}
	if utcBody.SuccessRate.Rate == nil || *utcBody.SuccessRate.Rate != 1 || utcBody.SuccessRate.Total != 1 {
		t.Errorf("success rate = %+v", utcBody.SuccessRate)
	}
}

func TestStats_RejectsBadParameters(t *testing.T) {
	t.Parallel()
	e := newStatsEnv(t)
	cases := []struct{ query, field string }{
		{"?range=14d", "range"},
		{"?range=7", "range"},
		{"?tz=Mars/Olympus", "tz"},
		{"?tz=Local", "tz"},
		{"?tz=../../etc/passwd", "tz"},
		{"?tz=" + strings.Repeat("A", 200), "tz"},
		{"?tz=UTC%0AX", "tz"},
		{"?range=", ""}, // empty = default
		{"?tz=", ""},
	}
	for _, tc := range cases {
		code, _, body := e.get(t, tc.query)
		if tc.field == "" {
			if code != http.StatusOK {
				t.Errorf("%s = %d %s, want 200", tc.query, code, body)
			}
			continue
		}
		if code != http.StatusBadRequest || !strings.Contains(body, fmt.Sprintf(`"field":"%s"`, tc.field)) || !strings.Contains(body, "validation_failed") {
			t.Errorf("%s = %d %s, want 400 validation_failed on field %q", tc.query, code, body, tc.field)
		}
	}
}

func TestStats_RequiresSession(t *testing.T) {
	t.Parallel()
	e := newStatsEnv(t)
	c := app.NewContext(0)
	c.Request.SetRequestURI("/admin/api/v1/stats")
	c.Request.SetMethod(http.MethodGet)
	e.h.ServeHTTP(context.Background(), c)
	if c.Response.StatusCode() != http.StatusUnauthorized {
		t.Errorf("GET /stats without a session = %d, want 401", c.Response.StatusCode())
	}
}
