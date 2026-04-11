package main

import (
	"context"
	"database/sql"
	"encoding/json"
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
)

type response struct {
	Status string `json:"status"`
	Tag    string `json:"tag,omitempty"`
	Error  string `json:"error,omitempty"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

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
	dlClient := NewDownloadClient()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	coordinator := NewCoordinator(appStore, q, func(app *App, jobID, tag string) (DownloadSummary, error) {
		return deploy(app, jobID, tag, tracker, dlClient)
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
	progressAdminHandler := NewProgressAdminHandler(tracker)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", multiAppWebhookHandler(admission))
	authMiddleware := BasicAuthMiddleware(serviceCfg.AdminUsername, serviceCfg.AdminPassword)
	RegisterAdminAppRoutes(mux, appAdminHandler, authMiddleware)
	RegisterAdminHistoryRoutes(mux, historyAdminHandler, authMiddleware)
	RegisterAdminProgressRoutes(mux, progressAdminHandler, authMiddleware)

	server := &http.Server{
		Addr:    serviceCfg.ListenAddr,
		Handler: mux,
	}

	go func() {
		slog.Info("starting", "addr", serviceCfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	coordinator.Wait()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
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
	slog.Info("downloading", "url", url)
	resp, err := DownloadWithRetry(client, url, nil)
	if err != nil {
		return DownloadSummary{}, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	f, err := os.Create(tmpPath)
	if err != nil {
		return DownloadSummary{}, fmt.Errorf("create tmp file: %w", err)
	}

	reader := NewCountingReader(resp.Body, resp.ContentLength, func(downloaded, total int64, speedBPS float64) {
		tracker.Update(appID, downloaded, total, speedBPS)
	})

	downloadStart := time.Now()
	written, err := io.Copy(f, io.LimitReader(reader, 100*1024*1024))
	if err != nil {
		f.Close()
		return DownloadSummary{}, fmt.Errorf("write tmp file: %w", err)
	}
	f.Close()

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

func deploy(app *App, jobID, tag string, tracker *ProgressTracker, client *http.Client) (DownloadSummary, error) {
	tracker.Start(app.ID, jobID, tag)

	tmpPath := app.BinaryPath + ".tmp"

	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", app.GithubRepo, tag, app.ArtifactName)
	summary, err := downloadBinary(url, tmpPath, tracker, app.ID, client)
	if err != nil {
		tracker.Fail(app.ID)
		return DownloadSummary{}, err
	}

	ef, err := os.Open(tmpPath)
	if err != nil {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("open tmp for validation: %w", err)
	}
	magic := make([]byte, 4)
	if _, err := io.ReadFull(ef, magic); err != nil {
		ef.Close()
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("read magic bytes: %w", err)
	}
	ef.Close()

	if magic[0] != 0x7f || magic[1] != 'E' || magic[2] != 'L' || magic[3] != 'F' {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("invalid binary: not an ELF executable")
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("chmod: %w", err)
	}

	if _, err := os.Stat(app.BinaryPath); err == nil {
		slog.Info("backup", "from", app.BinaryPath, "to", app.BinaryPath+".bak")
		if err := os.Rename(app.BinaryPath, app.BinaryPath+".bak"); err != nil {
			tracker.Fail(app.ID)
			return summary, fmt.Errorf("backup: %w", err)
		}
	}

	slog.Info("replacing binary")
	cleanup = false
	if err := os.Rename(tmpPath, app.BinaryPath); err != nil {
		cleanup = true
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("replace binary: %w", err)
	}

	slog.Info("restarting service", "service", app.ServiceName)
	if out, err := exec.Command("systemctl", "restart", app.ServiceName).CombinedOutput(); err != nil {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("restart service: %w: %s", err, string(out))
	}

	for i := 1; i <= 5; i++ {
		time.Sleep(2 * time.Second)
		out, err := exec.Command("systemctl", "is-active", app.ServiceName).CombinedOutput()
		status := strings.TrimSpace(string(out))
		slog.Info("health check", "attempt", i, "status", status)
		if err == nil && status == "active" {
			tracker.Finish(app.ID)
			return summary, nil
		}
	}

	slog.Error("rolling back", "reason", "service failed health check")
	if err := os.Rename(app.BinaryPath+".bak", app.BinaryPath); err != nil {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("rollback rename failed: %w", err)
	}
	if out, err := exec.Command("systemctl", "restart", app.ServiceName).CombinedOutput(); err != nil {
		tracker.Fail(app.ID)
		return summary, fmt.Errorf("rollback restart failed: %w: %s", err, string(out))
	}

	tracker.Fail(app.ID)
	return summary, fmt.Errorf("service failed to restart after deploy, rolled back")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
