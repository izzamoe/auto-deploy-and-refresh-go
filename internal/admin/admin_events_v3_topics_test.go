package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/coordinator"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// T038 (FR-015, SC-011): the existing event sources (tracker progress, cancel service,
// coordinator job_status) reach protocol-3 sockets on topic jobs only; a socket subscribed to
// summary alone receives no progress frame and no job event; protocol-2 sockets keep
// receiving everything exactly as before.
func TestV3_TopicFilter(t *testing.T) {
	sch := compileEventsV3Schema(t)
	e := newWSSessionEnv(t, "v3topics", time.Hour)
	e.hub.tracker.SetProgressSink(e.hub)

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "topics.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	apps, err := store.NewAppStore(db)
	mutevMust(t, err)
	q, err := store.NewDeployQueue(db, 10)
	mutevMust(t, err)
	mutevMust(t, q.Migrate())
	_, err = store.NewAdminUserStore(db)
	mutevMust(t, err)
	mutevMust(t, store.Migrate(db))
	cs := cancel.NewCancelService(q)
	cs.SetEventSink(e.hub)

	summaryOnly := v3cDial(t, e, sch)
	summaryOnly.send(t, v3cSub("r1", nil, v3cTopic("summary")))
	summaryOnly.await(t, "subscribed{summary}", v3cAck("subscribed", "r1"))

	jobs := v3cDial(t, e, sch)
	jobs.send(t, v3cSub("r1", nil, map[string]any{"name": "jobs", "progress": true}))
	jobs.await(t, "subscribed{jobs}", v3cAck("subscribed", "r1"))
	jobs.await(t, "snapshot", v3cIs("snapshot"))

	// A protocol-2 socket on the same hub, read in the background.
	v2, resp, err := dialEvents(t, e.addr, "/admin/events/v1/ws?protocol=2", e.header())
	closeBody(resp)
	if err != nil {
		t.Fatal(err)
	}
	defer v2.CloseNow()
	v2frames := make(chan map[string]any, 1024)
	go func() {
		for {
			_, raw, err := v2.Read(context.Background())
			if err != nil {
				close(v2frames)
				return
			}
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			v2frames <- m
		}
	}()

	// 1. Tracker progress: compact v3 frames for jobs only.
	e.hub.tracker.Start("a1", "j1", "v1.0.0")
	for i := 1; i <= 5; i++ {
		e.hub.tracker.Update("a1", int64(i*10), 100, 5)
	}
	p := jobs.await(t, "progress p on jobs", func(m map[string]any) bool { return m["t"] == "p" && m["j"] == "j1" })
	if p["v"] != "3" {
		t.Errorf("progress on protocol 3 = %v, want v:\"3\"", p["_raw"])
	}
	e.hub.tracker.Finish("a1")

	// 2. Cancel service: cancel_requested on topic jobs, without the protocol-2 payload copy.
	mutevMust(t, q.EnqueueManual("a1", "v9"))
	cancelID, _, err := q.DequeueNext("a1")
	if err != nil || cancelID == "" {
		t.Fatalf("DequeueNext: %q %v", cancelID, err)
	}
	if _, err := cs.RequestJobCancel(cancelID); err != nil {
		t.Fatal(err)
	}
	cr := jobs.await(t, "cancel_requested on jobs", v3cIs("cancel_requested"))
	if cr["topic"] != "jobs" || cr["jobId"] != cancelID || cr["seq"] == nil {
		t.Errorf("cancel_requested = %v, want topic jobs, jobId %s and a seq", cr["_raw"], cancelID)
	}
	if _, dup := v3cPayload(cr)["jobId"]; dup {
		t.Errorf("protocol 3 must drop payload.jobId: %v", cr["_raw"])
	}

	// 3. Coordinator: job_status of a job it ran (rolled back), on topic jobs.
	app, err := apps.Create("topics-app", "secret1", "/bin/app", "app.service", "org/repo", "artifact")
	mutevMust(t, err)
	coord := coordinator.NewCoordinator(apps, q, func(a *store.App, jobID, tag string) (store.DownloadSummary, error) {
		e.hub.tracker.Start(a.ID, jobID, tag)
		e.hub.tracker.Update(a.ID, 10, 100, 5)
		e.hub.tracker.Finish(a.ID)
		return store.DownloadSummary{}, fmt.Errorf("restart service: boom; %w", store.ErrRolledBack)
	}, e.hub.tracker)
	coord.SetEventSink(e.hub)
	mutevMust(t, q.Enqueue(app.ID, "v1"))
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	coord.Start(ctx)
	js := jobs.await(t, "coordinator job_status on jobs", func(m map[string]any) bool {
		return m["type"] == "job_status" && m["appId"] == app.ID && v3cPayload(m)["status"] == "failed"
	})
	if js["topic"] != "jobs" || v3cPayload(js)["rolledBack"] != true {
		t.Errorf("coordinator job_status = %v", js["_raw"])
	}
	if _, dup := v3cPayload(js)["jobId"]; dup {
		t.Errorf("protocol 3 must drop payload.jobId: %v", js["_raw"])
	}
	stop()
	coord.Wait()

	// The summary-only socket: a summary event is the barrier; before it, nothing at all.
	e.hub.PublishTopic("summary", "summary", "", "", "", map[string]any{"appsTotal": 1, "appsEnabled": 1, "running": 0, "queued": 0})
	summaryOnly.await(t, "the barrier summary event", v3cIs("summary"))
	for _, m := range summaryOnly.seen {
		if m["t"] == "p" || m["topic"] == "jobs" {
			t.Errorf("SC-011: a socket without topic jobs received %v", m["_raw"])
		}
	}

	// Protocol 2 is unchanged: every event, v "2" progress and payload.jobId kept.
	_ = v2.Close(websocket.StatusNormalClosure, "")
	seen := map[string]int{}
	for m := range v2frames {
		if m["protocol"] == "3" || m["v"] == "3" {
			t.Errorf("a protocol-2 socket received a protocol-3 frame: %v", m)
		}
		switch {
		case m["t"] == "p" && m["v"] == "2":
			seen["p"]++
		case m["type"] == "cancel_requested":
			if pl, _ := m["payload"].(map[string]any); pl["jobId"] != cancelID {
				t.Errorf("protocol 2 cancel_requested lost payload.jobId: %v", m)
			}
			seen["cancel_requested"]++
		case m["type"] == "job_status":
			seen["job_status"]++
		}
	}
	for _, typ := range []string{"p", "cancel_requested", "job_status"} {
		if seen[typ] == 0 {
			t.Errorf("protocol 2 socket received no %s (seen %v)", typ, seen)
		}
	}
}
