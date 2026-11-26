package admin

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/izzamoe/auto-deploy/internal/github"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// T047 (FR-010): GET /apps/:id/releases expires the cached release list of the app's repo
// once the app got a new job since its releases were last served (a new release is the
// usual reason for a deploy); otherwise the cache (60 s + If-None-Match, internal/github)
// answers.

type expiringLister struct {
	mu      sync.Mutex
	lists   int
	expired []string
}

func (l *expiringLister) ListReleases(_ context.Context, owner, repo string) ([]github.Release, error) {
	l.mu.Lock()
	l.lists++
	l.mu.Unlock()
	return []github.Release{{TagName: "v1.0.0", Assets: []string{"cache-linux"}}}, nil
}

func (l *expiringLister) ExpireReleases(owner, repo string) {
	l.mu.Lock()
	l.expired = append(l.expired, owner+"/"+repo)
	l.mu.Unlock()
}

func (l *expiringLister) expiries() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.expired...)
}

func newReleasesCacheEnv(t *testing.T) (*ReleasesHandler, *store.AppStore, *store.DeployQueue, *expiringLister, func() int) {
	t.Helper()
	db := newTestDB(t)
	apps, err := store.NewAppStore(db)
	if err != nil {
		t.Fatal(err)
	}
	q, err := store.NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	lister := &expiringLister{}
	h := NewReleasesHandler(apps, lister)
	h.SetDeployQueue(q)
	srv := server.New(server.WithHostPorts("127.0.0.1:0"), server.WithDisablePrintRoute(true))
	RegisterReleasesRoutesHertz(srv, h, func(ctx context.Context, c *app.RequestContext) { c.Next(ctx) })
	app, err := apps.Create("cache", "cache-secret", "/opt/cache/bin", "cache.service", "owner/cache-repo", "cache-linux")
	if err != nil {
		t.Fatal(err)
	}
	get := func() int {
		w := ut.PerformRequest(srv.Engine, "GET", "/admin/api/v1/apps/"+app.ID+"/releases", nil)
		return w.Code
	}
	return h, apps, q, lister, get
}

func TestReleasesCache_ExpiredWhenAJobOfTheAppIsCreated(t *testing.T) {
	_, apps, q, lister, get := newReleasesCacheEnv(t)
	list, _ := apps.List()
	appID := list[0].ID

	// No job yet: nothing to expire.
	if code := get(); code != http.StatusOK {
		t.Fatalf("GET releases = %d", code)
	}
	if got := lister.expiries(); len(got) != 0 {
		t.Fatalf("expired %v without any job", got)
	}

	// A job (webhook or manual deploy) is created: the next request expires the repo once.
	if err := q.Enqueue(appID, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	get()
	get()
	if got := lister.expiries(); len(got) != 1 || got[0] != "owner/cache-repo" {
		t.Fatalf("expiries after one new job = %v, want [owner/cache-repo] once", got)
	}

	// Another job: expired again.
	if err := q.EnqueueManual(appID, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	get()
	if got := lister.expiries(); len(got) != 2 {
		t.Fatalf("expiries after a second job = %v, want 2", got)
	}
	lister.mu.Lock()
	defer lister.mu.Unlock()
	if lister.lists != 4 {
		t.Fatalf("ListReleases called %d times, want 4 (the cache itself lives in the lister)", lister.lists)
	}
}

func TestReleasesCache_ListerWithoutCacheIsUnaffected(t *testing.T) {
	db := newTestDB(t)
	apps, err := store.NewAppStore(db)
	if err != nil {
		t.Fatal(err)
	}
	q, err := store.NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	h := NewReleasesHandler(apps, &contractReleaseLister{})
	h.SetDeployQueue(q)
	app, err := apps.Create("plain", "plain-secret", "/opt/plain/bin", "plain.service", "owner/plain", "plain-linux")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(app.ID, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	h.expireIfNewJob(app.ID, "owner", "plain") // a lister without ExpireReleases: no-op, no panic
}
