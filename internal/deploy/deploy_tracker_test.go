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

// T043 (B10/FR-033): a deploy canceled at ANY checkpoint must leave the progress
// tracker entry in a terminal phase ("failed"; the job status is "canceled"),
// otherwise it is never cleaned up and is re-sent in every WebSocket snapshot.
func TestDeploy_AllExitPathsFinalizeTracker(t *testing.T) {
	for _, point := range []string{"download", "chmod", "backup", "install", "restart", "healthcheck"} {
		t.Run(point, func(t *testing.T) {
			q := newTestQueue(t, 10)
			jobID := dequeueJob(t, q, "app1", "v-"+point)
			control := cancel.NewCancelService(q)
			binaryPath := filepath.Join(t.TempDir(), "app")
			if err := os.WriteFile(binaryPath, []byte("old-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			restoreGlobals := stubDeployGlobals(t)
			defer restoreGlobals()

			requestCancel := func(p string) {
				if p == point {
					if _, err := control.RequestJobCancel(jobID); err != nil {
						t.Errorf("RequestJobCancel at %s: %v", p, err)
					}
				}
			}
			stubLink(t, func(oldpath, newpath string) error {
				err := os.Link(oldpath, newpath)
				if err == nil && oldpath == binaryPath && newpath == binaryPath+".bak" {
					requestCancel("backup")
				}
				return err
			})
			originalRename := renameFile
			renameFile = func(oldpath, newpath string) error {
				err := originalRename(oldpath, newpath)
				if err == nil && oldpath == binaryPath+".tmp" && newpath == binaryPath {
					requestCancel("install")
				}
				return err
			}
			chmodFile = func(path string, mode os.FileMode) error {
				err := os.Chmod(path, mode)
				requestCancel("chmod")
				return err
			}
			runSystemctl = func(name string, args ...string) ([]byte, error) {
				switch name {
				case "restart":
					requestCancel("restart")
					return []byte("restarted"), nil
				case "is-active":
					requestCancel("healthcheck")
					return []byte("inactive\n"), nil
				}
				return nil, nil
			}

			client := staticELFClient()
			if point == "download" {
				started := make(chan struct{})
				client = fakeBodyClient(func(req *http.Request) (io.ReadCloser, int64) {
					return &blockingBody{ctx: req.Context(), started: started}, 1024
				})
				go func() {
					<-started
					requestCancel("download")
				}()
			}

			tracker := progress.NewProgressTracker()
			app := &store.App{ID: "app1", BinaryPath: binaryPath, ServiceName: "app.service", GithubRepo: "owner/repo", ArtifactName: "artifact"}
			done := make(chan error, 1)
			go func() {
				_, err := DeployArtifact(app, jobID, "v-"+point, tracker, client, control, nil)
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, errDeployCanceled) {
					t.Fatalf("deploy error = %v, want errDeployCanceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("deploy did not finish")
			}

			assertJobStatus(t, q, jobID, cancel.JobStatusCanceled)
			snap, ok := tracker.Snapshot(app.ID)
			if !ok {
				t.Fatal("tracker entry missing")
			}
			if snap.Phase != progress.ProgressStageFailed {
				t.Errorf("tracker phase after a cancel at %q = %q, want %q", point, snap.Phase, progress.ProgressStageFailed)
			}
		})
	}
}
