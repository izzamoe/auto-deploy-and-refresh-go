package coordinator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/store"
)

func newTestCoordinator(t *testing.T, runner CoordinatorRunner) (*Coordinator, *store.AppStore, *store.DeployQueue) {
	t.Helper()
	db := newTestDB(t)
	apps, err := store.NewAppStore(db)
	if err != nil {
		t.Fatalf("store.NewAppStore: %v", err)
	}
	q, err := store.NewDeployQueue(db, 100)
	if err != nil {
		t.Fatalf("store.NewDeployQueue: %v", err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatalf("store.DeployQueue.Migrate: %v", err)
	}
	c := NewCoordinator(apps, q, runner, nil)
	return c, apps, q
}

func TestCoordinatorSerializesJobsPerApp(t *testing.T) {
	t.Parallel()
	var running atomic.Int32
	var maxConcurrent atomic.Int32
	var mu sync.Mutex
	var order []string

	runner := func(app *store.App, jobID, tag string) (store.DownloadSummary, error) {
		cur := running.Add(1)
		for {
			old := maxConcurrent.Load()
			if cur <= old || maxConcurrent.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		order = append(order, tag)
		mu.Unlock()
		running.Add(-1)
		return store.DownloadSummary{}, nil
	}

	c, apps, q := newTestCoordinator(t, runner)
	app, err := apps.Create("test-app", "secret1", "/bin/test1", "test1.service", "org/repo1", "artifact1")
	if err != nil {
		t.Fatalf("Create app: %v", err)
	}

	for _, tag := range []string{"v1", "v2"} {
		if err := q.Enqueue(app.ID, tag); err != nil {
			t.Fatalf("Enqueue(%s): %v", tag, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Start(ctx)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for jobs")
		default:
		}
		items, err := q.ListByStatus("succeeded")
		if err != nil {
			t.Fatalf("ListByStatus: %v", err)
		}
		if len(items) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	c.Wait()

	if maxConcurrent.Load() > 1 {
		t.Errorf("expected max concurrency 1 for same app, got %d", maxConcurrent.Load())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "v1" || order[1] != "v2" {
		t.Errorf("expected order [v1 v2], got %v", order)
	}
}

func TestCoordinatorAllowsDifferentAppsToProgress(t *testing.T) {
	t.Parallel()
	appAStarted := make(chan struct{})
	appARelease := make(chan struct{})
	var appBDone atomic.Bool

	runner := func(app *store.App, jobID, tag string) (store.DownloadSummary, error) {
		if app.Name == "app-a" {
			close(appAStarted)
			<-appARelease
		} else {
			appBDone.Store(true)
		}
		return store.DownloadSummary{}, nil
	}

	c, apps, q := newTestCoordinator(t, runner)
	appA, err := apps.Create("app-a", "secret-a", "/bin/a", "a.service", "org/a", "art-a")
	if err != nil {
		t.Fatalf("Create app-a: %v", err)
	}
	appB, err := apps.Create("app-b", "secret-b", "/bin/b", "b.service", "org/b", "art-b")
	if err != nil {
		t.Fatalf("Create app-b: %v", err)
	}

	if err := q.Enqueue(appA.ID, "v1"); err != nil {
		t.Fatalf("Enqueue app-a: %v", err)
	}
	if err := q.Enqueue(appB.ID, "v1"); err != nil {
		t.Fatalf("Enqueue app-b: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Start(ctx)

	select {
	case <-appAStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("app-a runner never started")
	}

	deadline := time.After(3 * time.Second)
	for !appBDone.Load() {
		select {
		case <-deadline:
			t.Fatal("app-b did not complete while app-a was blocked")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(appARelease)
	cancel()
	c.Wait()
}

func TestCoordinatorContinuesAfterFailure(t *testing.T) {
	t.Parallel()
	runner := func(app *store.App, jobID, tag string) (store.DownloadSummary, error) {
		if tag == "v1" {
			return store.DownloadSummary{}, fmt.Errorf("simulated failure")
		}
		return store.DownloadSummary{}, nil
	}

	c, apps, q := newTestCoordinator(t, runner)
	app, err := apps.Create("test-app", "secret1", "/bin/test1", "test1.service", "org/repo1", "artifact1")
	if err != nil {
		t.Fatalf("Create app: %v", err)
	}

	if err := q.Enqueue(app.ID, "v1"); err != nil {
		t.Fatalf("Enqueue(v1): %v", err)
	}
	if err := q.Enqueue(app.ID, "v2"); err != nil {
		t.Fatalf("Enqueue(v2): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Start(ctx)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for jobs")
		default:
		}
		failed, _ := q.ListByStatus("failed")
		succeeded, _ := q.ListByStatus("succeeded")
		if len(failed)+len(succeeded) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	c.Wait()

	failed, _ := q.ListByStatus("failed")
	if len(failed) != 1 || failed[0].Tag != "v1" {
		t.Errorf("expected 1 failed job (v1), got %v", failed)
	}
	succeeded, _ := q.ListByStatus("succeeded")
	if len(succeeded) != 1 || succeeded[0].Tag != "v2" {
		t.Errorf("expected 1 succeeded job (v2), got %v", succeeded)
	}
}

func TestCoordinatorStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	runner := func(app *store.App, jobID, tag string) (store.DownloadSummary, error) {
		return store.DownloadSummary{}, nil
	}

	c, apps, _ := newTestCoordinator(t, runner)
	_, err := apps.Create("test-app", "secret1", "/bin/test1", "test1.service", "org/repo1", "artifact1")
	if err != nil {
		t.Fatalf("Create app: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	cancel()

	done := make(chan struct{})
	go func() {
		c.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("coordinator did not stop within 2 seconds after context cancellation")
	}
}

func TestCoordinatorSummaryPersisted(t *testing.T) {
	t.Parallel()
	wantSummary := store.DownloadSummary{Bytes: 4096, DurationMs: 200, SpeedBPS: 20480}

	runner := func(app *store.App, jobID, tag string) (store.DownloadSummary, error) {
		return wantSummary, nil
	}

	c, apps, q := newTestCoordinator(t, runner)
	app, err := apps.Create("test-app", "secret1", "/bin/test1", "test1.service", "org/repo1", "artifact1")
	if err != nil {
		t.Fatalf("Create app: %v", err)
	}

	if err := q.Enqueue(app.ID, "v1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Start(ctx)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for job to succeed")
		default:
		}
		items, err := q.ListByStatus("succeeded")
		if err != nil {
			t.Fatalf("ListByStatus: %v", err)
		}
		if len(items) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	c.Wait()

	items, _ := q.ListByStatus("succeeded")
	if len(items) != 1 {
		t.Fatalf("expected 1 succeeded job, got %d", len(items))
	}

	job := items[0]
	if job.DownloadBytes != wantSummary.Bytes {
		t.Errorf("DownloadBytes = %d, want %d", job.DownloadBytes, wantSummary.Bytes)
	}
	if job.DownloadDurationMs != wantSummary.DurationMs {
		t.Errorf("DownloadDurationMs = %d, want %d", job.DownloadDurationMs, wantSummary.DurationMs)
	}
	if job.DownloadSpeedBPS != wantSummary.SpeedBPS {
		t.Errorf("DownloadSpeedBPS = %v, want %v", job.DownloadSpeedBPS, wantSummary.SpeedBPS)
	}
}

// B23 / FR-048: an idle coordinator must not hammer the database. It used to list the
// apps every 100 ms (10+ queries/s); the notification-based loop needs at most one per
// fallback interval (>= 2 s).
func TestCoordinator_IdleDoesNotPollTheAppsTable(t *testing.T) {
	c, _, _ := newTestCoordinator(t, func(*store.App, string, string) (store.DownloadSummary, error) {
		return store.DownloadSummary{}, nil
	})
	var calls atomic.Int32
	inner := c.listApps
	c.listApps = func() ([]store.App, error) {
		calls.Add(1)
		return inner()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	time.Sleep(2 * time.Second)
	cancel()
	c.Wait()
	if n := calls.Load(); n > 2 {
		t.Fatalf("B23: %d apps.List queries in 2 s of idling (%.0f/s); want <= 1 per 2 s fallback interval plus the first pass", n, float64(n)/2)
	}
}

// FR-048: a new job starts at once (wake channel), not at the next poll.
func TestCoordinator_EnqueueStartsTheJobWithoutWaitingForThePollInterval(t *testing.T) {
	started := make(chan time.Time, 4)
	c, apps, q := newTestCoordinator(t, func(*store.App, string, string) (store.DownloadSummary, error) {
		started <- time.Now()
		return store.DownloadSummary{}, nil
	})
	app, err := apps.Create("wake", "sec-wake-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "/opt/wake/wake", "wake.service", "o/r", "art")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); c.Wait() }()
	c.Start(ctx)
	time.Sleep(300 * time.Millisecond) // the scheduler is idle and waiting
	enqueuedAt := time.Now()
	if err := q.Enqueue(app.ID, "v1"); err != nil {
		t.Fatal(err)
	}
	select {
	case at := <-started:
		if d := at.Sub(enqueuedAt); d > 200*time.Millisecond {
			t.Fatalf("the job started %v after the enqueue, want < 200 ms (fallback poll is %v)", d, pollFallback)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the job never started")
	}
}

// FR-048: a job queued behind a running one of the same app starts as soon as that one ends.
func TestCoordinator_NextJobOfTheSameAppStartsWhenTheRunningOneEnds(t *testing.T) {
	release := make(chan struct{})
	var starts []time.Time
	var mu sync.Mutex
	c, apps, q := newTestCoordinator(t, func(_ *store.App, _ string, tag string) (store.DownloadSummary, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		n := len(starts)
		mu.Unlock()
		if n == 1 {
			<-release
		}
		return store.DownloadSummary{}, nil
	})
	app, err := apps.Create("seq", "sec-seq-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "/opt/seq/seq", "seq.service", "o/r", "art")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); c.Wait() }()
	c.Start(ctx)
	_ = q.Enqueue(app.ID, "v1")
	_ = q.Enqueue(app.ID, "v2")
	time.Sleep(300 * time.Millisecond)
	releasedAt := time.Now()
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(starts)
		var second time.Time
		if n >= 2 {
			second = starts[1]
		}
		mu.Unlock()
		if n >= 2 {
			if d := second.Sub(releasedAt); d > 300*time.Millisecond {
				t.Fatalf("the queued job started %v after the first one ended, want < 300 ms", d)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the second job never started")
}

// B26 / FR-051: a panic inside the deploy runner must not take the process down: the job
// is marked failed and the coordinator keeps serving the queue.
func TestCoordinator_PanicInRunnerFailsTheJobAndKeepsRunning(t *testing.T) {
	var calls atomic.Int32
	c, apps, q := newTestCoordinator(t, func(_ *store.App, _ string, tag string) (store.DownloadSummary, error) {
		calls.Add(1)
		if tag == "v-boom" {
			panic("runner exploded: token=hunter2")
		}
		return store.DownloadSummary{}, nil
	})
	app, err := apps.Create("pan", "sec-pan-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "/opt/pan/pan", "pan.service", "o/r", "art")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); c.Wait() }()
	c.Start(ctx)
	if err := q.Enqueue(app.ID, "v-boom"); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(app.ID, "v-after"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		failed, _ := q.ListByStatus("failed")
		done, _ := q.ListByStatus("succeeded")
		if len(failed) == 1 && len(done) == 1 {
			if msg := failed[0].ErrorMsg; strings.Contains(msg, "hunter2") {
				t.Errorf("the panic value leaked into the job error: %q", msg)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	failed, _ := q.ListByStatus("failed")
	done, _ := q.ListByStatus("succeeded")
	t.Fatalf("after a runner panic: %d failed, %d succeeded jobs; want 1 failed (the panic) and 1 succeeded (the next job)", len(failed), len(done))
}

// FR-051: MarkDone is retried; a transient failure does not leave the job in_progress.
func TestCoordinator_MarkDoneIsRetried(t *testing.T) {
	old := markDoneBackoff
	markDoneBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	defer func() { markDoneBackoff = old }()

	c, apps, q := newTestCoordinator(t, func(*store.App, string, string) (store.DownloadSummary, error) {
		return store.DownloadSummary{}, nil
	})
	var attempts atomic.Int32
	inner := c.markDone
	c.markDone = func(id string, ok bool, msg string, sum *store.DownloadSummary) error {
		if attempts.Add(1) <= 2 {
			return fmt.Errorf("database is locked")
		}
		return inner(id, ok, msg, sum)
	}
	app, err := apps.Create("ret", "sec-ret-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "/opt/ret/ret", "ret.service", "o/r", "art")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); c.Wait() }()
	c.Start(ctx)
	if err := q.Enqueue(app.ID, "v1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done, _ := q.ListByStatus("succeeded"); len(done) == 1 {
			if attempts.Load() != 3 {
				t.Errorf("MarkDone attempts = %d, want 3 (two failures, then success)", attempts.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the job never reached succeeded (MarkDone attempts: %d)", attempts.Load())
}
