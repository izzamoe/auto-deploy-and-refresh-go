package events_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/izzamoe/auto-deploy/internal/admin"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/events"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
	_ "modernc.org/sqlite"
)

// T042 (FR-002): topic summary publishes {appsTotal, appsEnabled, running, queued} only
// when it changed, its ack state is the current one, and it is computed by the same
// function as the apps/active fields of GET /stats.

type fakeSummarySource struct {
	st  events.SummaryState
	err error
}

func (f *fakeSummarySource) get() (events.SummaryState, error) { return f.st, f.err }

func TestSummary_PublishesOnlyOnChange(t *testing.T) {
	src := &fakeSummarySource{st: events.SummaryState{AppsTotal: 1, AppsEnabled: 1}}
	var got []events.Event
	s := events.NewSummary(src.get, func(e events.Event) { got = append(got, e) })

	if !s.Refresh() {
		t.Fatal("the first Refresh must publish the initial state")
	}
	if s.Refresh() || s.Refresh() {
		t.Fatal("Refresh with an unchanged state published again")
	}
	src.st.Running = 1
	if !s.Refresh() {
		t.Fatal("Refresh after a change did not publish")
	}
	src.err = errors.New("db down")
	if s.Refresh() {
		t.Fatal("a failing source must publish nothing")
	}
	if len(got) != 2 {
		t.Fatalf("published %d events, want 2", len(got))
	}
	for i, want := range []events.SummaryState{{AppsTotal: 1, AppsEnabled: 1}, {AppsTotal: 1, AppsEnabled: 1, Running: 1}} {
		e := got[i]
		if e.Topic != events.TopicSummary || e.Type != events.TypeSummary || e.Payload != want {
			t.Fatalf("event %d = %+v, want summary %+v", i, e, want)
		}
	}
}

func TestSummary_CurrentIsTheAckState(t *testing.T) {
	src := &fakeSummarySource{st: events.SummaryState{AppsTotal: 3, AppsEnabled: 2, Running: 1, Queued: 4}}
	var published int
	s := events.NewSummary(src.get, func(events.Event) { published++ })
	st, err := s.Current()
	if err != nil || st != src.st {
		t.Fatalf("Current = %+v %v, want %+v", st, err, src.st)
	}
	// A state the ack shows is also the last published one: no duplicate afterwards.
	if s.Refresh() {
		t.Fatal("Refresh after Current published the same state again")
	}
	if published != 1 {
		t.Fatalf("published %d, want 1 (Current publishes a state nobody saw yet)", published)
	}
	raw, _ := json.Marshal(st)
	if string(raw) != `{"appsTotal":3,"appsEnabled":2,"running":1,"queued":4}` {
		t.Fatalf("summary JSON = %s", raw)
	}
}

func TestSummary_PublishedOnTheBus(t *testing.T) {
	bus := events.NewBus()
	c := events.NewClient(events.Principal{Kind: events.PrincipalToken})
	bus.Register(c)
	bus.Subscribe(c, []events.TopicSub{{Topic: events.TopicSummary}}, nil)
	src := &fakeSummarySource{st: events.SummaryState{AppsTotal: 1}}
	s := events.NewSummary(src.get, func(e events.Event) { _, _ = bus.Publish(e) })
	s.Refresh()
	s.Refresh()
	select {
	case raw := <-c.Queue():
		var f struct {
			Seq     uint64         `json:"seq"`
			Topic   string         `json:"topic"`
			Type    string         `json:"type"`
			Payload map[string]int `json:"payload"`
		}
		if err := json.Unmarshal(raw, &f); err != nil || f.Seq == 0 || f.Topic != "summary" || f.Type != "summary" || f.Payload["appsTotal"] != 1 {
			t.Fatalf("frame = %s (%v)", raw, err)
		}
	default:
		t.Fatal("an API-token client subscribed to summary got nothing (Q-6=A allows summary)")
	}
	select {
	case raw := <-c.Queue():
		t.Fatalf("unchanged state was published twice: %s", raw)
	default:
	}
}

// TestSummary_MatchesStatsEndpoint builds a database with apps and jobs in every status and
// compares the summary (computed by store.DeployQueue.Summary, the function the hub uses)
// with the apps/active fields of a real GET /stats response.
func TestSummary_MatchesStatsEndpoint(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
	if _, err := store.NewAdminUserStore(db); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, name := range []string{"a", "b", "c"} {
		app, err := apps.Create(name, "secret-"+name, "/opt/"+name+"/bin", name+".service", "org/"+name, name+"-linux")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, app.ID)
		if i == 2 {
			if err := apps.SetEnabled(app.ID, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	// app a: one running, one queued; app b: cancel_requested (running), two queued and a
	// finished one; app c: a failed one.
	mustEnqueue := func(app, tag string) {
		if err := q.Enqueue(app, tag); err != nil {
			t.Fatal(err)
		}
	}
	mustEnqueue(ids[0], "v1.0.0")
	if _, _, err := q.DequeueNext(ids[0]); err != nil {
		t.Fatal(err)
	}
	mustEnqueue(ids[0], "v1.0.1")
	mustEnqueue(ids[1], "v2.0.0")
	done, _, err := q.DequeueNext(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := q.MarkDone(done, true, "", nil); err != nil {
		t.Fatal(err)
	}
	mustEnqueue(ids[1], "v2.0.1")
	running, _, err := q.DequeueNext(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cancel.NewCancelService(q).RequestJobCancel(running); err != nil {
		t.Fatal(err)
	}
	mustEnqueue(ids[1], "v2.0.2")
	mustEnqueue(ids[1], "v2.0.3")
	mustEnqueue(ids[2], "v3.0.0")
	failed, _, err := q.DequeueNext(ids[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := q.MarkDone(failed, false, "boom", nil); err != nil {
		t.Fatal(err)
	}

	var published []events.Event
	s := events.NewSummary(func() (events.SummaryState, error) {
		sum, err := q.Summary()
		return events.SummaryState{AppsTotal: sum.AppsTotal, AppsEnabled: sum.AppsEnabled, Running: sum.Running, Queued: sum.Queued}, err
	}, func(e events.Event) { published = append(published, e) })
	st, err := s.Current()
	if err != nil {
		t.Fatal(err)
	}

	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	handler := admin.NewAdminAPIHandler(apps, q, progress.NewProgressTracker(), cancel.NewCancelService(q))
	h.GET("/stats", handler.GetStatsHertz)
	w := ut.PerformRequest(h.Engine, "GET", "/stats", nil)
	if w.Code != 200 {
		t.Fatalf("GET /stats = %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Apps   struct{ Total, Enabled int }
		Active struct{ Running, Queued int }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	fromStats := events.SummaryState{AppsTotal: body.Apps.Total, AppsEnabled: body.Apps.Enabled, Running: body.Active.Running, Queued: body.Active.Queued}
	if st != fromStats {
		t.Fatalf("summary %+v != GET /stats apps/active %+v", st, fromStats)
	}
	want := events.SummaryState{AppsTotal: 3, AppsEnabled: 2, Running: 2, Queued: 3}
	if st != want {
		t.Fatalf("summary = %+v, want %+v", st, want)
	}
	if len(published) != 1 || published[0].Payload != want {
		t.Fatalf("published %+v, want one summary %+v", published, want)
	}
}
