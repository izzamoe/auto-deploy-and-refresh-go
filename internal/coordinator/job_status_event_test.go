package coordinator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// T044 (B11, events-ws 3.4): after MarkDone the coordinator announces the terminal
// status with a job_status event (appId + jobId top-level, jobId also in the payload).

type captureSink struct {
	mu  sync.Mutex
	evs []cancel.CancelEvent
}

func (s *captureSink) BroadcastCancelEvent(e cancel.CancelEvent) {
	s.mu.Lock()
	s.evs = append(s.evs, e)
	s.mu.Unlock()
}

func (s *captureSink) events() []cancel.CancelEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]cancel.CancelEvent(nil), s.evs...)
}

// runOneJob runs a single deploy through a coordinator and waits for it to end.
func runOneJob(t *testing.T, runner CoordinatorRunner, sink cancel.EventSink) (store.App, string, *store.DeployQueue) {
	t.Helper()
	c, apps, q := newTestCoordinator(t, runner)
	if _, err := store.NewAdminUserStore(q.DB()); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(q.DB()); err != nil {
		t.Fatal(err)
	}
	if sink != nil {
		c.SetEventSink(sink)
	}
	app, err := apps.Create("app", "secret1", "/bin/app", "app.service", "org/repo", "artifact")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(app.ID, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	ctx, cancelCtx := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCtx()
	c.Start(ctx)
	deadline := time.After(5 * time.Second)
	var jobID string
	for {
		var status string
		err := q.DB().QueryRow(`SELECT id, status FROM deploy_jobs WHERE app_id = ?`, app.ID).Scan(&jobID, &status)
		if err == nil && (status == "succeeded" || status == "failed" || status == "canceled") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("job never finished")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancelCtx()
	c.Wait()
	return *app, jobID, q
}

func jobStatusEvents(evs []cancel.CancelEvent) []cancel.CancelEvent {
	var out []cancel.CancelEvent
	for _, e := range evs {
		if e.Type == cancel.CancelEventTypeJobStatus {
			out = append(out, e)
		}
	}
	return out
}

func TestEvents_JobStatusOnMarkDone(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus string
		rolledBack bool
		errorCode  string
	}{
		{"succeeded", nil, "succeeded", false, ""},
		{"failed", errors.New("download failed"), "failed", false, "deploy_failed"},
		{"failed and rolled back", fmt.Errorf("restart service: boom; %w", store.ErrRolledBack), "failed", true, "rolled_back"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &captureSink{}
			app, jobID, _ := runOneJob(t, func(*store.App, string, string) (store.DownloadSummary, error) {
				return store.DownloadSummary{}, tc.err
			}, sink)

			evs := jobStatusEvents(sink.events())
			if len(evs) != 1 {
				t.Fatalf("got %d job_status events, want exactly 1: %+v", len(evs), sink.events())
			}
			e := evs[0]
			if e.AppID != app.ID || e.JobID != jobID {
				t.Errorf("event ids = (%q, %q), want (%q, %q)", e.AppID, e.JobID, app.ID, jobID)
			}
			p := e.Payload
			if p["jobId"] != jobID {
				t.Errorf("payload.jobId = %v, want %s (kept for one release)", p["jobId"], jobID)
			}
			if p["status"] != tc.wantStatus || p["previousStatus"] != "in_progress" || p["tag"] != "v1.2.3" {
				t.Errorf("payload = %+v, want status=%s previousStatus=in_progress tag=v1.2.3", p, tc.wantStatus)
			}
			if got, _ := p["rolledBack"].(bool); got != tc.rolledBack {
				t.Errorf("rolledBack = %v, want %v", p["rolledBack"], tc.rolledBack)
			}
			code, hasCode := p["errorCode"].(string)
			if tc.errorCode == "" && hasCode {
				t.Errorf("errorCode = %q on a success, want none", code)
			}
			if tc.errorCode != "" && code != tc.errorCode {
				t.Errorf("errorCode = %q, want %q", code, tc.errorCode)
			}
			if ts, _ := p["finishedAt"].(string); ts == "" {
				t.Errorf("finishedAt missing: %+v", p)
			} else if _, err := time.Parse(time.RFC3339, ts); err != nil {
				t.Errorf("finishedAt %q is not RFC 3339: %v", ts, err)
			}
		})
	}
}

// A job the cancel service already ended (checkpoint -> canceled) was announced by
// the cancel service; the coordinator must not announce it a second time.
func TestEvents_NoDuplicateJobStatusWhenCancelServiceEndedJob(t *testing.T) {
	sink := &captureSink{}
	var q *store.DeployQueue
	runner := func(app *store.App, jobID, tag string) (store.DownloadSummary, error) {
		_, err := q.DB().Exec(`UPDATE deploy_jobs SET status = 'canceled' WHERE id = ?`, jobID)
		if err != nil {
			t.Error(err)
		}
		return store.DownloadSummary{}, errors.New("deploy canceled at safe checkpoint")
	}
	c, apps, qq := newTestCoordinator(t, runner)
	q = qq
	if _, err := store.NewAdminUserStore(q.DB()); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(q.DB()); err != nil {
		t.Fatal(err)
	}
	c.SetEventSink(sink)
	app, err := apps.Create("app", "secret1", "/bin/app", "app.service", "org/repo", "artifact")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(app.ID, "v1"); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	c.Start(ctx)
	deadline := time.After(5 * time.Second)
	for {
		var status string
		if err := q.DB().QueryRow(`SELECT status FROM deploy_jobs WHERE app_id = ?`, app.ID).Scan(&status); err == nil && status == "canceled" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("job never reached canceled")
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(150 * time.Millisecond) // give the coordinator time to (wrongly) publish
	stop()
	c.Wait()
	if evs := jobStatusEvents(sink.events()); len(evs) != 0 {
		t.Errorf("coordinator published %d job_status events for a job the cancel service ended: %+v", len(evs), evs)
	}
}
