package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/izzamoe/auto-deploy/internal/store"
)

// Self-deploy (Q-19, FR-144): an app whose service is auto-deploy itself. The process
// that performs the restart is the process that dies, so the job cannot be settled by
// it. Instead:
//
//  1. before the restart a pending-self-deploy file (job id, paths) is written and a
//     one-shot guard timer (systemd-run --on-active=120) is armed; the guard restores
//     <bin>.bak and restarts the service if the pending file is still there;
//  2. the restart is non-blocking and the deploy goroutine waits for the shutdown
//     signal; the job stays in_progress (store.ErrSelfDeployInFlight);
//  3. the new process finds the pending file at startup: it IS the new binary and it
//     started, so the job is settled as succeeded, the guard is stopped and the files
//     are removed. If the new binary never starts, the guard rolls back and leaves
//     "<pending>.rolledback" for the old binary to report.

const (
	selfGuardDelaySeconds = 120
	selfRestartWait       = 90 * time.Second
	pendingSelfDeployFile = "pending-self-deploy"
)

var (
	selfMu          sync.RWMutex
	selfServiceName string
	selfStateDir    string
)

// ConfigureSelfDeploy enables the self-deploy path for the app whose service name is
// selfService; stateDir (next to the database) holds the pending file. An empty
// argument disables it.
func ConfigureSelfDeploy(selfService, stateDir string) {
	selfMu.Lock()
	selfServiceName, selfStateDir = normalizeUnit(selfService), stateDir
	selfMu.Unlock()
}

func normalizeUnit(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return strings.TrimSuffix(name, ".service")
}

// IsSelfApp reports whether app is the auto-deploy service itself.
func IsSelfApp(app *store.App) bool {
	selfMu.RLock()
	defer selfMu.RUnlock()
	return selfServiceName != "" && selfStateDir != "" && normalizeUnit(app.ServiceName) == selfServiceName
}

func selfDir() string {
	selfMu.RLock()
	defer selfMu.RUnlock()
	return selfStateDir
}

// SelfDeployResolver settles the job of a self-deploy that was in flight when the
// process died.
type SelfDeployResolver interface {
	MarkSelfDeploySucceeded(jobID string) error
	MarkSelfDeployRolledBack(jobID, message string) error
}

// PendingSelfDeployPath is the self-deploy transaction file inside stateDir.
func PendingSelfDeployPath(stateDir string) string {
	return filepath.Join(stateDir, pendingSelfDeployFile)
}

// PendingSelfDeploy is the content of the pending-self-deploy file.
type PendingSelfDeploy struct {
	JobID       string `json:"jobId"`
	BinaryPath  string `json:"binaryPath"`
	BackupPath  string `json:"backupPath"`
	ServiceName string `json:"serviceName"`
}

var unsafeUnitChars = regexp.MustCompile(`[^A-Za-z0-9]+`)

func guardUnitName(jobID string) string {
	id := unsafeUnitChars.ReplaceAllString(jobID, "")
	if len(id) > 32 {
		id = id[:32]
	}
	return "auto-deploy-guard-" + id
}

// runSystemdRun creates the guard timer; a seam for tests.
var runSystemdRun = func(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "systemd-run", args...).CombinedOutput()
}

// The guard script gets every value as a positional argument (never interpolated).
const guardScript = `if [ -e "$1" ]; then mv -f -- "$2" "$3" && printf '%s\n' "$5" > "$1.rolledback"; rm -f -- "$1" "$3.deploying"; systemctl restart -- "$4"; fi`

// armSelfDeploy writes the pending file and arms the guard timer.
func armSelfDeploy(app *store.App, jobID, backupPath string) error {
	dir := selfDir()
	pending := PendingSelfDeployPath(dir)
	data, err := json.Marshal(PendingSelfDeploy{JobID: jobID, BinaryPath: app.BinaryPath, BackupPath: backupPath, ServiceName: app.ServiceName})
	if err != nil {
		return err
	}
	if err := os.WriteFile(pending, data, 0o600); err != nil {
		return fmt.Errorf("write pending-self-deploy: %w", err)
	}
	if err := syncFile(pending); err != nil {
		_ = os.Remove(pending)
		return err
	}
	_ = syncDir(dir)
	out, err := runSystemdRun(
		"--unit="+guardUnitName(jobID), fmt.Sprintf("--on-active=%d", selfGuardDelaySeconds), "--collect", "--quiet",
		"/bin/sh", "-c", guardScript, "sh", pending, backupPath, app.BinaryPath, app.ServiceName, jobID,
	)
	if err != nil {
		_ = os.Remove(pending)
		return fmt.Errorf("arm the rollback guard: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// disarmSelfDeploy removes the pending file and stops the guard timer.
func disarmSelfDeploy(jobID string) {
	_ = os.Remove(PendingSelfDeployPath(selfDir()))
	if out, err := runSystemctl("stop", guardUnitName(jobID)+".timer"); err != nil {
		slog.Debug("stopping the self-deploy guard", "err", err, "out", strings.TrimSpace(string(out)))
	}
}

// selfDeployRestart restarts auto-deploy's own service and waits to be terminated.
// It returns store.ErrSelfDeployInFlight when the shutdown signal arrived (the new
// process settles the job), another error when the restart did not happen.
func selfDeployRestart(ctx context.Context, app *store.App, jobID, backupPath string) error {
	if err := armSelfDeploy(app, jobID, backupPath); err != nil {
		return &selfGuardError{err: err}
	}
	if out, err := runSystemctl("restart", "--no-block", app.ServiceName); err != nil {
		disarmSelfDeploy(jobID)
		return fmt.Errorf("restart service: %w: %s", err, strings.TrimSpace(string(out)))
	}
	select {
	case <-ctx.Done():
		return store.ErrSelfDeployInFlight
	case <-time.After(selfRestartWait):
		disarmSelfDeploy(jobID)
		return errors.New("restart service: the service did not restart itself in time")
	}
}

// selfGuardError marks a failure BEFORE the restart: nothing was restarted, so the
// caller only has to put the previous binary back.
type selfGuardError struct{ err error }

func (e *selfGuardError) Error() string { return "self-deploy guard unavailable: " + e.err.Error() }
func (e *selfGuardError) Unwrap() error { return e.err }

// RecoverSelfDeploy runs at startup, before stale-job recovery. It reports whether a
// self-deploy was settled.
func RecoverSelfDeploy(stateDir string, r SelfDeployResolver) (bool, error) {
	pending := PendingSelfDeployPath(stateDir)

	if data, err := os.ReadFile(pending + ".rolledback"); err == nil {
		jobID := strings.TrimSpace(string(data))
		_ = os.Remove(pending)
		if err := r.MarkSelfDeployRolledBack(jobID, "self-deploy rolled back by the guard: the new binary did not start"); err != nil {
			return true, err
		}
		_ = os.Remove(pending + ".rolledback")
		return true, nil
	}

	data, err := os.ReadFile(pending)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var p PendingSelfDeploy
	if err := json.Unmarshal(data, &p); err != nil || p.JobID == "" {
		_ = os.Remove(pending)
		return false, fmt.Errorf("unreadable pending-self-deploy file removed: %v", err)
	}
	// This process is the new binary and it started: the deploy succeeded.
	if err := r.MarkSelfDeploySucceeded(p.JobID); err != nil {
		return true, err
	}
	if p.BinaryPath != "" {
		_ = os.Remove(deployingMarkerPath(p.BinaryPath))
	}
	disarmSelfDeploy2(stateDir, p.JobID)
	slog.Info("self-deploy confirmed by the new process", "job", p.JobID)
	return true, nil
}

func disarmSelfDeploy2(stateDir, jobID string) {
	_ = os.Remove(PendingSelfDeployPath(stateDir))
	if out, err := runSystemctl("stop", guardUnitName(jobID)+".timer"); err != nil {
		slog.Debug("stopping the self-deploy guard", "err", err, "out", strings.TrimSpace(string(out)))
	}
}
