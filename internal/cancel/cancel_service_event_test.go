package cancel

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/store"
)

// T041 (RV2-5, RV3-9): the cancel service is a source of job events through its existing
// EventSink seam (internal/cancel never imports internal/events). Every event is sent
// AFTER the change is committed: the sink reads the job row back and must see the new
// status. With one database connection a call inside the open transaction would block, so
// the read has a deadline.

type committedSink struct {
	db *sql.DB
	mu sync.Mutex
	// got holds "<type>/<payload status>@<status read from the database>".
	got []string
}

func (s *committedSink) BroadcastCancelEvent(e CancelEvent) {
	read := make(chan string, 1)
	go func() {
		var status string
		if err := s.db.QueryRow(`SELECT status FROM deploy_jobs WHERE id = ?`, e.JobID).Scan(&status); err != nil {
			status = "error:" + err.Error()
		}
		read <- status
	}()
	var dbStatus string
	select {
	case dbStatus = <-read:
	case <-time.After(2 * time.Second):
		dbStatus = "BLOCKED (sent inside the open transaction)"
	}
	s.mu.Lock()
	s.got = append(s.got, e.Type+"/"+e.Payload["status"].(string)+"@"+dbStatus)
	s.mu.Unlock()
}

func (s *committedSink) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.got
	s.got = nil
	return out
}

func newEventQueue(t *testing.T) (*store.DeployQueue, *CancelService, *committedSink) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cancel-events.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	q, err := store.NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	svc := NewCancelService(q)
	sink := &committedSink{db: db}
	svc.SetEventSink(sink)
	return q, svc, sink
}

func enqueueEventJob(t *testing.T, q *store.DeployQueue, appID, tag string, start bool) string {
	t.Helper()
	if err := q.Enqueue(appID, tag); err != nil {
		t.Fatal(err)
	}
	if start {
		id, _, err := q.DequeueNext(appID)
		if err != nil || id == "" {
			t.Fatalf("DequeueNext: %q %v", id, err)
		}
		return id
	}
	var id string
	if err := q.DB().QueryRow(`SELECT id FROM deploy_jobs WHERE app_id = ? AND tag = ?`, appID, tag).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func wantEvents(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: events %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: event %d = %q, want %q (all: %v)", what, i, got[i], want[i], got)
		}
	}
}

func TestCancelService_EventsAfterCommit(t *testing.T) {
	t.Run("queued_job", func(t *testing.T) {
		q, svc, sink := newEventQueue(t)
		running := enqueueEventJob(t, q, "app", "v1.0.0", true)
		queued := enqueueEventJob(t, q, "app", "v2.0.0", false)
		_ = running
		if _, err := svc.RequestJobCancel(queued); err != nil {
			t.Fatal(err)
		}
		// cancel_requested{canceled} and the terminal job_status, both after the commit.
		wantEvents(t, "RequestJobCancel(pending)", sink.take(), "cancel_requested/canceled@canceled", "job_status/canceled@canceled")
	})

	t.Run("running_job", func(t *testing.T) {
		q, svc, sink := newEventQueue(t)
		running := enqueueEventJob(t, q, "app", "v1.0.0", true)
		if _, err := svc.RequestJobCancel(running); err != nil {
			t.Fatal(err)
		}
		wantEvents(t, "RequestJobCancel(in_progress)", sink.take(), "cancel_requested/cancel_requested@cancel_requested")
		// Asking again changes nothing in the database but is reported (idempotent request).
		if _, err := svc.RequestJobCancel(running); err != nil {
			t.Fatal(err)
		}
		wantEvents(t, "RequestJobCancel(cancel_requested)", sink.take(), "cancel_requested/cancel_requested@cancel_requested")
	})

	t.Run("app_cancel", func(t *testing.T) {
		q, svc, sink := newEventQueue(t)
		enqueueEventJob(t, q, "app", "v1.0.0", true)
		enqueueEventJob(t, q, "app", "v2.0.0", false)
		if _, err := svc.RequestAppCancel("app"); err != nil {
			t.Fatal(err)
		}
		wantEvents(t, "RequestAppCancel", sink.take(),
			"cancel_requested/cancel_requested@cancel_requested",
			"cancel_requested/canceled@canceled", "job_status/canceled@canceled")
	})

	t.Run("checkpoint_mark_terminal", func(t *testing.T) {
		q, svc, sink := newEventQueue(t)
		running := enqueueEventJob(t, q, "app", "v1.0.0", true)
		if _, err := svc.RequestJobCancel(running); err != nil {
			t.Fatal(err)
		}
		sink.take()
		d, err := svc.Checkpoint(running, DeployPhaseCleanupComplete)
		if err != nil || d.TerminalStatus != JobStatusCanceled {
			t.Fatalf("Checkpoint = %+v, %v", d, err)
		}
		wantEvents(t, "Checkpoint(markTerminal)", sink.take(), "job_status/canceled@canceled")
	})

	t.Run("checkpoint_after_markdone_is_silent", func(t *testing.T) {
		q, svc, sink := newEventQueue(t)
		running := enqueueEventJob(t, q, "app", "v1.0.0", true)
		if _, err := svc.RequestJobCancel(running); err != nil {
			t.Fatal(err)
		}
		sink.take()
		// The job ends through MarkDone between the status read and the UPDATE of the
		// checkpoint: markTerminal changes no row, so the cancel service announces nothing
		// (the coordinator announces the MarkDone).
		var appID string
		if err := q.DB().QueryRow(`SELECT app_id FROM deploy_jobs WHERE id = ?`, running).Scan(&appID); err != nil {
			t.Fatal(err)
		}
		changed, err := svc.markTerminal(running, JobStatusCanceled, "late")
		if err != nil || !changed {
			t.Fatalf("first markTerminal = %v %v, want changed", changed, err)
		}
		changed, err = svc.markTerminal(running, JobStatusFailed, "later")
		if err != nil || changed {
			t.Fatalf("second markTerminal = %v %v, want no change", changed, err)
		}
		if got := sink.take(); len(got) != 0 {
			t.Fatalf("markTerminal alone published %v", got)
		}
	})

	t.Run("terminal_and_unknown_are_silent", func(t *testing.T) {
		q, svc, sink := newEventQueue(t)
		running := enqueueEventJob(t, q, "app", "v1.0.0", true)
		if err := q.MarkDone(running, true, "", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RequestJobCancel(running); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RequestJobCancel("no-such-job"); err != nil {
			t.Fatal(err)
		}
		if got := sink.take(); len(got) != 0 {
			t.Fatalf("terminal/unknown cancel published %v", got)
		}
	})
}
