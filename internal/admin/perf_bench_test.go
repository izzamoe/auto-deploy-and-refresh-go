package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
	_ "modernc.org/sqlite"
)

// T104 (NFR-001, NFR-002, Q-26): GET /admin/api/v1/apps and GET /admin/api/v1/stats with 50 apps and
// 10,000 jobs, through the real routes and the real session middleware. The duration of EVERY iteration
// is kept and the 95th percentile is reported as the `p95-ms` metric (ns/op is a mean, not a p95).
//
//	go test -bench . -run '^$' ./internal/admin/ -benchtime=50x
const (
	perfApps = 50
	perfJobs = 10000
)

func newPerfServer(b *testing.B) *server.Hertz {
	b.Helper()
	db, err := sql.Open("sqlite", b.TempDir()+"/perf.db")
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	b.Cleanup(func() { _ = db.Close() })
	appStore, err := store.NewAppStore(db)
	if err != nil {
		b.Fatal(err)
	}
	queue, err := store.NewDeployQueue(db, 10)
	if err != nil {
		b.Fatal(err)
	}
	if err := queue.Migrate(); err != nil {
		b.Fatal(err)
	}
	if _, err := store.NewAdminUserStore(db); err != nil {
		b.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		b.Fatal(err)
	}

	ids := make([]string, 0, perfApps)
	for i := 0; i < perfApps; i++ {
		a, err := appStore.Create(fmt.Sprintf("app%02d", i), fmt.Sprintf("secret-%02d-%s", i, "0123456789abcdef0123456789abcdef"), fmt.Sprintf("/opt/app%02d/app", i), fmt.Sprintf("app%02d.service", i), "o/r", "a")
		if err != nil {
			b.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, created_at, updated_at, rolled_back) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	statuses := []string{"succeeded", "succeeded", "succeeded", "failed", "canceled"}
	const layout = "2006-01-02 15:04:05"
	now := time.Now().UTC()
	for i := 0; i < perfJobs; i++ {
		ts := now.Add(-time.Duration(i) * 259 * time.Second) // spread over ~30 days
		if _, err := stmt.Exec(fmt.Sprintf("pj-%06d", i), i+1, ids[i%perfApps], fmt.Sprintf("v%d", i), statuses[i%5], ts.Format(layout), ts.Format(layout), i%17/16); err != nil {
			b.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	handler := NewAdminAPIHandler(appStore, queue, progress.NewProgressTracker(), cancel.NewCancelService(queue))
	jwt := sharedAuth(b)
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	RegisterAdminAPIRoutesHertz(h, handler, jwt.middleware())
	return h
}

func p95Millis(d []time.Duration) float64 {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := (len(s)*95 + 99) / 100 // nearest rank
	if idx < 1 {
		idx = 1
	}
	return float64(s[idx-1]) / float64(time.Millisecond)
}

func benchAdminGET(b *testing.B, h *server.Hertz, path string) {
	jwt := sharedAuth(b)
	durations := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c := adminAPIRequestHertz(jwt, http.MethodGet, path, "")
		b.StartTimer()
		start := time.Now()
		h.ServeHTTP(context.Background(), c)
		durations = append(durations, time.Since(start))
		b.StopTimer()
		if c.Response.StatusCode() != http.StatusOK || len(c.Response.Body()) < 50 {
			b.Fatalf("GET %s = %d (%d bytes)", path, c.Response.StatusCode(), len(c.Response.Body()))
		}
		b.StartTimer()
	}
	b.StopTimer()
	b.ReportMetric(p95Millis(durations), "p95-ms")
}

func BenchmarkAdminAPI_Apps_50Apps10kJobs(b *testing.B) {
	h := newPerfServer(b)
	benchAdminGET(b, h, "/admin/api/v1/apps")
}

func BenchmarkAdminAPI_Stats_50Apps10kJobs(b *testing.B) {
	h := newPerfServer(b)
	benchAdminGET(b, h, "/admin/api/v1/stats?range=30d")
}
