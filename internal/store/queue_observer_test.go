package store

import (
	"sync"
	"testing"
)

// T041 (FR-006, FR-004): every transition of deploy_jobs reaches the JobObserver, once,
// after it is committed (the observer reads the committed row back), and a write that
// changed nothing is not reported. MarkRolledBack, RecoverStale and PruneHistory are
// asserted here, not in TestMutationEvents_Table (RV3-13).

type recordingObserver struct {
	q  *DeployQueue
	mu sync.Mutex
	ts []JobTransition
	// seen is the status the database held when the observer was called (proof that the
	// transition was committed first); "" for pruned.
	seen []string
}

func (o *recordingObserver) OnJobTransition(t JobTransition) {
	var status string
	if t.JobID != "" {
		_ = o.q.DB().QueryRow(`SELECT status FROM deploy_jobs WHERE id = ?`, t.JobID).Scan(&status)
	}
	o.mu.Lock()
	o.ts = append(o.ts, t)
	o.seen = append(o.seen, status)
	o.mu.Unlock()
}

func (o *recordingObserver) take() ([]JobTransition, []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ts, seen := o.ts, o.seen
	o.ts, o.seen = nil, nil
	return ts, seen
}

func newObservedQueue(t *testing.T) (*DeployQueue, *recordingObserver) {
	t.Helper()
	q := newTestQueue(t, 10)
	q.DB().SetMaxOpenConns(1) // a callback inside an open transaction would dead-lock here
	if _, err := NewAppStore(q.DB()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAdminUserStore(q.DB()); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(q.DB()); err != nil { // deploy_jobs.rolled_back
		t.Fatal(err)
	}
	o := &recordingObserver{q: q}
	q.SetJobObserver(o)
	return q, o
}

func wantOne(t *testing.T, what string, ts []JobTransition, seen []string, want JobTransition) {
	t.Helper()
	if len(ts) != 1 {
		t.Fatalf("%s: %d transitions, want 1: %+v", what, len(ts), ts)
	}
	got := ts[0]
	if want.JobID == "" {
		want.JobID = got.JobID
	}
	if got != want {
		t.Fatalf("%s: transition = %+v, want %+v", what, got, want)
	}
	if want.Kind != JobsPruned && seen[0] != want.Status {
		t.Fatalf("%s: the observer read status %q, want the committed %q", what, seen[0], want.Status)
	}
}

func TestQueue_Observer(t *testing.T) {
	t.Run("enqueue_dequeue_markdone", func(t *testing.T) {
		q, o := newObservedQueue(t)
		if err := q.Enqueue("app-1", "v1.0.0"); err != nil {
			t.Fatal(err)
		}
		ts, seen := o.take()
		wantOne(t, "Enqueue", ts, seen, JobTransition{Kind: JobEnqueued, AppID: "app-1", Tag: "v1.0.0", Trigger: "webhook", Status: "pending"})
		job1 := ts[0].JobID

		id, tag, err := q.DequeueNext("app-1")
		if err != nil || id != job1 || tag != "v1.0.0" {
			t.Fatalf("DequeueNext = %q %q %v", id, tag, err)
		}
		ts, seen = o.take()
		wantOne(t, "DequeueNext", ts, seen, JobTransition{Kind: JobStarted, JobID: job1, AppID: "app-1", Tag: "v1.0.0", Trigger: "webhook", Status: "in_progress", PreviousStatus: "pending"})

		if err := q.MarkDone(job1, true, "", nil); err != nil {
			t.Fatal(err)
		}
		ts, seen = o.take()
		wantOne(t, "MarkDone", ts, seen, JobTransition{Kind: JobFinished, JobID: job1, AppID: "app-1", Tag: "v1.0.0", Trigger: "webhook", Status: "succeeded"})

		// A second MarkDone changes nothing and is not a transition.
		if err := q.MarkDone(job1, false, "late", nil); err != nil {
			t.Fatal(err)
		}
		if ts, _ := o.take(); len(ts) != 0 {
			t.Fatalf("no-op MarkDone reported %+v", ts)
		}
		// An empty queue: DequeueNext reports nothing.
		if id, _, err := q.DequeueNext("app-1"); err != nil || id != "" {
			t.Fatalf("DequeueNext on empty queue = %q %v", id, err)
		}
		if ts, _ := o.take(); len(ts) != 0 {
			t.Fatalf("empty DequeueNext reported %+v", ts)
		}
	})

	t.Run("manual_retry_failure_rollback", func(t *testing.T) {
		q, o := newObservedQueue(t)
		if err := q.EnqueueManual("app-2", "v2.0.0"); err != nil {
			t.Fatal(err)
		}
		ts, seen := o.take()
		wantOne(t, "EnqueueManual", ts, seen, JobTransition{Kind: JobEnqueued, AppID: "app-2", Tag: "v2.0.0", Trigger: "manual_deploy", Status: "pending"})
		job := ts[0].JobID
		if _, _, err := q.DequeueNext("app-2"); err != nil {
			t.Fatal(err)
		}
		o.take()
		if err := q.MarkDone(job, false, "boom: secret detail", nil); err != nil {
			t.Fatal(err)
		}
		ts, seen = o.take()
		wantOne(t, "MarkDone(failure)", ts, seen, JobTransition{Kind: JobFinished, JobID: job, AppID: "app-2", Tag: "v2.0.0", Trigger: "manual_deploy", Status: "failed"})

		if err := q.MarkRolledBack(job); err != nil {
			t.Fatal(err)
		}
		ts, seen = o.take()
		wantOne(t, "MarkRolledBack", ts, seen, JobTransition{Kind: JobRolledBack, JobID: job, AppID: "app-2", Tag: "v2.0.0", Trigger: "manual_deploy", Status: "failed"})
		if err := q.MarkRolledBack("no-such-job"); err != nil {
			t.Fatal(err)
		}
		if ts, _ := o.take(); len(ts) != 0 {
			t.Fatalf("MarkRolledBack of an unknown job reported %+v", ts)
		}

		retry, err := q.CreateRetryJob(job, "app-2", "v2.0.0")
		if err != nil {
			t.Fatal(err)
		}
		ts, seen = o.take()
		wantOne(t, "CreateRetryJob", ts, seen, JobTransition{Kind: JobEnqueued, JobID: retry, AppID: "app-2", Tag: "v2.0.0", Trigger: "manual_retry", Status: "pending"})

		// A refused enqueue (duplicate) is not a transition.
		if err := q.EnqueueManual("app-2", "v2.0.0"); err != ErrDuplicate {
			t.Fatalf("duplicate EnqueueManual = %v, want ErrDuplicate", err)
		}
		if ts, _ := o.take(); len(ts) != 0 {
			t.Fatalf("refused enqueue reported %+v", ts)
		}
	})

	t.Run("recover_stale", func(t *testing.T) {
		q, o := newObservedQueue(t)
		for _, app := range []string{"a", "b"} {
			if err := q.Enqueue(app, "v1.0.0"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := q.DequeueNext(app); err != nil {
				t.Fatal(err)
			}
		}
		if err := q.Enqueue("c", "v1.0.0"); err != nil { // stays pending: not recovered
			t.Fatal(err)
		}
		o.take()
		if _, err := q.DB().Exec(`UPDATE deploy_jobs SET status = 'cancel_requested' WHERE app_id = 'b'`); err != nil {
			t.Fatal(err)
		}
		if err := q.RecoverStale(); err != nil {
			t.Fatal(err)
		}
		ts, seen := o.take()
		if len(ts) != 2 {
			t.Fatalf("RecoverStale: %d transitions, want 2: %+v", len(ts), ts)
		}
		for i, want := range []JobTransition{
			{Kind: JobRecovered, AppID: "a", Tag: "v1.0.0", Trigger: "webhook", Status: "failed", PreviousStatus: "in_progress"},
			{Kind: JobRecovered, AppID: "b", Tag: "v1.0.0", Trigger: "webhook", Status: "failed", PreviousStatus: "cancel_requested"},
		} {
			wantOne(t, "RecoverStale", ts[i:i+1], seen[i:i+1], want)
		}
		if err := q.RecoverStale(); err != nil {
			t.Fatal(err)
		}
		if ts, _ := o.take(); len(ts) != 0 {
			t.Fatalf("RecoverStale with nothing stale reported %+v", ts)
		}
	})

	t.Run("prune_history", func(t *testing.T) {
		q, o := newObservedQueue(t)
		for _, tag := range []string{"v1.0.0", "v1.0.1", "v1.0.2"} {
			if err := q.Enqueue("app-p", tag); err != nil {
				t.Fatal(err)
			}
			id, _, err := q.DequeueNext("app-p")
			if err != nil {
				t.Fatal(err)
			}
			if err := q.MarkDone(id, true, "", nil); err != nil {
				t.Fatal(err)
			}
		}
		o.take()
		n, err := q.PruneHistory(1)
		if err != nil || n != 2 {
			t.Fatalf("PruneHistory = %d %v, want 2", n, err)
		}
		ts, seen := o.take()
		wantOne(t, "PruneHistory", ts, seen, JobTransition{Kind: JobsPruned, Count: 2})
		if n, err := q.PruneHistory(1); err != nil || n != 0 {
			t.Fatalf("second PruneHistory = %d %v", n, err)
		}
		if ts, _ := o.take(); len(ts) != 0 {
			t.Fatalf("PruneHistory that deleted nothing reported %+v", ts)
		}
	})

	t.Run("detached", func(t *testing.T) {
		q, o := newObservedQueue(t)
		q.SetJobObserver(nil)
		if err := q.Enqueue("app-x", "v1.0.0"); err != nil {
			t.Fatal(err)
		}
		if err := q.RecoverStale(); err != nil {
			t.Fatal(err)
		}
		if ts, _ := o.take(); len(ts) != 0 {
			t.Fatalf("a detached observer was called: %+v", ts)
		}
	})
}
