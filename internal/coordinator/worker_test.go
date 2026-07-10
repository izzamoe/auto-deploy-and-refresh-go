package coordinator

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/store"
)

func TestWorkerProcessesFIFOSequentially(t *testing.T) {
	t.Parallel()
	q := newTestQueue(t, 10)

	for _, tag := range []string{"v1", "v2", "v3"} {
		if err := q.Enqueue("legacy", tag); err != nil {
			t.Fatalf("Enqueue(%s): %v", tag, err)
		}
	}

	var mu sync.Mutex
	var order []string

	runner := DeployRunner(func(tag string) error {
		mu.Lock()
		order = append(order, tag)
		mu.Unlock()
		return nil
	})

	w := NewWorker(q, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w.Start(ctx)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for all items to be processed")
		default:
		}
		items, err := q.ListByStatus("succeeded")
		if err != nil {
			t.Fatalf("ListByStatus: %v", err)
		}
		if len(items) == 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	w.Wait()

	mu.Lock()
	defer mu.Unlock()

	if len(order) != 3 {
		t.Fatalf("expected 3 items processed, got %d", len(order))
	}
	expected := []string{"v1", "v2", "v3"}
	for i, tag := range expected {
		if order[i] != tag {
			t.Errorf("order[%d] = %q, want %q", i, order[i], tag)
		}
	}

	items, _ := q.ListByStatus("succeeded")
	if len(items) != 3 {
		t.Errorf("expected 3 succeeded items, got %d", len(items))
	}
}

func TestWorkerContinuesAfterFailure(t *testing.T) {
	t.Parallel()
	q := newTestQueue(t, 10)

	if err := q.Enqueue("legacy", "v1"); err != nil {
		t.Fatalf("Enqueue(v1): %v", err)
	}
	if err := q.Enqueue("legacy", "v2"); err != nil {
		t.Fatalf("Enqueue(v2): %v", err)
	}

	runner := DeployRunner(func(tag string) error {
		if tag == "v1" {
			return fmt.Errorf("simulated failure for v1")
		}
		return nil
	})

	w := NewWorker(q, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w.Start(ctx)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for items to be processed")
		default:
		}
		failed, _ := q.ListByStatus("failed")
		succeeded, _ := q.ListByStatus("succeeded")
		if len(failed)+len(succeeded) == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	w.Wait()

	failed, _ := q.ListByStatus("failed")
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed item, got %d", len(failed))
	}
	if failed[0].Tag != "v1" {
		t.Errorf("failed item tag = %q, want %q", failed[0].Tag, "v1")
	}

	succeeded, _ := q.ListByStatus("succeeded")
	if len(succeeded) != 1 {
		t.Fatalf("expected 1 succeeded item, got %d", len(succeeded))
	}
	if succeeded[0].Tag != "v2" {
		t.Errorf("succeeded item tag = %q, want %q", succeeded[0].Tag, "v2")
	}
}

func TestWorkerStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	q := newTestQueue(t, 10)

	runner := DeployRunner(func(tag string) error {
		return nil
	})

	w := NewWorker(q, runner)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	cancel()

	done := make(chan struct{})
	go func() {
		w.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop within 2 seconds after context cancellation")
	}
}

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
