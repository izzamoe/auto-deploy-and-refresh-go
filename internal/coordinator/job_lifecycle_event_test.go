package coordinator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// T041 (FR-006, FR-004): the lifecycle of a job as the realtime sources see it. The queue
// observer (store.JobObserver) reports every committed transition, the coordinator's event
// sink the terminal job_status; together, in this order: pending (enqueue, webhook/manual/
// retry), in_progress (DequeueNext), the terminal MarkDone, the rollback flag, and only then
// the coordinator's job_status (which carries rolledBack). A self-deploy in flight ends
// nothing.

type lifecycleLog struct {
	mu  sync.Mutex
	log []string
}

func (l *lifecycleLog) add(s string) {
	l.mu.Lock()
	l.log = append(l.log, s)
	l.mu.Unlock()
}

func (l *lifecycleLog) OnJobTransition(t store.JobTransition) {
	l.add(fmt.Sprintf("queue:%s:%s:%s:%s", t.Kind, t.Status, t.Trigger, t.Tag))
}

func (l *lifecycleLog) BroadcastCancelEvent(e cancel.CancelEvent) {
	l.add(fmt.Sprintf("sink:%s:%v:%v:%v:%v", e.Type, e.Payload["status"], e.Payload["tag"], e.Payload["rolledBack"], e.Payload["errorCode"]))
}

func (l *lifecycleLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.log...)
}

// waitFor waits until the log has an entry with prefix, then returns the log.
func (l *lifecycleLog) waitFor(t *testing.T, prefix string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		log := l.snapshot()
		for _, s := range log {
			if strings.HasPrefix(s, prefix) {
				return log
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q within 5 s; log: %v", prefix, log)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newLifecycleCoordinator(t *testing.T, runner CoordinatorRunner) (*Coordinator, *store.DeployQueue, string, *lifecycleLog) {
	t.Helper()
	c, apps, q := newTestCoordinator(t, runner)
	if _, err := store.NewAdminUserStore(q.DB()); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(q.DB()); err != nil { // deploy_jobs.rolled_back
		t.Fatal(err)
	}
	app, err := apps.Create("app", "secret1", "/bin/app", "app.service", "org/repo", "artifact")
	if err != nil {
		t.Fatal(err)
	}
	l := &lifecycleLog{}
	q.SetJobObserver(l)
	c.SetEventSink(l)
	ctx, stop := context.WithCancel(context.Background())
	c.Start(ctx)
	t.Cleanup(func() {
		stop()
		c.Wait()
	})
	return c, q, app.ID, l
}

func wantLog(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("lifecycle = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lifecycle[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestJobLifecycleEvents(t *testing.T) {
	t.Run("webhook_success", func(t *testing.T) {
		_, q, appID, l := newLifecycleCoordinator(t, func(*store.App, string, string) (store.DownloadSummary, error) {
			return store.DownloadSummary{}, nil
		})
		if err := q.Enqueue(appID, "v1.0.0"); err != nil {
			t.Fatal(err)
		}
		wantLog(t, l.waitFor(t, "sink:job_status"),
			"queue:enqueued:pending:webhook:v1.0.0",
			"queue:started:in_progress:webhook:v1.0.0",
			"queue:finished:succeeded:webhook:v1.0.0",
			"sink:job_status:succeeded:v1.0.0:false:<nil>",
		)
	})

	t.Run("manual_failure_rolled_back", func(t *testing.T) {
		_, q, appID, l := newLifecycleCoordinator(t, func(*store.App, string, string) (store.DownloadSummary, error) {
			return store.DownloadSummary{}, fmt.Errorf("health check failed: %w", store.ErrRolledBack)
		})
		if err := q.EnqueueManual(appID, "v2.0.0"); err != nil {
			t.Fatal(err)
		}
		wantLog(t, l.waitFor(t, "sink:job_status"),
			"queue:enqueued:pending:manual_deploy:v2.0.0",
			"queue:started:in_progress:manual_deploy:v2.0.0",
			"queue:finished:failed:manual_deploy:v2.0.0",
			"queue:rolled_back:failed:manual_deploy:v2.0.0",
			"sink:job_status:failed:v2.0.0:true:rolled_back",
		)
	})

	t.Run("retry", func(t *testing.T) {
		_, q, appID, l := newLifecycleCoordinator(t, func(*store.App, string, string) (store.DownloadSummary, error) {
			return store.DownloadSummary{}, nil
		})
		if err := q.Enqueue(appID, "v3.0.0"); err != nil {
			t.Fatal(err)
		}
		l.waitFor(t, "sink:job_status")
		var first string
		if err := q.DB().QueryRow(`SELECT id FROM deploy_jobs WHERE app_id = ?`, appID).Scan(&first); err != nil {
			t.Fatal(err)
		}
		l.mu.Lock()
		l.log = nil
		l.mu.Unlock()
		if _, err := q.CreateRetryJob(first, appID, "v3.0.0"); err != nil {
			t.Fatal(err)
		}
		wantLog(t, l.waitFor(t, "sink:job_status"),
			"queue:enqueued:pending:manual_retry:v3.0.0",
			"queue:started:in_progress:manual_retry:v3.0.0",
			"queue:finished:succeeded:manual_retry:v3.0.0",
			"sink:job_status:succeeded:v3.0.0:false:<nil>",
		)
	})

	t.Run("self_deploy_in_flight", func(t *testing.T) {
		ran := make(chan struct{})
		_, q, appID, l := newLifecycleCoordinator(t, func(*store.App, string, string) (store.DownloadSummary, error) {
			defer close(ran)
			return store.DownloadSummary{}, store.ErrSelfDeployInFlight
		})
		if err := q.Enqueue(appID, "v4.0.0"); err != nil {
			t.Fatal(err)
		}
		<-ran
		time.Sleep(200 * time.Millisecond)
		wantLog(t, l.snapshot(),
			"queue:enqueued:pending:webhook:v4.0.0",
			"queue:started:in_progress:webhook:v4.0.0",
		)
	})
}
