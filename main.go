package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/common/hlog"
)

var (
	runSystemctl = func(name string, args ...string) ([]byte, error) {
		cmdArgs := append([]string{name}, args...)
		return exec.Command("systemctl", cmdArgs...).CombinedOutput()
	}
	renameFile             = os.Rename
	chmodFile              = os.Chmod
	deployHealthCheckSleep = time.Sleep
)

const maxArtifactBytes int64 = 100 * 1024 * 1024

var errDeployCanceled = errors.New("deploy canceled at safe checkpoint")

type response struct {
	Status string `json:"status"`
	Tag    string `json:"tag,omitempty"`
	Error  string `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	hertzLogger, err := NewZapLogger()
	if err != nil {
		slog.Error("zap logger init failed", "err", err)
		os.Exit(1)
	}
	hlog.SetLogger(hertzLogger)

	serviceCfg, err := LoadServiceConfig()
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", serviceCfg.QueueDBPath)
	if err != nil {
		slog.Error("db open failed", "err", err)
		os.Exit(1)
	}
	db.SetMaxOpenConns(1)

	q, err := NewDeployQueue(db, serviceCfg.QueueMax)
	if err != nil {
		slog.Error("queue init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	defer q.Close()

	appStore, err := NewAppStore(db)
	if err != nil {
		slog.Error("app store init failed", "err", err)
		db.Close()
		os.Exit(1)
	}

	if err := q.RecoverStale(); err != nil {
		slog.Warn("stale recovery", "err", err)
	}

	legacyCfg := LoadLegacyBootstrapConfig()
	if _, err := appStore.BootstrapIfEmpty(legacyCfg); err != nil {
		slog.Error("bootstrap failed", "err", err)
		db.Close()
		os.Exit(1)
	}

	admission := NewAdmissionService(appStore, q)

	tracker := NewProgressTracker()
	dlClient := NewDownloadClient(serviceCfg.DownloadDNS)
	adminEventHub := NewAdminEventHub(tracker)
	tracker.SetProgressSink(adminEventHub)
	cancelService := NewCancelService(q)
	cancelService.SetEventHub(adminEventHub)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	coordinator := NewCoordinator(appStore, q, func(app *App, jobID, tag string) (DownloadSummary, error) {
		return deployWithControl(app, jobID, tag, tracker, dlClient, cancelService)
	}, tracker)
	coordinator.Start(ctx)

	adminHandler, err := NewAdminHandler(serviceCfg)
	if err != nil {
		slog.Error("admin handler init failed", "err", err)
		db.Close()
		os.Exit(1)
	}
	appAdminHandler := NewAppAdminHandler(appStore, q, adminHandler.templates, tracker)
	historyAdminHandler := NewHistoryAdminHandler(appStore, q, adminHandler.templates, tracker)
	progressAdminHandler := NewProgressAdminHandler(tracker, appStore, q, adminHandler.templates)
	adminAPIHandler := NewAdminAPIHandler(appStore, q, tracker, cancelService)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", multiAppWebhookHandler(admission))
	authMiddleware := BasicAuthMiddleware(serviceCfg.AdminUsername, serviceCfg.AdminPassword)
	RegisterAdminEventRoutes(mux, adminEventHub, authMiddleware)
	RegisterAdminAPIRoutes(mux, adminAPIHandler, authMiddleware)
	RegisterAdminProgressRoutes(mux, progressAdminHandler, authMiddleware)
	RegisterAdminAppActionRoutes(mux, appAdminHandler, authMiddleware)
	RegisterAdminHistoryActionRoutes(mux, historyAdminHandler, authMiddleware)
	RegisterAdminSPARoutes(mux, authMiddleware)

	h := server.New(
		server.WithHostPorts(serviceCfg.ListenAddr),
		server.WithReadTimeout(45*time.Second),
		server.WithWriteTimeout(45*time.Second),
		server.WithIdleTimeout(90*time.Second),
		server.WithKeepAliveTimeout(1*time.Minute),
		server.WithMaxRequestBodySize(16*1024*1024),
		server.WithMaxHeaderBytes(1<<20),
		server.WithMaxKeepBodySize(4*1024*1024),
		server.WithReadBufferSize(4*1024),
		server.WithKeepAlive(true),
		server.WithExitWaitTime(30*time.Second),
		server.WithNetwork("tcp"),
	)

	setupMiddleware(h)

	h.Any("/*path", func(ctx context.Context, c *app.RequestContext) {
		adaptor.HertzHandler(mux)(ctx, c)
	})

	go func() {
		slog.Info("starting", "addr", serviceCfg.ListenAddr)
		if err := h.Run(); err != nil {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	coordinator.Wait()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := h.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
}


func multiAppWebhookHandler(admission *AdmissionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, response{Status: "error", Error: "method not allowed"})
			return
		}
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, response{Status: "error", Error: "unauthorized"})
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")

		var body struct {
			Tag string `json:"tag"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Tag == "" {
			writeJSON(w, http.StatusBadRequest, response{Status: "error", Error: "missing or empty tag"})
			return
		}

		result := admission.Admit(token, body.Tag)
		switch result.Outcome {
		case OutcomeUnauthorized:
			writeJSON(w, http.StatusUnauthorized, response{Status: "error", Error: "unauthorized"})
		case OutcomeBadRequest:
			writeJSON(w, http.StatusBadRequest, response{Status: "error", Error: "missing or empty tag"})
		case OutcomeDuplicate:
			writeJSON(w, http.StatusOK, response{Status: "duplicate", Tag: body.Tag})
		case OutcomeQueued:
			writeJSON(w, http.StatusAccepted, response{Status: "queued", Tag: body.Tag})
		case OutcomeError:
			errMsg := "internal error"
			if result.Error != nil {
				errMsg = result.Error.Error()
			}
			if errMsg == "queue full" {
				writeJSON(w, http.StatusServiceUnavailable, response{Status: "error", Error: "queue full"})
			} else {
				writeJSON(w, http.StatusInternalServerError, response{Status: "error", Error: errMsg})
			}
		default:
			writeJSON(w, http.StatusInternalServerError, response{Status: "error", Error: "unexpected outcome"})
		}
	}
}

func downloadBinary(url, tmpPath string, tracker *ProgressTracker, appID string, client *http.Client) (DownloadSummary, error) {
	return downloadBinaryContext(context.Background(), url, tmpPath, tracker, appID, client, maxArtifactBytes)
}

func downloadBinaryWithMaxBytes(url, tmpPath string, tracker *ProgressTracker, appID string, client *http.Client, maxBytes int64) (DownloadSummary, error) {
	return downloadBinaryContext(context.Background(), url, tmpPath, tracker, appID, client, maxBytes)
}

func downloadBinaryContext(ctx context.Context, url, tmpPath string, tracker *ProgressTracker, appID string, client *http.Client, maxBytes int64) (DownloadSummary, error) {
	slog.Info("downloading", "url", url)
	resp, err := DownloadWithRetryContext(ctx, client, url, nil)
	if err != nil {
		return DownloadSummary{}, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if maxBytes < 0 {
		return DownloadSummary{}, fmt.Errorf("download too large: invalid max bytes %d", maxBytes)
	}
	if resp.ContentLength > maxBytes {
		return DownloadSummary{}, fmt.Errorf("download too large: content length %d exceeds max %d", resp.ContentLength, maxBytes)
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		return DownloadSummary{}, fmt.Errorf("create tmp file: %w", err)
	}

	reader := NewCountingReader(resp.Body, resp.ContentLength, func(downloaded, total int64, speedBPS float64) {
		tracker.Update(appID, downloaded, total, speedBPS)
	})

	downloadStart := time.Now()
	probeLimit := maxBytes + 1
	if probeLimit < maxBytes {
		probeLimit = maxBytes
	}
	written, err := io.Copy(f, io.LimitReader(reader, probeLimit))
	if err != nil {
		cleanupRejectedDownload(f, tmpPath)
		if ctx.Err() != nil {
			return DownloadSummary{}, ctx.Err()
		}
		if resp.ContentLength >= 0 && written != resp.ContentLength {
			return DownloadSummary{}, fmt.Errorf("download incomplete: wrote %d bytes, expected %d: %w", written, resp.ContentLength, err)
		}
		return DownloadSummary{}, fmt.Errorf("write tmp file: %w", err)
	}
	if ctx.Err() != nil {
		cleanupRejectedDownload(f, tmpPath)
		return DownloadSummary{}, ctx.Err()
	}
	if written > maxBytes {
		cleanupRejectedDownload(f, tmpPath)
		return DownloadSummary{}, fmt.Errorf("download too large: wrote %d bytes, max %d", written, maxBytes)
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		cleanupRejectedDownload(f, tmpPath)
		return DownloadSummary{}, fmt.Errorf("download incomplete: wrote %d bytes, expected %d", written, resp.ContentLength)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return DownloadSummary{}, fmt.Errorf("write tmp file: %w", err)
	}

	duration := time.Since(downloadStart)
	var avgSpeed float64
	if duration > 0 {
		avgSpeed = float64(written) / duration.Seconds()
	}

	return DownloadSummary{
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

func deploy(app *App, jobID, tag string, tracker *ProgressTracker, client *http.Client) (DownloadSummary, error) {
	return deployWithControl(app, jobID, tag, tracker, client, nil)
}

func deployWithControl(app *App, jobID, tag string, tracker *ProgressTracker, client *http.Client, control *CancelService) (DownloadSummary, error) {
	tracker.Start(app.ID, jobID, tag)

	tmpPath := app.BinaryPath + ".tmp"

	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", app.GithubRepo, tag, app.ArtifactName)
	downloadCtx, stopDownloadCancelWatcher := deployDownloadContext(jobID, control)
	summary, err := downloadBinaryContext(downloadCtx, url, tmpPath, tracker, app.ID, client, maxArtifactBytes)
	stopDownloadCancelWatcher()
	if err != nil {
		if errors.Is(err, context.Canceled) && control != nil {
			if cancelErr := finishCanceledCleanup(control, jobID, tmpPath); cancelErr != nil {
				tracker.Fail(app.ID)
				return DownloadSummary{}, cancelErr
			}
			return DownloadSummary{}, errDeployCanceled
		}
		tracker.Fail(app.ID)
		return DownloadSummary{}, err
	}
	if cancelErr := checkpointCleanupTemp(control, jobID, DeployPhaseDownloaded, tmpPath); cancelErr != nil {
		if errors.Is(cancelErr, errDeployCanceled) {
			return summary, cancelErr
		}
		tracker.Fail(app.ID)
		return summary, cancelErr
	}
	tracker.SetPhase(app.ID, StageValidating)
	if cancelErr := checkpointCleanupTemp(control, jobID, DeployPhaseValidating, tmpPath); cancelErr != nil {
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
	tracker.SetPhase(app.ID, StageBackingUp)
	if cancelErr := checkpointCleanupTemp(control, jobID, DeployPhaseBackingUp, tmpPath); cancelErr != nil {
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
	tracker.SetPhase(app.ID, StageInstalling)
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
	if cancelErr := checkpointRollbackInstalled(control, jobID, DeployPhaseInstalling, app, backupPath, backupCreated); cancelErr != nil {
		if errors.Is(cancelErr, errDeployCanceled) {
			return summary, cancelErr
		}
		tracker.Fail(app.ID)
		return summary, cancelErr
	}

	slog.Info("restarting service", "service", app.ServiceName)
	tracker.SetPhase(app.ID, StageRestarting)
	if out, err := runSystemctl("restart", app.ServiceName); err != nil {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("restart service: %w: %s", err, string(out))
	}
	if cancelErr := checkpointRollbackInstalled(control, jobID, DeployPhaseRestarting, app, backupPath, backupCreated); cancelErr != nil {
		if errors.Is(cancelErr, errDeployCanceled) {
			return summary, cancelErr
		}
		tracker.Fail(app.ID)
		return summary, cancelErr
	}

	tracker.SetPhase(app.ID, StageHealthcheck)
	for i := 1; i <= 5; i++ {
		if cancelErr := checkpointRollbackInstalled(control, jobID, DeployPhaseHealthcheck, app, backupPath, backupCreated); cancelErr != nil {
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
	tracker.SetPhase(app.ID, StageRollback)
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

func deployDownloadContext(jobID string, control *CancelService) (context.Context, func()) {
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

func finishCanceledCleanup(control *CancelService, jobID, tmpPath string) error {
	if _, err := control.Checkpoint(jobID, DeployPhaseDownloading); err != nil {
		return err
	}
	if err := removeIfExists(tmpPath); err != nil {
		if _, checkpointErr := control.Checkpoint(jobID, DeployPhaseCleanupFailed); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel cleanup temp: %w", err)
	}
	if _, err := control.Checkpoint(jobID, DeployPhaseCleanupComplete); err != nil {
		return err
	}
	return nil
}

func checkpointCleanupTemp(control *CancelService, jobID string, phase DeployPhase, tmpPath string) error {
	if control == nil {
		return nil
	}
	decision, err := control.Checkpoint(jobID, phase)
	if err != nil || !decision.CancelRequested {
		return err
	}
	if err := removeIfExists(tmpPath); err != nil {
		if _, checkpointErr := control.Checkpoint(jobID, DeployPhaseCleanupFailed); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel cleanup temp: %w", err)
	}
	if _, err := control.Checkpoint(jobID, DeployPhaseCleanupComplete); err != nil {
		return err
	}
	return errDeployCanceled
}

func checkpointRestoreBackup(control *CancelService, jobID, tmpPath, backupPath, binaryPath string, backupCreated bool) error {
	if control == nil {
		return nil
	}
	decision, err := control.Checkpoint(jobID, DeployPhaseBackupComplete)
	if err != nil || !decision.CancelRequested {
		return err
	}
	if err := removeIfExists(tmpPath); err != nil {
		if _, checkpointErr := control.Checkpoint(jobID, DeployPhaseCleanupFailed); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel cleanup temp: %w", err)
	}
	if backupCreated {
		if err := renameFile(backupPath, binaryPath); err != nil {
			if _, checkpointErr := control.Checkpoint(jobID, DeployPhaseRollbackFailed); checkpointErr != nil {
				return checkpointErr
			}
			return fmt.Errorf("cancel restore backup: %w", err)
		}
	}
	if _, err := control.Checkpoint(jobID, DeployPhaseRollbackComplete); err != nil {
		return err
	}
	return errDeployCanceled
}

func checkpointRollbackInstalled(control *CancelService, jobID string, phase DeployPhase, app *App, backupPath string, backupCreated bool) error {
	if control == nil {
		return nil
	}
	decision, err := control.Checkpoint(jobID, phase)
	if err != nil || !decision.CancelRequested {
		return err
	}
	if !backupCreated {
		if _, checkpointErr := control.Checkpoint(jobID, DeployPhaseSystemInconsistent); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel rollback unavailable: no previous binary backup")
	}
	if err := renameFile(backupPath, app.BinaryPath); err != nil {
		if _, checkpointErr := control.Checkpoint(jobID, DeployPhaseRollbackFailed); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel rollback rename failed: %w", err)
	}
	if out, err := runSystemctl("restart", app.ServiceName); err != nil {
		if _, checkpointErr := control.Checkpoint(jobID, DeployPhaseRollbackFailed); checkpointErr != nil {
			return checkpointErr
		}
		return fmt.Errorf("cancel rollback restart failed: %w: %s", err, string(out))
	}
	if _, err := control.Checkpoint(jobID, DeployPhaseRollbackComplete); err != nil {
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
