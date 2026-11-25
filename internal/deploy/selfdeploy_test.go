package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// Q-19 / FR-144: a deploy of auto-deploy's own service arms a guard and a pending file
// BEFORE the (non-blocking) restart, then waits for the shutdown signal and leaves the
// job to the next process.

func configureSelf(t *testing.T, service string) string {
	t.Helper()
	state := t.TempDir()
	ConfigureSelfDeploy(service, state)
	t.Cleanup(func() { ConfigureSelfDeploy("", "") })
	return state
}

func TestSelfDeploy_ArmsGuardAndPendingBeforeRestartThenWaits(t *testing.T) {
	state := configureSelf(t, "auto-deploy.service")
	app := testApp(t.TempDir())
	app.ServiceName = "auto-deploy.service"
	if err := os.WriteFile(app.BinaryPath, elfBinary("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubDeploySleep(t)

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()
	var mu sync.Mutex
	var order []string
	var guardArgs []string
	origRun := runSystemdRun
	runSystemdRun = func(args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, "guard")
		guardArgs = args
		if _, err := os.Stat(PendingSelfDeployPath(state)); err != nil {
			t.Errorf("the pending file must exist before the guard is armed: %v", err)
		}
		return nil, nil
	}
	t.Cleanup(func() { runSystemdRun = origRun })
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		mu.Lock()
		order = append(order, name+" "+strings.Join(args, " "))
		mu.Unlock()
		if name == "restart" {
			// The service manager kills us: the shutdown context is canceled.
			cancelCtx()
		}
		return []byte("active\n"), nil
	})

	_, err := DeployArtifactContext(ctx, app, "job-self-1", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil)
	if !errors.Is(err, store.ErrSelfDeployInFlight) {
		t.Fatalf("err = %v, want ErrSelfDeployInFlight (the next process settles the job)", err)
	}
	if len(order) < 2 || order[0] != "guard" || order[1] != "restart --no-block -- auto-deploy.service" {
		t.Errorf("order = %v, want guard first, then a NON-blocking restart", order)
	}
	joined := strings.Join(guardArgs, " ")
	if !strings.Contains(joined, "--on-active=120") || !strings.Contains(joined, "job-self-1") {
		t.Errorf("guard args = %v", guardArgs)
	}
	data, rerr := os.ReadFile(PendingSelfDeployPath(state))
	if rerr != nil {
		t.Fatalf("pending file missing after the restart request: %v", rerr)
	}
	var p PendingSelfDeploy
	_ = json.Unmarshal(data, &p)
	if p.JobID != "job-self-1" || p.BinaryPath != app.BinaryPath || p.BackupPath != app.BinaryPath+".bak" {
		t.Errorf("pending = %+v", p)
	}
	if _, serr := os.Stat(deployingMarkerPath(app.BinaryPath)); serr != nil {
		t.Errorf("the .deploying marker must stay for the next process: %v", serr)
	}
	assertFileBytes(t, app.BinaryPath, elfBinary("new-binary"))
}

// Without a guard there is no safety net: the previous binary is restored and nothing
// is restarted.
func TestSelfDeploy_GuardFailureRestoresPreviousBinaryWithoutRestart(t *testing.T) {
	state := configureSelf(t, "auto-deploy.service")
	app := testApp(t.TempDir())
	app.ServiceName = "auto-deploy.service"
	original := elfBinary("old-binary")
	if err := os.WriteFile(app.BinaryPath, original, 0o755); err != nil {
		t.Fatal(err)
	}
	stubDeploySleep(t)
	origRun := runSystemdRun
	runSystemdRun = func(...string) ([]byte, error) { return []byte("no systemd"), errors.New("exit status 1") }
	t.Cleanup(func() { runSystemdRun = origRun })
	restarts := 0
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		if name == "restart" {
			restarts++
		}
		return []byte("active\n"), nil
	})

	_, err := DeployArtifactContext(context.Background(), app, "job-self-2", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "guard") {
		t.Fatalf("err = %v, want a guard failure", err)
	}
	if restarts != 0 {
		t.Errorf("restarted %d times without a guard", restarts)
	}
	assertFileBytes(t, app.BinaryPath, original)
	assertPathMissing(t, PendingSelfDeployPath(state))
	assertPathMissing(t, deployingMarkerPath(app.BinaryPath))
}

// Only the self app takes the self path.
func TestSelfDeploy_OtherAppsAreUnaffected(t *testing.T) {
	configureSelf(t, "auto-deploy.service")
	app := testApp(t.TempDir())
	if IsSelfApp(app) {
		t.Fatal("an unrelated service must not be a self app")
	}
	self := *app
	self.ServiceName = "auto-deploy"
	if !IsSelfApp(&self) {
		t.Error("a service name without the .service suffix must still match")
	}
}
