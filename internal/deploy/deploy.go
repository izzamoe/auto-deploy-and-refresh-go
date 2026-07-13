package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
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
)

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

func downloadBinary(url, tmpPath string, tracker *progress.ProgressTracker, appID string, client *client.Client) (store.DownloadSummary, error) {
	return downloadBinaryContext(context.Background(), url, tmpPath, tracker, appID, client, nil, maxArtifactBytes)
}

func downloadBinaryContext(ctx context.Context, url, tmpPath string, tracker *progress.ProgressTracker, appID string, client *client.Client, headers map[string]string, maxBytes int64) (store.DownloadSummary, error) {
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
func DeployWithControl(app *store.App, jobID, tag string, tracker *progress.ProgressTracker, client *client.Client, control *cancel.CancelService) (store.DownloadSummary, error) {
	return DeployArtifact(app, jobID, tag, tracker, client, control, nil)
}

// DeployArtifact is DeployWithControl with an ArtifactResolver, which lets the
// download authenticate against the GitHub API (required for assets in private
// repositories). A nil resolver reproduces DeployWithControl's public-URL path.
func DeployArtifact(app *store.App, jobID, tag string, tracker *progress.ProgressTracker, client *client.Client, control *cancel.CancelService, resolver ArtifactResolver) (store.DownloadSummary, error) {
	tracker.Start(app.ID, jobID, tag)

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

	downloadCtx, stopDownloadCancelWatcher := deployDownloadContext(jobID, control)
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
	if _, err := os.Stat(app.BinaryPath); err == nil {
		slog.Info("backup", "from", app.BinaryPath, "to", backupPath)
		if err := renameFile(app.BinaryPath, backupPath); err != nil {
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
	if err := renameFile(tmpPath, app.BinaryPath); err != nil {
		cleanup = true
		tracker.Fail(app.ID)
		if backupCreated {
			if restoreErr := renameFile(backupPath, app.BinaryPath); restoreErr != nil {
				return summary, fmt.Errorf("replace binary: %w; restore backup: %v", err, restoreErr)
			}
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
	if out, err := runSystemctl("restart", app.ServiceName); err != nil {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("restart service: %w: %s", err, string(out))
	}
	if cancelErr := checkpointRollbackInstalled(control, jobID, cancel.DeployPhaseRestarting, app, backupPath, backupCreated); cancelErr != nil {
		if errors.Is(cancelErr, errDeployCanceled) {
			return summary, cancelErr
		}
		tracker.Fail(app.ID)
		return summary, cancelErr
	}

	tracker.SetPhase(app.ID, progress.ProgressStageHealthcheck)
	for i := 1; i <= 5; i++ {
		if cancelErr := checkpointRollbackInstalled(control, jobID, cancel.DeployPhaseHealthcheck, app, backupPath, backupCreated); cancelErr != nil {
			if errors.Is(cancelErr, errDeployCanceled) {
				return summary, cancelErr
			}
			tracker.Fail(app.ID)
			return summary, cancelErr
		}
		deployHealthCheckSleep(2 * time.Second)
		out, err := runSystemctl("is-active", app.ServiceName)
		status := strings.TrimSpace(string(out))
		slog.Info("health check", "attempt", i, "status", status)
		if err == nil && status == "active" {
			tracker.Finish(app.ID)
			return summary, nil
		}
	}

	slog.Error("rolling back", "reason", "service failed health check")
	tracker.SetPhase(app.ID, progress.ProgressStageRollback)
	if !backupCreated {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("rollback unavailable: no previous binary backup")
	}
	if err := renameFile(backupPath, app.BinaryPath); err != nil {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("rollback rename failed: %w", err)
	}
	if out, err := runSystemctl("restart", app.ServiceName); err != nil {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("rollback restart failed: %w: %s", err, string(out))
	}

	tracker.Fail(app.ID)
	return summary, fmt.Errorf("service failed to restart after deploy, rolled back")
}

func deployDownloadContext(jobID string, control *cancel.CancelService) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
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
		if err := renameFile(backupPath, binaryPath); err != nil {
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
	if err := renameFile(backupPath, app.BinaryPath); err != nil {
		if _, checkpointErr := control.Checkpoint(jobID, cancel.DeployPhaseRollbackFailed); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel rollback rename failed: %w", err)
	}
	if out, err := runSystemctl("restart", app.ServiceName); err != nil {
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
