package main

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	Secret       string
	BinaryPath   string
	ServiceName  string
	ListenAddr   string
	GithubRepo   string
	ArtifactName string
}

type response struct {
	Status string `json:"status"`
	Tag    string `json:"tag,omitempty"`
	Error  string `json:"error,omitempty"`
}

var deployMu sync.Mutex

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	cfg := loadConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", webhookHandler(cfg))

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("starting", "addr", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
}

func loadConfig() *Config {
	secret := os.Getenv("WEBHOOK_SECRET")
	if secret == "" {
		slog.Error("WEBHOOK_SECRET environment variable is required")
		os.Exit(1)
	}

	return &Config{
		Secret:       secret,
		BinaryPath:   envOrDefault("DEPLOY_BINARY_PATH", "/root/pb/pocketbase"),
		ServiceName:  envOrDefault("DEPLOY_SERVICE_NAME", "pocketbase.service"),
		ListenAddr:   envOrDefault("LISTEN_ADDR", ":9000"),
		GithubRepo:   envOrDefault("GITHUB_REPO", "izzamoe/backend-kas"),
		ArtifactName: envOrDefault("ARTIFACT_NAME", "kas-linux-arm64"),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func webhookHandler(cfg *Config) http.HandlerFunc {
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
		if !hmac.Equal([]byte(token), []byte(cfg.Secret)) {
			writeJSON(w, http.StatusUnauthorized, response{Status: "error", Error: "unauthorized"})
			return
		}

		var body struct {
			Tag string `json:"tag"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Tag == "" {
			writeJSON(w, http.StatusBadRequest, response{Status: "error", Error: "missing or empty tag"})
			return
		}

		if !deployMu.TryLock() {
			writeJSON(w, http.StatusConflict, response{Status: "error", Error: "deploy already in progress"})
			return
		}
		defer deployMu.Unlock()

		if err := deploy(cfg, body.Tag); err != nil {
			slog.Error("deploy failed", "tag", body.Tag, "err", err)
			writeJSON(w, http.StatusInternalServerError, response{Status: "error", Tag: body.Tag, Error: err.Error()})
			return
		}

		slog.Info("deploy succeeded", "tag", body.Tag)
		writeJSON(w, http.StatusOK, response{Status: "ok", Tag: body.Tag})
	}
}

func deploy(cfg *Config, tag string) error {
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", cfg.GithubRepo, tag, cfg.ArtifactName)
	tmpPath := cfg.BinaryPath + ".tmp"

	// Download
	slog.Info("downloading", "url", url, "tag", tag)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create tmp file: %w", err)
	}

	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(f, io.LimitReader(resp.Body, 100*1024*1024)); err != nil {
		f.Close()
		return fmt.Errorf("write tmp file: %w", err)
	}
	f.Close()

	// Validate ELF
	ef, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("open tmp for validation: %w", err)
	}
	magic := make([]byte, 4)
	if _, err := io.ReadFull(ef, magic); err != nil {
		ef.Close()
		return fmt.Errorf("read magic bytes: %w", err)
	}
	ef.Close()

	if magic[0] != 0x7f || magic[1] != 'E' || magic[2] != 'L' || magic[3] != 'F' {
		return fmt.Errorf("invalid binary: not an ELF executable")
	}

	// Chmod
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// Backup existing binary if it exists
	if _, err := os.Stat(cfg.BinaryPath); err == nil {
		slog.Info("backup", "from", cfg.BinaryPath, "to", cfg.BinaryPath+".bak")
		if err := os.Rename(cfg.BinaryPath, cfg.BinaryPath+".bak"); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}

	// Replace
	slog.Info("replacing binary")
	cleanup = false
	if err := os.Rename(tmpPath, cfg.BinaryPath); err != nil {
		cleanup = true
		return fmt.Errorf("replace binary: %w", err)
	}

	// Restart service
	slog.Info("restarting service", "service", cfg.ServiceName)
	if out, err := exec.Command("systemctl", "restart", cfg.ServiceName).CombinedOutput(); err != nil {
		return fmt.Errorf("restart service: %w: %s", err, string(out))
	}

	// Health check
	for i := 1; i <= 5; i++ {
		time.Sleep(2 * time.Second)
		out, err := exec.Command("systemctl", "is-active", cfg.ServiceName).CombinedOutput()
		status := strings.TrimSpace(string(out))
		slog.Info("health check", "attempt", i, "status", status)
		if err == nil && status == "active" {
			return nil
		}
	}

	// Rollback
	slog.Error("rolling back", "reason", "service failed health check")
	if err := os.Rename(cfg.BinaryPath+".bak", cfg.BinaryPath); err != nil {
		return fmt.Errorf("rollback rename failed: %w", err)
	}
	if out, err := exec.Command("systemctl", "restart", cfg.ServiceName).CombinedOutput(); err != nil {
		return fmt.Errorf("rollback restart failed: %w: %s", err, string(out))
	}

	return fmt.Errorf("service failed to restart after deploy, rolled back")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
