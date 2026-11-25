package store

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// B24 / FR-049: the per-app pending quota is enforced atomically: N concurrent
// enqueues against maxPending=K leave at most K pending jobs and exactly K successes.

func countPending(t *testing.T, q *DeployQueue, appID string) int {
	t.Helper()
	var n int
	if err := q.DB().QueryRow(`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ? AND status = 'pending'`, appID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func hammerEnqueue(t *testing.T, q *DeployQueue, enqueue func(appID, tag string) error, workers int) (ok, full int32) {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch err := enqueue("app-quota", fmt.Sprintf("v%d", i)); {
			case err == nil:
				atomic.AddInt32(&ok, 1)
			case errors.Is(err, ErrQueueFull):
				atomic.AddInt32(&full, 1)
			default:
				t.Errorf("enqueue v%d: %v", i, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	return ok, full
}

func TestQuota_ConcurrentEnqueueNeverExceedsMaxPending(t *testing.T) {
	const maxPending, workers = 10, 50
	for _, tc := range []struct {
		name    string
		enqueue func(q *DeployQueue) func(string, string) error
	}{
		{"webhook", func(q *DeployQueue) func(string, string) error { return q.Enqueue }},
		{"manual", func(q *DeployQueue) func(string, string) error { return q.EnqueueManual }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Repeat: the race is a matter of interleaving.
			for round := 0; round < 5; round++ {
				q, err := NewDeployQueue(newTestDB(t), maxPending)
				if err != nil {
					t.Fatal(err)
				}
				if err := q.Migrate(); err != nil {
					t.Fatal(err)
				}
				ok, full := hammerEnqueue(t, q, tc.enqueue(q), workers)
				if got := countPending(t, q, "app-quota"); got > maxPending || ok > maxPending {
					t.Fatalf("B24 round %d: %d pending rows / %d successes with maxPending=%d (%d rejected)", round, got, ok, maxPending, full)
				}
				if ok != maxPending || full != workers-maxPending {
					t.Fatalf("round %d: %d accepted / %d rejected, want exactly %d / %d", round, ok, full, maxPending, workers-maxPending)
				}
			}
		})
	}
}

// The quota is per app: a full queue of one app does not block another.
func TestQuota_IsPerApp(t *testing.T) {
	q, err := NewDeployQueue(newTestDB(t), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"v1", "v2"} {
		if err := q.Enqueue("a", tag); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Enqueue("a", "v3"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third job of app a: %v, want ErrQueueFull", err)
	}
	if err := q.Enqueue("b", "v1"); err != nil {
		t.Fatalf("app b must have its own quota: %v", err)
	}
}
