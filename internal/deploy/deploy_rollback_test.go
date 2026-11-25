package deploy

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// T042 (B9/FR-032): when "systemctl restart" fails after the new binary was
// installed, the previous binary must be restored and restarted.

func TestDeploy_RestartFailureRollsBackAndRestartsOldBinary(t *testing.T) {
	app := testApp(t.TempDir())
	original := elfBinary("old-binary")
	if err := os.WriteFile(app.BinaryPath, original, 0755); err != nil {
		t.Fatal(err)
	}
	stubDeploySleep(t)
	restarts := 0
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		if name != "restart" {
			return []byte("active\n"), nil
		}
		restarts++
		if restarts == 1 {
			return []byte("Job for test.service failed"), errors.New("exit status 1")
		}
		return []byte("restarted"), nil
	})
	tracker := progress.NewProgressTracker()

	_, err := DeployArtifact(app, "job-b9", "v2", tracker, deployClientWithBody(elfBinary("new-binary")), nil, nil)
	if err == nil {
		t.Fatal("expected the deploy to fail")
	}
	if restarts != 2 {
		t.Errorf("systemctl restart calls = %d, want 2 (failed deploy + rollback)", restarts)
	}
	assertFileBytes(t, app.BinaryPath, original)
	assertPathMissing(t, app.BinaryPath+".bak")
	assertPathMissing(t, app.BinaryPath+".tmp")
	for _, want := range []string{"restart service", "Job for test.service failed", "rolled back"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must contain %q", err.Error(), want)
		}
	}
	if !errors.Is(err, store.ErrRolledBack) {
		t.Errorf("error must wrap store.ErrRolledBack so the job is flagged rolled_back=1: %v", err)
	}
	if snap, ok := tracker.Snapshot(app.ID); !ok || snap.Phase != progress.ProgressStageFailed {
		t.Errorf("tracker = %+v ok=%v, want terminal phase %q", snap, ok, progress.ProgressStageFailed)
	}
}

func TestDeploy_RestartFailureRollbackRestartAlsoFails(t *testing.T) {
	app := testApp(t.TempDir())
	original := elfBinary("old-binary")
	if err := os.WriteFile(app.BinaryPath, original, 0755); err != nil {
		t.Fatal(err)
	}
	stubDeploySleep(t)
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		if name == "restart" {
			return []byte("boom"), errors.New("exit status 1")
		}
		return []byte("failed\n"), nil
	})

	_, err := DeployArtifact(app, "job-b9b", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil)
	if err == nil {
		t.Fatal("expected the deploy to fail")
	}
	// The old binary is back on disk, but the service is not running: the error
	// must say so and must NOT claim a successful rollback.
	assertFileBytes(t, app.BinaryPath, original)
	assertPathMissing(t, app.BinaryPath+".bak")
	if !strings.Contains(err.Error(), "rollback restart failed") {
		t.Errorf("error %q must report the failed rollback restart", err.Error())
	}
	if errors.Is(err, store.ErrRolledBack) {
		t.Errorf("a rollback whose restart failed must not be flagged rolled back: %v", err)
	}
}

func TestDeploy_RestartFailureFirstDeployHasNoBackupToRestore(t *testing.T) {
	app := testApp(t.TempDir()) // no previous binary
	stubDeploySleep(t)
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		if name == "restart" {
			return []byte("boom"), errors.New("exit status 1")
		}
		return []byte("failed\n"), nil
	})

	_, err := DeployArtifact(app, "job-b9c", "v1", progress.NewProgressTracker(), deployClientWithBody(elfBinary("first")), nil, nil)
	if err == nil {
		t.Fatal("expected the deploy to fail")
	}
	if !strings.Contains(err.Error(), "rollback unavailable: no previous binary backup") {
		t.Errorf("error %q must say there is nothing to roll back to", err.Error())
	}
	if strings.Contains(err.Error(), "rolled back") || errors.Is(err, store.ErrRolledBack) {
		t.Errorf("must not claim a rollback without a backup: %v", err)
	}
}

func TestDeploy_RestartFailureRollbackRenameFails(t *testing.T) {
	app := testApp(t.TempDir())
	if err := os.WriteFile(app.BinaryPath, elfBinary("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	stubDeploySleep(t)
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		if name == "restart" {
			return []byte("boom"), errors.New("exit status 1")
		}
		return []byte("failed\n"), nil
	})
	stubRename(t, func(oldpath, newpath string) error {
		if oldpath == app.BinaryPath+".bak" {
			return fmt.Errorf("disk on fire")
		}
		return os.Rename(oldpath, newpath)
	})

	_, err := DeployArtifact(app, "job-b9d", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "rollback rename failed") {
		t.Fatalf("error = %v, want rollback rename failure reported", err)
	}
	if errors.Is(err, store.ErrRolledBack) {
		t.Errorf("failed rename must not be flagged rolled back")
	}
}
