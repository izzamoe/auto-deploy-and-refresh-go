package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/protocol"
)

func TestQueueCancelPendingBeforeDequeue(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := enqueueJob(t, q, "app1", "v1")

	result, err := NewCancelService(q).RequestJobCancel(jobID)
	if err != nil {
		t.Fatalf("RequestJobCancel: %v", err)
	}
	if result.Outcome != CancelOutcomePendingCanceled {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, CancelOutcomePendingCanceled)
	}

	id, tag, err := q.DequeueNext("app1")
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}
	if id != "" || tag != "" {
		t.Fatalf("DequeueNext returned canceled job (%q, %q)", id, tag)
	}
	assertJobStatus(t, q, jobID, JobStatusCanceled)
}

func TestWorkerCancelDuringDownloadCleansTempAndMarksCanceled(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v-download")
	control := NewCancelService(q)
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "app")
	tmpPath := binaryPath + ".tmp"
	started := make(chan struct{})

	dlClient, _ := client.NewClient(client.WithResponseBodyStream(true))
	dlClient.Use(func(next client.Endpoint) client.Endpoint {
		return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
			resp.SetStatusCode(http.StatusOK)
			resp.Header.SetContentLength(1024)
			resp.SetBodyStream(&blockingBody{ctx: ctx, started: started}, 1024)
			return nil
		}
	})
	tracker := NewProgressTracker()
	app := &App{ID: "app1", BinaryPath: binaryPath, ServiceName: "app.service", GithubRepo: "owner/repo", ArtifactName: "artifact"}

	done := make(chan error, 1)
	go func() {
		_, err := deployWithControl(app, jobID, "v-download", tracker, dlClient, control)
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
	assertJobStatus(t, q, jobID, JobStatusCanceled)
	if _, err := os.Stat(tmpPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp artifact still exists or stat failed unexpectedly: %v", err)
	}
}

func TestWorkerCancelAfterBackupRestoresBackupAndMarksCanceled(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v-backup")
	control := NewCancelService(q)
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "app")
	if err := os.WriteFile(binaryPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}
	restoreGlobals := stubDeployGlobals(t)
	defer restoreGlobals()
	originalRename := renameFile
	renameFile = func(oldpath, newpath string) error {
		err := originalRename(oldpath, newpath)
		if err == nil && oldpath == binaryPath && newpath == binaryPath+".bak" {
			if _, cancelErr := control.RequestJobCancel(jobID); cancelErr != nil {
				return cancelErr
			}
		}
		return err
	}

	app := &App{ID: "app1", BinaryPath: binaryPath, ServiceName: "app.service", GithubRepo: "owner/repo", ArtifactName: "artifact"}
	_, err := deployWithControl(app, jobID, "v-backup", NewProgressTracker(), staticELFClient(), control)
	if !errors.Is(err, errDeployCanceled) {
		t.Fatalf("deploy error = %v, want errDeployCanceled", err)
	}
	assertJobStatus(t, q, jobID, JobStatusCanceled)
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
	control := NewCancelService(q)
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

	app := &App{ID: "app1", BinaryPath: binaryPath, ServiceName: "app.service", GithubRepo: "owner/repo", ArtifactName: "artifact"}
	_, err := deployWithControl(app, jobID, "v-health", NewProgressTracker(), staticELFClient(), control)
	if err == nil || errors.Is(err, errDeployCanceled) {
		t.Fatalf("deploy error = %v, want rollback failure", err)
	}
	assertJobStatus(t, q, jobID, JobStatusFailed)
}

func TestWorkerCancelTerminalAndRepeatedNoop(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v-terminal")
	if err := q.MarkDone(jobID, true, "", nil); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	svc := NewCancelService(q)
	for i := 0; i < 2; i++ {
		result, err := svc.RequestJobCancel(jobID)
		if err != nil {
			t.Fatalf("RequestJobCancel #%d: %v", i+1, err)
		}
		if result.Outcome != CancelOutcomeTerminalNoop || result.Status != JobStatusSucceeded {
			t.Fatalf("cancel #%d = %+v, want terminal noop", i+1, result)
		}
	}
	assertJobStatus(t, q, jobID, JobStatusSucceeded)
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

func staticELFClient() *client.Client {
	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	c.Use(func(next client.Endpoint) client.Endpoint {
		return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
			body := []byte{0x7f, 'E', 'L', 'F', 1, 2, 3, 4}
			resp.SetStatusCode(http.StatusOK)
			resp.Header.SetContentLength(len(body))
			resp.SetBodyStream(bytes.NewReader(body), len(body))
			return nil
		}
	})
	return c
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
