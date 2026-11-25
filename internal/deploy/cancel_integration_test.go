package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestQueueCancelPendingBeforeDequeue(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := enqueueJob(t, q, "app1", "v1")

	result, err := cancel.NewCancelService(q).RequestJobCancel(jobID)
	if err != nil {
		t.Fatalf("RequestJobCancel: %v", err)
	}
	if result.Outcome != cancel.CancelOutcomePendingCanceled {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, cancel.CancelOutcomePendingCanceled)
	}

	id, tag, err := q.DequeueNext("app1")
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	if id != "" || tag != "" {
		t.Fatalf("DequeueNext returned canceled job (%q, %q)", id, tag)
	}
	assertJobStatus(t, q, jobID, cancel.JobStatusCanceled)
}

func TestWorkerCancelDuringDownloadCleansTempAndMarksCanceled(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v-download")
	control := cancel.NewCancelService(q)
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "app")
	tmpPath := binaryPath + ".tmp"
	started := make(chan struct{})

	dlClient := fakeBodyClient(func(req *http.Request) (io.ReadCloser, int64) {
		return &blockingBody{ctx: req.Context(), started: started}, 1024
	})
	tracker := progress.NewProgressTracker()
	app := &store.App{ID: "app1", BinaryPath: binaryPath, ServiceName: "app.service", GithubRepo: "owner/repo", ArtifactName: "artifact"}

	done := make(chan error, 1)
	go func() {
		_, err := DeployArtifact(app, jobID, "v-download", tracker, dlClient, control, nil)
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("download never started")
	}
	if _, err := control.RequestJobCancel(jobID); err != nil {
		t.Fatalf("RequestJobCancel: %v", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, errDeployCanceled) {
			t.Fatalf("deploy error = %v, want errDeployCanceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deploy did not stop after cancel")
	}
	assertJobStatus(t, q, jobID, cancel.JobStatusCanceled)
	if _, err := os.Stat(tmpPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp artifact still exists or stat failed unexpectedly: %v", err)
	}
}

func TestWorkerCancelAfterBackupRestoresBackupAndMarksCanceled(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v-backup")
	control := cancel.NewCancelService(q)
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "app")
	if err := os.WriteFile(binaryPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}
	restoreGlobals := stubDeployGlobals(t)
	defer restoreGlobals()
	// FR-043: the backup is a hard link now; cancel right after it is made.
	stubLink(t, func(oldpath, newpath string) error {
		err := os.Link(oldpath, newpath)
		if err == nil && oldpath == binaryPath && newpath == binaryPath+".bak" {
			if _, cancelErr := control.RequestJobCancel(jobID); cancelErr != nil {
				return cancelErr
			}
		}
		return err
	})

	app := &store.App{ID: "app1", BinaryPath: binaryPath, ServiceName: "app.service", GithubRepo: "owner/repo", ArtifactName: "artifact"}
	_, err := DeployArtifact(app, jobID, "v-backup", progress.NewProgressTracker(), staticELFClient(), control, nil)
	if !errors.Is(err, errDeployCanceled) {
		t.Fatalf("deploy error = %v, want errDeployCanceled", err)
	}
	assertJobStatus(t, q, jobID, cancel.JobStatusCanceled)
	contents, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read restored binary: %v", err)
	}
	if string(contents) != "old-binary" {
		t.Fatalf("binary = %q, want restored old binary", string(contents))
	}
}

func TestWorkerCancelDuringHealthcheckRollbackFailureMarksFailed(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v-health")
	control := cancel.NewCancelService(q)
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "app")
	if err := os.WriteFile(binaryPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}
	restoreGlobals := stubDeployGlobals(t)
	defer restoreGlobals()
	originalRename := renameFile
	renameFile = func(oldpath, newpath string) error {
		if oldpath == binaryPath+".bak" && newpath == binaryPath {
			return fmt.Errorf("rollback blocked")
		}
		err := originalRename(oldpath, newpath)
		if err == nil && newpath == binaryPath && oldpath == binaryPath+".tmp" {
			if _, cancelErr := control.RequestJobCancel(jobID); cancelErr != nil {
				return cancelErr
			}
		}
		return err
	}

	app := &store.App{ID: "app1", BinaryPath: binaryPath, ServiceName: "app.service", GithubRepo: "owner/repo", ArtifactName: "artifact"}
	_, err := DeployArtifact(app, jobID, "v-health", progress.NewProgressTracker(), staticELFClient(), control, nil)
	if err == nil || errors.Is(err, errDeployCanceled) {
		t.Fatalf("deploy error = %v, want rollback failure", err)
	}
	assertJobStatus(t, q, jobID, cancel.JobStatusFailed)
}

func TestWorkerCancelTerminalAndRepeatedNoop(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v-terminal")
	if err := q.MarkDone(jobID, true, "", nil); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	svc := cancel.NewCancelService(q)
	for i := range 2 {
		result, err := svc.RequestJobCancel(jobID)
		if err != nil {
			t.Fatalf("RequestJobCancel #%d: %v", i+1, err)
		}
		if result.Outcome != cancel.CancelOutcomeTerminalNoop || result.Status != cancel.JobStatusSucceeded {
			t.Fatalf("cancel #%d = %+v, want terminal noop", i+1, result)
		}
	}
	assertJobStatus(t, q, jobID, cancel.JobStatusSucceeded)
}

func dequeueJob(t *testing.T, q *store.DeployQueue, appID, tag string) string {
	t.Helper()
	if err := q.Enqueue(appID, tag); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	id, gotTag, err := q.DequeueNext(appID)
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	if id == "" || gotTag != tag {
		t.Fatalf("DequeueNext = (%q, %q), want tag %q", id, gotTag, tag)
	}
	return id
}

func enqueueJob(t *testing.T, q *store.DeployQueue, appID, tag string) string {
	t.Helper()
	if err := q.Enqueue(appID, tag); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	var id string
	if err := q.DB().QueryRow(`SELECT id FROM deploy_jobs WHERE app_id = ? AND tag = ?`, appID, tag).Scan(&id); err != nil {
		t.Fatalf("query job id: %v", err)
	}
	return id
}

func newTestQueue(t *testing.T, maxPending int) *store.DeployQueue {
	t.Helper()
	db := newTestDB(t)
	q, err := store.NewDeployQueue(db, maxPending)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return q
}

func assertJobStatus(t *testing.T, q *store.DeployQueue, jobID, want string) {
	t.Helper()
	var status string
	if err := q.DB().QueryRow(`SELECT status FROM deploy_jobs WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != want {
		t.Fatalf("job %s status = %q, want %q", jobID, status, want)
	}
}

type blockingBody struct {
	ctx       context.Context
	started   chan struct{}
	sentStart bool
}

func (b *blockingBody) Read(p []byte) (int, error) {
	if !b.sentStart {
		b.sentStart = true
		close(b.started)
		copy(p, []byte{0x7f, 'E', 'L', 'F'})
		return 4, nil
	}
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-time.After(50 * time.Millisecond):
		p[0] = 0
		return 1, nil
	}
}

func (b *blockingBody) Close() error { return nil }

func staticELFClient() *http.Client {
	body := elfBinary("\x01\x02\x03\x04")
	return fakeBodyClient(func(*http.Request) (io.ReadCloser, int64) {
		return io.NopCloser(bytes.NewReader(body)), int64(len(body))
	})
}

func stubDeployGlobals(t *testing.T) func() {
	t.Helper()
	originalRunSystemctl := runSystemctl
	originalRename := renameFile
	originalChmod := chmodFile
	originalSleep := deployHealthCheckSleep
	runSystemctl = func(name string, args ...string) ([]byte, error) {
		if name == "is-active" {
			return []byte("active\n"), nil
		}
		return nil, nil
	}
	renameFile = os.Rename
	chmodFile = os.Chmod
	deployHealthCheckSleep = func(time.Duration) {}
	return func() {
		runSystemctl = originalRunSystemctl
		renameFile = originalRename
		chmodFile = originalChmod
		deployHealthCheckSleep = originalSleep
	}
}
