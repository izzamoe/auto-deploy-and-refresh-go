package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/coordinator"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
	"github.com/santhosh-tekuri/jsonschema/v6"
	_ "modernc.org/sqlite"
)

// T044 (B11, B52 BE; contracts/events-ws.md): jobId top-level AND in the payload for
// one release, job_status from the coordinator, status in the snapshot, and every
// event valid against internal/admin/testdata/events-ws.schema.json.

type eventsEnv struct {
	q       *store.DeployQueue
	apps    *store.AppStore
	tracker *progress.ProgressTracker
	hub     *AdminEventHub
	cancel  *cancel.CancelService
}

func newEventsEnv(t *testing.T) *eventsEnv {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
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
	if _, err := store.NewAdminUserStore(db); err != nil { // store.Migrate alters admin_users
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	tracker := progress.NewProgressTracker()
	hub := NewAdminEventHub(tracker)
	tracker.SetProgressSink(hub)
	cs := cancel.NewCancelService(q)
	cs.SetEventSink(hub)
	return &eventsEnv{q: q, apps: apps, tracker: tracker, hub: hub, cancel: cs}
}

// readEvents drains client until no frame arrives for quiet.
func readEvents(t *testing.T, c *adminEventClient, quiet time.Duration) []map[string]any {
	t.Helper()
	var out []map[string]any
	for {
		select {
		case raw, ok := <-c.send:
			if !ok {
				return out
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("event is not JSON: %s", raw)
			}
			m["_raw"] = string(raw)
			out = append(out, m)
		case <-time.After(quiet):
			return out
		}
	}
}

func eventsOfType(evs []map[string]any, typ string) []map[string]any {
	var out []map[string]any
	for _, e := range evs {
		if e["type"] == typ {
			out = append(out, e)
		}
	}
	return out
}

func payloadOf(t *testing.T, e map[string]any) map[string]any {
	t.Helper()
	p, ok := e["payload"].(map[string]any)
	if !ok {
		t.Fatalf("event %v has no object payload: %v", e["type"], e["_raw"])
	}
	return p
}

func TestEvents_CancelRequestedHasTopLevelJobID(t *testing.T) {
	env := newEventsEnv(t)
	if err := env.q.EnqueueManual("a1", "v1"); err != nil {
		t.Fatal(err)
	}
	jobID, _, err := env.q.DequeueNext("a1")
	if err != nil || jobID == "" {
		t.Fatalf("DequeueNext: %q %v", jobID, err)
	}
	client := env.hub.addClient()
	defer env.hub.removeClient(client)

	if _, err := env.cancel.RequestJobCancel(jobID); err != nil {
		t.Fatal(err)
	}
	evs := eventsOfType(readEvents(t, client, 200*time.Millisecond), "cancel_requested")
	if len(evs) != 1 {
		t.Fatalf("got %d cancel_requested events, want 1", len(evs))
	}
	e := evs[0]
	if e["jobId"] != jobID {
		t.Errorf("top-level jobId = %v, want %s: %v", e["jobId"], jobID, e["_raw"])
	}
	if e["appId"] != "a1" {
		t.Errorf("top-level appId = %v, want a1", e["appId"])
	}
	p := payloadOf(t, e)
	if p["jobId"] != jobID {
		t.Errorf("payload.jobId = %v, want %s (kept for one release, events-ws 3.5)", p["jobId"], jobID)
	}
	if p["status"] != "cancel_requested" || p["previousStatus"] != "in_progress" {
		t.Errorf("payload status/previousStatus = %v/%v, want cancel_requested/in_progress", p["status"], p["previousStatus"])
	}
}

// Canceling a PENDING job ends it at once: cancel_requested then job_status "canceled",
// both with appId and jobId at the top level and jobId in the payload.
func TestEvents_CancelOfPendingJobEmitsJobStatusWithIDs(t *testing.T) {
	env := newEventsEnv(t)
	if err := env.q.EnqueueManual("a1", "v1"); err != nil {
		t.Fatal(err)
	}
	var jobID string
	if err := env.q.DB().QueryRow(`SELECT id FROM deploy_jobs WHERE app_id='a1'`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	client := env.hub.addClient()
	defer env.hub.removeClient(client)
	if _, err := env.cancel.RequestJobCancel(jobID); err != nil {
		t.Fatal(err)
	}
	evs := readEvents(t, client, 200*time.Millisecond)
	js := eventsOfType(evs, "job_status")
	if len(js) != 1 {
		t.Fatalf("got %d job_status events, want 1: %v", len(js), evs)
	}
	if js[0]["jobId"] != jobID || js[0]["appId"] != "a1" || payloadOf(t, js[0])["jobId"] != jobID || payloadOf(t, js[0])["status"] != "canceled" {
		t.Errorf("job_status = %v", js[0]["_raw"])
	}
}

func TestEvents_SnapshotHasStatus(t *testing.T) {
	addr, hub := startEventsServer(t)
	hub.tracker.Start("a1", "j1", "v1.2.3")
	hub.tracker.Update("a1", 1024, 2048, 512)
	hub.tracker.Start("a2", "j2", "v1")
	hub.tracker.Finish("a2") // terminal entries are not part of the snapshot (FR-033)

	conn, resp, err := dialEvents(t, addr, adminEventsV1Path, sessionHeader(t))
	closeBody(resp)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	readFrame(t, conn) // hello
	var snap map[string]any
	if err := json.Unmarshal(readFrame(t, conn), &snap); err != nil {
		t.Fatal(err)
	}
	if snap["type"] != "snapshot" {
		t.Fatalf("second frame = %v, want snapshot", snap["type"])
	}
	jobs, _ := payloadOf(t, snap)["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("snapshot jobs = %v, want exactly the one active job", jobs)
	}
	j := jobs[0].(map[string]any)
	want := map[string]any{"appId": "a1", "jobId": "j1", "tag": "v1.2.3", "status": "in_progress", "phase": progress.ProgressStageDownloading}
	for k, v := range want {
		if j[k] != v {
			t.Errorf("job.%s = %v, want %v (job = %v)", k, j[k], v, j)
		}
	}
	if j["percent"] != float64(50) || j["downloadedBytes"] != float64(1024) || j["totalBytes"] != float64(2048) || j["speedBps"] != float64(512) {
		t.Errorf("progress numbers wrong: %v", j)
	}
}

func compileEventsSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "events-ws.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	const url = "https://auto-deploy.invalid/events-ws.schema.json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

func validateEvent(t *testing.T, sch *jsonschema.Schema, raw string) {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	if err := sch.Validate(v); err != nil {
		t.Errorf("event violates events-ws.schema.json: %s\n%v", raw, err)
	}
}

func TestEvents_AllEventsMatchSchema(t *testing.T) {
	sch := compileEventsSchema(t)

	t.Run("schema rejects the old shape", func(t *testing.T) {
		for _, bad := range []string{
			`{"protocol":"2","id":"evt-1","type":"cancel_requested","ts":"2026-11-24T10:00:00Z","payload":{"status":"cancel_requested"}}`, // no jobId
			`{"protocol":"v1","id":"evt-1","type":"heartbeat","ts":"2026-11-24T10:00:00Z"}`,                                               // old protocol
			`{"protocol":"2","id":"evt-1","type":"snapshot","ts":"2026-11-24T10:00:00Z","payload":{"jobs":[{"AppID":"a"}]}}`,              // Go field names
			`{"v":"2","t":"p","i":"evt-1","ts":"2026-11-24T10:00:00Z","a":"a","j":"j","ph":"downloading","db":1,"bps":1,"msg":""}`,        // no st
			`{"protocol":"2","id":"evt-1","type":"job_status","ts":"2026-11-24T10:00:00Z","jobId":"j","payload":{"status":"done"}}`,       // bad status
		} {
			var v any
			_ = json.Unmarshal([]byte(bad), &v)
			if err := sch.Validate(v); err == nil {
				t.Errorf("schema accepted an invalid event: %s", bad)
			}
		}
	})

	addr, hub := startEventsServer(t)
	hub.tracker.Start("a0", "j0", "v0")
	conn, resp, err := dialEvents(t, addr, adminEventsV1Path, sessionHeader(t))
	closeBody(resp)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for _, want := range []string{"hello", "snapshot"} {
		raw := readFrame(t, conn)
		validateEvent(t, sch, string(raw))
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["type"] != want {
			t.Errorf("frame type = %v, want %s", m["type"], want)
		}
	}

	// The live events: cancel service, tracker, notice, heartbeat and the coordinator's
	// job_status, all through one hub.
	env := newEventsEnv(t)
	client := env.hub.addClient()
	defer env.hub.removeClient(client)

	app, err := env.apps.Create("app", "secret1", "/bin/app", "app.service", "org/repo", "artifact")
	if err != nil {
		t.Fatal(err)
	}
	coord := coordinator.NewCoordinator(env.apps, env.q, func(a *store.App, jobID, tag string) (store.DownloadSummary, error) {
		env.tracker.Start(a.ID, jobID, tag)
		env.tracker.Update(a.ID, 10, 100, 5)
		env.tracker.Finish(a.ID)
		return store.DownloadSummary{}, fmt.Errorf("restart service: boom; %w", store.ErrRolledBack)
	}, env.tracker)
	coord.SetEventSink(env.hub)
	if err := env.q.Enqueue(app.ID, "v1"); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	coord.Start(ctx)

	if err := env.q.EnqueueManual("a1", "v9"); err != nil { // a job to cancel
		t.Fatal(err)
	}
	cancelID, _, _ := env.q.DequeueNext("a1")
	if _, err := env.cancel.RequestJobCancel(cancelID); err != nil {
		t.Fatal(err)
	}
	env.hub.Broadcast(AdminEvent{Type: adminEventTypeNotice, Payload: map[string]any{"level": "warning", "code": "telegram_reload_failed", "message": "could not reload"}})
	hb, err := env.hub.encodeEvent(AdminEvent{Type: adminEventTypeHeartbeat})
	if err != nil {
		t.Fatal(err)
	}
	validateEvent(t, sch, string(hb))

	time.Sleep(300 * time.Millisecond) // let the coordinator finish
	stop()
	coord.Wait()
	evs := readEvents(t, client, 200*time.Millisecond)
	seen := map[string]int{}
	for _, e := range evs {
		validateEvent(t, sch, e["_raw"].(string))
		if typ, ok := e["type"].(string); ok {
			seen[typ]++
		} else if e["t"] == "p" {
			seen["progress"]++
		}
	}
	for _, typ := range []string{"cancel_requested", "job_status", "progress", "notice"} {
		if seen[typ] == 0 {
			t.Errorf("no %q event was produced (seen %v)", typ, seen)
		}
	}
}
