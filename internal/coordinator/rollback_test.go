package coordinator

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/store"
)

// T042 (B9/FR-032): a deploy that failed and was rolled back is recorded with
// deploy_jobs.rolled_back = 1; any other failure keeps rolled_back = 0.
func TestCoordinator_RolledBackFlagFollowsDeployError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"rolled back", fmt.Errorf("restart service: boom; %w", store.ErrRolledBack), 1},
		{"plain failure", errors.New("restart service: boom"), 0},
		{"success", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := func(app *store.App, jobID, tag string) (store.DownloadSummary, error) {
				return store.DownloadSummary{}, tc.err
			}
			c, apps, q := newTestCoordinator(t, runner)
			// adds deploy_jobs.rolled_back, exactly as cmd/server does at start (after the stores)
			if _, err := store.NewAdminUserStore(q.DB()); err != nil {
				t.Fatal(err)
			}
			if err := store.Migrate(q.DB()); err != nil {
				t.Fatal(err)
			}
			app, err := apps.Create("app", "secret1", "/bin/app", "app.service", "org/repo", "artifact")
			if err != nil {
				t.Fatal(err)
			}
			if err := q.Enqueue(app.ID, "v1"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c.Start(ctx)

			status := "succeeded"
			if tc.err != nil {
				status = "failed"
			}
			deadline := time.After(5 * time.Second)
			for {
				items, err := q.ListByStatus(status)
				if err != nil {
					t.Fatal(err)
				}
				if len(items) == 1 {
					break
				}
				select {
				case <-deadline:
					t.Fatalf("job never reached status %q", status)
				case <-time.After(20 * time.Millisecond):
				}
			}
			cancel()
			c.Wait()

			var got int
			if err := q.DB().QueryRow(`SELECT rolled_back FROM deploy_jobs WHERE app_id = ?`, app.ID).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("rolled_back = %d, want %d", got, tc.want)
			}
		})
	}
}
