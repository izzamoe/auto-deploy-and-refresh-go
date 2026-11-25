package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/store"
)

// FR-144: when the runner reports a self-deploy in flight, the coordinator must NOT
// finish the job: the restarted process settles it.
func TestCoordinator_SelfDeployInFlightLeavesJobInProgress(t *testing.T) {
	ran := make(chan struct{})
	c, appStore, q := newTestCoordinator(t, func(a *store.App, jobID, tag string) (store.DownloadSummary, error) {
		defer close(ran)
		return store.DownloadSummary{}, store.ErrSelfDeployInFlight
	})
	app, err := appStore.Create("self", "sec-self-deploy-xxxxxxxxxxxxxxxxxxxxxxxx", "/opt/self/self", "self.service", "o/r", "art")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(app.ID, "v9"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	select {
	case <-ran:
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not run")
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	c.Wait()

	jobs, err := q.ListByStatus("in_progress")
	if err != nil || len(jobs) != 1 {
		t.Fatalf("in_progress jobs = %v (%v), want the self-deploy job to stay in_progress", jobs, err)
	}
}
