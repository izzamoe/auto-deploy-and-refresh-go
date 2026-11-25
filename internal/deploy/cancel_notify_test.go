package deploy

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// ctxBlockedBody never produces data until the request context ends.
type ctxBlockedBody struct{ r *http.Request }

func (b ctxBlockedBody) Read([]byte) (int, error) {
	<-b.r.Context().Done()
	return 0, b.r.Context().Err()
}
func (ctxBlockedBody) Close() error { return nil }

// FR-048: a cancel request reaches a running download by notification, well inside the
// 2 s fallback interval of the watcher.
func TestDeploy_CancelDuringDownloadIsNotifiedNotPolled(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v-cancel")
	control := cancel.NewCancelService(q)
	binaryPath := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(binaryPath, elfBinary("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	restore := stubDeployGlobals(t)
	defer restore()
	client := fakeBodyClient(func(r *http.Request) (io.ReadCloser, int64) { return ctxBlockedBody{r}, 1 << 20 })

	app := &store.App{ID: "app1", BinaryPath: binaryPath, ServiceName: "app.service", GithubRepo: "owner/repo", ArtifactName: "artifact"}
	type result struct {
		err error
		at  time.Time
	}
	done := make(chan result, 1)
	go func() {
		_, err := DeployWithControl(app, jobID, "v-cancel", progress.NewProgressTracker(), client, control)
		done <- result{err, time.Now()}
	}()
	time.Sleep(300 * time.Millisecond) // the download is in flight
	requestedAt := time.Now()
	if _, err := control.RequestJobCancel(jobID); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if !errors.Is(r.err, errDeployCanceled) {
			t.Fatalf("err = %v, want errDeployCanceled", r.err)
		}
		if d := r.at.Sub(requestedAt); d > 700*time.Millisecond {
			t.Errorf("the cancel took %v to stop the download, want well below the %v fallback", d, cancelPollFallback)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the download was not canceled")
	}
}
