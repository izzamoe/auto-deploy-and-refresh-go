package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/download"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

var (
	systemctlTimeout = 30 * time.Second
	runSystemctl     = func(name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
		defer cancel()
		cmdArgs := append([]string{name}, args...)
		cmd := exec.CommandContext(ctx, "systemctl", cmdArgs...)
		return cmd.CombinedOutput()
	}
	renameFile             = os.Rename
	chmodFile              = os.Chmod
	deployHealthCheckSleep = time.Sleep
	runJournalctl          = func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "journalctl", args...)
		return cmd.CombinedOutput()
	}
)

// defaultJournalctlLines is how many recent journal lines CaptureServiceLogs
// fetches when capturing a deploy's logs.
const defaultJournalctlLines = 200

// CaptureServiceLogs returns the recent systemd journal for serviceName (the
// last defaultJournalctlLines lines), used to diagnose why a deploy's health
// check failed. It never returns an error: on a non-zero journalctl exit the
// combined output (which usually explains the problem) is returned as-is, so
// the caller always has something to show or store.
func CaptureServiceLogs(serviceName string) string {
	return CaptureServiceLogsLines(serviceName, defaultJournalctlLines)
}

// CaptureServiceLogsLines is CaptureServiceLogs with a caller-chosen line
// count: lines > 0 fetches the last lines entries, lines <= 0 fetches the full
// available journal (no -n limit). Used by the live log viewers so operators
// can widen the window. It never returns an error, matching CaptureServiceLogs.
func CaptureServiceLogsLines(serviceName string, lines int) string {
	if strings.TrimSpace(serviceName) == "" {
		return ""
	}
	args := []string{"-u", serviceName, "--no-pager"}
	if lines > 0 {
		args = append(args, "-n", strconv.Itoa(lines))
	} else {
		args = append(args, "--no-tail")
	}
	out, err := runJournalctl(args...)
	if err != nil {
		if len(out) > 0 {
			return string(out)
		}
		return fmt.Sprintf("failed to read logs for %s: %v", serviceName, err)
	}
	return string(out)
}

// CaptureServiceLogsSince returns serviceName's systemd journal limited to
// entries emitted at or after since. Unlike CaptureServiceLogs (which returns
// an arbitrary tail of recent output), this scopes the result to a single
// deploy's execution window — the logs printed between when the deploy started
// and when it finished (its restart plus health-check window) — so a job's
// stored log shows exactly what the freshly-restarted service emitted before it
// became healthy or died, rather than unrelated output from a long-running
// process. The since bound is passed to journalctl as "@<unix-seconds>", which
// systemd interprets as an absolute UTC timestamp (no timezone ambiguity). It
// never returns an error, matching CaptureServiceLogs.
func CaptureServiceLogsSince(serviceName string, since time.Time) string {
	if strings.TrimSpace(serviceName) == "" {
		return ""
	}
	args := []string{"-u", serviceName, "--no-pager", "--since", fmt.Sprintf("@%d", since.Unix())}
	out, err := runJournalctl(args...)
	if err != nil {
		if len(out) > 0 {
			return string(out)
		}
		return fmt.Sprintf("failed to read logs for %s: %v", serviceName, err)
	}
	return string(out)
}

// SetRunJournalctlForTest overrides the journalctl-invoking function and
// returns a restore func, so other packages' tests can stub log capture
// without invoking the real journalctl binary.
func SetRunJournalctlForTest(fn func(args ...string) ([]byte, error)) (restore func()) {
	original := runJournalctl
	runJournalctl = fn
	return func() { runJournalctl = original }
}

const maxArtifactBytes int64 = 100 * 1024 * 1024

var errDeployCanceled = errors.New("deploy canceled at safe checkpoint")

// ArtifactResolver resolves where and how to download an app's release
// artifact. It is implemented by *github.Client: with a token configured it
// returns an authenticated GitHub API asset URL plus auth headers (which works
// for private repositories), otherwise the public browser download URL and no
// headers. A nil resolver falls back to the public URL, keeping the
// unauthenticated public-repo path working with no extra dependencies.
type ArtifactResolver interface {
	ArtifactDownload(ctx context.Context, repo, tag, artifact string) (url string, headers map[string]string, err error)
}

// resolveArtifactDownload asks resolver for the artifact URL and headers,
// falling back to the public download URL when no resolver is supplied.
func resolveArtifactDownload(ctx context.Context, resolver ArtifactResolver, repo, tag, artifact string) (string, map[string]string, error) {
	if resolver == nil {
		return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, artifact), nil, nil
	}
	return resolver.ArtifactDownload(ctx, repo, tag, artifact)
}

func downloadBinary(url, tmpPath string, tracker *progress.ProgressTracker, appID string, client *http.Client) (store.DownloadSummary, error) {
	return downloadBinaryContext(context.Background(), url, tmpPath, tracker, appID, client, nil, maxArtifactBytes)
}

func downloadBinaryContext(ctx context.Context, url, tmpPath string, tracker *progress.ProgressTracker, appID string, client *http.Client, headers map[string]string, maxBytes int64) (store.DownloadSummary, error) {
	slog.Info("downloading", "url", url)
	resp, err := download.DownloadWithRetryContext(ctx, client, url, headers, nil)
	if err != nil {
		return store.DownloadSummary{}, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if maxBytes < 0 {
		return store.DownloadSummary{}, fmt.Errorf("download too large: invalid max bytes %d", maxBytes)
	}
	if resp.ContentLength > maxBytes {
		return store.DownloadSummary{}, fmt.Errorf("download too large: content length %d exceeds max %d", resp.ContentLength, maxBytes)
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		return store.DownloadSummary{}, fmt.Errorf("create tmp file: %w", err)
	}

	reader := progress.NewCountingReader(resp.Body, resp.ContentLength, func(downloaded, total int64, speedBPS float64) {
		tracker.Update(appID, downloaded, total, speedBPS)
	})

	downloadStart := time.Now()
	probeLimit := max(maxBytes+1, maxBytes)
	written, err := io.Copy(f, io.LimitReader(reader, probeLimit))
	if err != nil {
		cleanupRejectedDownload(f, tmpPath)
		if ctx.Err() != nil {
			return store.DownloadSummary{}, ctx.Err()
		}
		if resp.ContentLength >= 0 && written != resp.ContentLength {
			return store.DownloadSummary{}, fmt.Errorf("download incomplete: wrote %d bytes, expected %d: %w", written, resp.ContentLength, err)
		}
		return store.DownloadSummary{}, fmt.Errorf("write tmp file: %w", err)
	}
	if ctx.Err() != nil {
		cleanupRejectedDownload(f, tmpPath)
		return store.DownloadSummary{}, ctx.Err()
	}
	if written > maxBytes {
		cleanupRejectedDownload(f, tmpPath)
		return store.DownloadSummary{}, fmt.Errorf("download too large: wrote %d bytes, max %d", written, maxBytes)
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		cleanupRejectedDownload(f, tmpPath)
		return store.DownloadSummary{}, fmt.Errorf("download incomplete: wrote %d bytes, expected %d", written, resp.ContentLength)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return store.DownloadSummary{}, fmt.Errorf("write tmp file: %w", err)
	}

	duration := time.Since(downloadStart)
	var avgSpeed float64
	if duration > 0 {
		avgSpeed = float64(written) / duration.Seconds()
	}

	return store.DownloadSummary{
		Bytes:      written,
		DurationMs: duration.Milliseconds(),
		SpeedBPS:   avgSpeed,
	}, nil
}

func cleanupRejectedDownload(f *os.File, tmpPath string) {
	f.Close()
	os.Remove(tmpPath)
}

func validateDownloadedArtifact(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("artifact validation: stat artifact: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("artifact validation: empty artifact")
	}
	if info.Size() < 4 {
		return fmt.Errorf("artifact validation: too small: %d bytes", info.Size())
	}

	ef, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("artifact validation: open artifact: %w", err)
	}
	defer ef.Close()

	magic := make([]byte, 4)
	if _, err := io.ReadFull(ef, magic); err != nil {
		return fmt.Errorf("artifact validation: read magic bytes: %w", err)
	}
	if magic[0] != 0x7f || magic[1] != 'E' || magic[2] != 'L' || magic[3] != 'F' {
		return fmt.Errorf("artifact validation: invalid binary: not an ELF executable")
	}

	return nil
}

// DeployWithControl deploys app's release from the public GitHub download URL
// (no authentication). Kept for callers that do not need private-repo support;
// it delegates to DeployArtifact with a nil resolver.
func DeployWithControl(app *store.App, jobID, tag string, tracker *progress.ProgressTracker, client *http.Client, control *cancel.CancelService) (store.DownloadSummary, error) {
	return DeployArtifact(app, jobID, tag, tracker, client, control, nil)
}

// DeployArtifact is DeployWithControl with an ArtifactResolver, which lets the
// download authenticate against the GitHub API (required for assets in private
// repositories). A nil resolver reproduces DeployWithControl's public-URL path.
func DeployArtifact(app *store.App, jobID, tag string, tracker *progress.ProgressTracker, client *http.Client, control *cancel.CancelService, resolver ArtifactResolver) (store.DownloadSummary, error) {
	return DeployArtifactContext(context.Background(), app, jobID, tag, tracker, client, control, resolver)
}

// DeployArtifactContext is DeployArtifact with a parent context: when it is canceled
// (process shutdown, FR-044) the download stops, and a self-deploy that is waiting for
// its own restart is released.
func DeployArtifactContext(parent context.Context, app *store.App, jobID, tag string, tracker *progress.ProgressTracker, client *http.Client, control *cancel.CancelService, resolver ArtifactResolver) (store.DownloadSummary, error) {
	tracker.Start(app.ID, jobID, tag)
	// B10: whatever path returns (cancel checkpoints included), the tracker entry
	// ends terminal. A deploy canceled mid-way is "failed" for the tracker (its
	// phases have no "canceled"); the job status itself is "canceled".
	defer tracker.FailIfActive(app.ID, jobID)

	// FR-040 defense in depth: an app row written before validation existed (or by a
	// path that skipped it) must not reach the filesystem or systemctl with an unsafe
	// binary path, service name, repo or artifact name.
	if errs := ValidateAppInput(AppInput{BinaryPath: app.BinaryPath, ServiceName: app.ServiceName, GithubRepo: app.GithubRepo, ArtifactName: app.ArtifactName, RunAsUser: app.RunAsUser, HealthURL: app.HealthURL}); len(errs) > 0 {
		return store.DownloadSummary{}, fmt.Errorf("edit app: %s is invalid (%s); this stored value predates input validation and must be corrected before the app can be deployed", errs[0].Field, errs[0].Message)
	}

	tmpPath := app.BinaryPath + ".tmp"

	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	// Ensure the target directory exists before writing the temp file. On a
	// first-ever deploy the binary's parent directory may not exist yet, which
	// otherwise fails as "create tmp file: ... no such file or directory".
	if dir := filepath.Dir(app.BinaryPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			tracker.Fail(app.ID)
			return store.DownloadSummary{}, fmt.Errorf("create binary directory %s: %w", dir, err)
		}
	}

	downloadCtx, stopDownloadCancelWatcher := deployDownloadContext(parent, jobID, control)
	url, headers, err := resolveArtifactDownload(downloadCtx, resolver, app.GithubRepo, tag, app.ArtifactName)
	if err != nil {
		stopDownloadCancelWatcher()
		tracker.Fail(app.ID)
		return store.DownloadSummary{}, fmt.Errorf("resolve artifact download: %w", err)
	}
	summary, err := downloadBinaryContext(downloadCtx, url, tmpPath, tracker, app.ID, client, headers, maxArtifactBytes)
	stopDownloadCancelWatcher()
	if err != nil {
		if errors.Is(err, context.Canceled) && control != nil {
			if cancelErr := finishCanceledCleanup(control, jobID, tmpPath); cancelErr != nil {
				tracker.Fail(app.ID)
				return store.DownloadSummary{}, cancelErr
			}
			return store.DownloadSummary{}, errDeployCanceled
		}
		tracker.Fail(app.ID)
		return store.DownloadSummary{}, err
	}
	if cancelErr := checkpointCleanupTemp(control, jobID, cancel.DeployPhaseDownloaded, tmpPath); cancelErr != nil {
		if errors.Is(cancelErr, errDeployCanceled) {
			return summary, cancelErr
		}
		tracker.Fail(app.ID)
		return summary, cancelErr
	}
	tracker.SetPhase(app.ID, progress.ProgressStageValidating)
	if cancelErr := checkpointCleanupTemp(control, jobID, cancel.DeployPhaseValidating, tmpPath); cancelErr != nil {
		if errors.Is(cancelErr, errDeployCanceled) {
			return summary, cancelErr
		}
		tracker.Fail(app.ID)
		return summary, cancelErr
	}

	if err := validateDownloadedArtifact(tmpPath); err != nil {
		tracker.Fail(app.ID)
		return summary, err
	}
	// FR-045: ELF type + architecture, then the release digest (Q-14).
	sum, verified, verifyErr := verifyArtifact(downloadCtx, app, resolver, tag, tmpPath)
	summary.SHA256, summary.Verified = sum, verified
	if verifyErr != nil {
		tracker.Fail(app.ID)
		return summary, verifyErr
	}

	if err := chmodFile(tmpPath, 0755); err != nil {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("chmod: %w", err)
	}

	backupPath := app.BinaryPath + ".bak"
	backupCreated := false
	tracker.SetPhase(app.ID, progress.ProgressStageBackingUp)
	if cancelErr := checkpointCleanupTemp(control, jobID, cancel.DeployPhaseBackingUp, tmpPath); cancelErr != nil {
		if errors.Is(cancelErr, errDeployCanceled) {
			return summary, cancelErr
		}
		tracker.Fail(app.ID)
		return summary, cancelErr
	}

	// FR-043/FR-044: the transaction marker is written before anything is touched and
	// removed once the binary is consistent again (deploy succeeded, rollback restored
	// the old binary, or nothing was changed). A crash in between leaves it for the
	// startup recovery.
	_, statErr := os.Stat(app.BinaryPath)
	hadBinary := statErr == nil
	markerPath := deployingMarkerPath(app.BinaryPath)
	if err := writeDeployingMarker(app.BinaryPath, jobID, tag, hadBinary); err != nil {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("write deploy marker: %w", err)
	}
	leaveMarker := false
	defer func() {
		if !leaveMarker {
			_ = os.Remove(markerPath)
		}
	}()

	if hadBinary {
		slog.Info("backup", "from", app.BinaryPath, "to", backupPath)
		if err := makeBackup(app.BinaryPath, backupPath); err != nil {
			tracker.Fail(app.ID)
			return summary, fmt.Errorf("backup: %w", err)
		}
		backupCreated = true
	}
	if cancelErr := checkpointRestoreBackup(control, jobID, tmpPath, backupPath, app.BinaryPath, backupCreated); cancelErr != nil {
		if errors.Is(cancelErr, errDeployCanceled) {
			return summary, cancelErr
		}
		tracker.Fail(app.ID)
		return summary, cancelErr
	}

	slog.Info("replacing binary")
	tracker.SetPhase(app.ID, progress.ProgressStageInstalling)
	cleanup = false
	leaveMarker = true // from here on only a restored binary or a success closes the transaction
	if err := installBinary(tmpPath, app.BinaryPath); err != nil {
		// rename is atomic: the old binary is still in place. Drop the backup of this
		// attempt and the temp file; nothing was changed.
		cleanup = true
		leaveMarker = false
		tracker.Fail(app.ID)
		if backupCreated {
			_ = os.Remove(backupPath)
		}
		return summary, fmt.Errorf("replace binary: %w", err)
	}
	if cancelErr := checkpointRollbackInstalled(control, jobID, cancel.DeployPhaseInstalling, app, backupPath, backupCreated); cancelErr != nil {
		if errors.Is(cancelErr, errDeployCanceled) {
			return summary, cancelErr
		}
		tracker.Fail(app.ID)
		return summary, cancelErr
	}

	slog.Info("restarting service", "service", app.ServiceName)
	tracker.SetPhase(app.ID, progress.ProgressStageRestarting)
	if IsSelfApp(app) {
		// Q-19: this process is the one that dies on restart.
		if err := selfDeployRestart(parent, app, jobID, backupPath); err != nil {
			if errors.Is(err, store.ErrSelfDeployInFlight) {
				return summary, err // the new process settles the job; marker + pending stay
			}
			var guardErr *selfGuardError
			if errors.As(err, &guardErr) {
				// Nothing was restarted: just put the previous binary back.
				tracker.Fail(app.ID)
				if !backupCreated {
					return summary, err
				}
				if restoreErr := restoreBackup(backupPath, app.BinaryPath); restoreErr != nil {
					return summary, fmt.Errorf("%w; restoring the previous binary failed: %v", err, restoreErr)
				}
				return summary, fmt.Errorf("%w; the previous binary was restored", err)
			}
			return summary, rollbackInstalled(app, tracker, backupPath, backupCreated, err)
		}
	} else if out, err := systemctlUnit("restart", app.ServiceName); err != nil {
		// B9: the new binary is installed but the service will not start. Put
		// the previous binary back and restart it instead of leaving it down.
		cause := fmt.Errorf("restart service: %w: %s", err, strings.TrimSpace(string(out)))
		return summary, rollbackInstalled(app, tracker, backupPath, backupCreated, cause)
	}
	if cancelErr := checkpointRollbackInstalled(control, jobID, cancel.DeployPhaseRestarting, app, backupPath, backupCreated); cancelErr != nil {
		if errors.Is(cancelErr, errDeployCanceled) {
			return summary, cancelErr
		}
		tracker.Fail(app.ID)
		return summary, cancelErr
	}

	tracker.SetPhase(app.ID, progress.ProgressStageHealthcheck)
	baseline, haveBaseline := readNRestarts(app.ServiceName)
	unhealthy, abort := runHealthCheck(app.ServiceName, app.HealthURL, baseline, haveBaseline, func() error {
		return checkpointRollbackInstalled(control, jobID, cancel.DeployPhaseHealthcheck, app, backupPath, backupCreated)
	})
	if abort != nil {
		if errors.Is(abort, errDeployCanceled) {
			return summary, abort
		}
		tracker.Fail(app.ID)
		return summary, abort
	}
	if unhealthy == nil {
		leaveMarker = false
		tracker.Finish(app.ID)
		return summary, nil
	}

	slog.Error("rolling back", "reason", unhealthy.Error())
	return summary, rollbackInstalled(app, tracker, backupPath, backupCreated, fmt.Errorf("service failed the health check after deploy: %w", unhealthy))
}

// rollbackInstalled restores the previous binary (backupPath -> app.BinaryPath)
// after the freshly installed one failed to run, and restarts the service. It
// always finishes the tracker as failed and returns an error that starts with
// cause and ends with the outcome of the rollback:
//   - no backup: "rollback unavailable: no previous binary backup";
//   - the restore or the restart of the old binary failed: the failure is
//     reported verbatim and the error does NOT wrap store.ErrRolledBack;
//   - success: "rolled back ..." and the error wraps store.ErrRolledBack so the
//     coordinator sets deploy_jobs.rolled_back = 1.
//
// The failed new binary is replaced by the rename, so no .bak is left behind.
func rollbackInstalled(app *store.App, tracker *progress.ProgressTracker, backupPath string, backupCreated bool, cause error) error {
	tracker.SetPhase(app.ID, progress.ProgressStageRollback)
	defer tracker.Fail(app.ID)
	if !backupCreated {
		return fmt.Errorf("%w; rollback unavailable: no previous binary backup", cause)
	}
	if err := restoreBackup(backupPath, app.BinaryPath); err != nil {
		return fmt.Errorf("%w; rollback rename failed: %v", cause, err)
	}
	if out, err := systemctlUnit("restart", app.ServiceName); err != nil {
		return fmt.Errorf("%w; rollback restart failed: %v: %s", cause, err, strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("%w; rolled back to the previous binary and restarted it: %w", cause, store.ErrRolledBack)
}

func deployDownloadContext(parent context.Context, jobID string, control *cancel.CancelService) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	if control == nil {
		return ctx, cancel
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				requested, err := control.IsCancelRequested(jobID)
				if err == nil && requested {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() {
		cancel()
		<-done
	}
}

func finishCanceledCleanup(control *cancel.CancelService, jobID, tmpPath string) error {
	if _, err := control.Checkpoint(jobID, cancel.DeployPhaseDownloading); err != nil {
		return err
	}
	if err := removeIfExists(tmpPath); err != nil {
		if _, checkpointErr := control.Checkpoint(jobID, cancel.DeployPhaseCleanupFailed); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel cleanup temp: %w", err)
	}
	if _, err := control.Checkpoint(jobID, cancel.DeployPhaseCleanupComplete); err != nil {
		return err
	}
	return nil
}

func checkpointCleanupTemp(control *cancel.CancelService, jobID string, phase cancel.DeployPhase, tmpPath string) error {
	if control == nil {
		return nil
	}
	decision, err := control.Checkpoint(jobID, phase)
	if err != nil || !decision.CancelRequested {
		return err
	}
	if err := removeIfExists(tmpPath); err != nil {
		if _, checkpointErr := control.Checkpoint(jobID, cancel.DeployPhaseCleanupFailed); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel cleanup temp: %w", err)
	}
	if _, err := control.Checkpoint(jobID, cancel.DeployPhaseCleanupComplete); err != nil {
		return err
	}
	return errDeployCanceled
}

func checkpointRestoreBackup(control *cancel.CancelService, jobID, tmpPath, backupPath, binaryPath string, backupCreated bool) error {
	if control == nil {
		return nil
	}
	decision, err := control.Checkpoint(jobID, cancel.DeployPhaseBackupComplete)
	if err != nil || !decision.CancelRequested {
		return err
	}
	if err := removeIfExists(tmpPath); err != nil {
		if _, checkpointErr := control.Checkpoint(jobID, cancel.DeployPhaseCleanupFailed); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel cleanup temp: %w", err)
	}
	if backupCreated {
		if err := restoreBackup(backupPath, binaryPath); err != nil {
			if _, checkpointErr := control.Checkpoint(jobID, cancel.DeployPhaseRollbackFailed); checkpointErr != nil {
				return checkpointErr
			}
			return fmt.Errorf("cancel restore backup: %w", err)
		}
	}
	if _, err := control.Checkpoint(jobID, cancel.DeployPhaseRollbackComplete); err != nil {
		return err
	}
	return errDeployCanceled
}

func checkpointRollbackInstalled(control *cancel.CancelService, jobID string, phase cancel.DeployPhase, app *store.App, backupPath string, backupCreated bool) error {
	if control == nil {
		return nil
	}
	decision, err := control.Checkpoint(jobID, phase)
	if err != nil || !decision.CancelRequested {
		return err
	}
	if !backupCreated {
		if _, checkpointErr := control.Checkpoint(jobID, cancel.DeployPhaseSystemInconsistent); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel rollback unavailable: no previous binary backup")
	}
	if err := restoreBackup(backupPath, app.BinaryPath); err != nil {
		if _, checkpointErr := control.Checkpoint(jobID, cancel.DeployPhaseRollbackFailed); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel rollback rename failed: %w", err)
	}
	if out, err := systemctlUnit("restart", app.ServiceName); err != nil {
		if _, checkpointErr := control.Checkpoint(jobID, cancel.DeployPhaseRollbackFailed); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel rollback restart failed: %w: %s", err, string(out))
	}
	if _, err := control.Checkpoint(jobID, cancel.DeployPhaseRollbackComplete); err != nil {
		return err
	}
	return errDeployCanceled
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
