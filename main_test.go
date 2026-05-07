package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/protocol"
)

func setupTestWebhook(t *testing.T) (http.HandlerFunc, string) {
	t.Helper()
	db := newTestDB(t)
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("new deploy queue: %v", err)
	}
	store, err := NewAppStore(db)
	if err != nil {
		t.Fatalf("new app store: %v", err)
	}
	secret := "test-secret-1234"
	_, err = store.Create("testapp", secret, "/bin/test", "test.service", "owner/repo", "artifact")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	admission := NewAdmissionService(store, q)
	return multiAppWebhookHandler(admission), secret
}

func setupTestWebhookWithQueueMax(t *testing.T, maxPending int) (http.HandlerFunc, string) {
	t.Helper()
	db := newTestDB(t)
	q, err := NewDeployQueue(db, maxPending)
	if err != nil {
		t.Fatalf("new deploy queue: %v", err)
	}
	store, err := NewAppStore(db)
	if err != nil {
		t.Fatalf("new app store: %v", err)
	}
	secret := "test-secret-1234"
	_, err = store.Create("testapp", secret, "/bin/test", "test.service", "owner/repo", "artifact")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	admission := NewAdmissionService(store, q)
	return multiAppWebhookHandler(admission), secret
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder) response {
	t.Helper()
	var resp response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	return resp
}

func newAuthRequestWithSecret(t *testing.T, tag, secret string) *http.Request {
	t.Helper()
	body := bytes.NewBufferString(`{"tag":"` + tag + `"}`)
	r := httptest.NewRequest(http.MethodPost, "/webhook", body)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+secret)
	return r
}

func TestWebhookValidRequestReturns202Queued(t *testing.T) {
	handler, secret := setupTestWebhook(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newAuthRequestWithSecret(t, "v1.2.0", secret))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	resp := decodeResponse(t, rec)
	if resp.Status != "queued" {
		t.Errorf("expected status=queued, got %q", resp.Status)
	}
	if resp.Tag != "v1.2.0" {
		t.Errorf("expected tag=v1.2.0, got %q", resp.Tag)
	}
}

func TestWebhookSecondRequestAlsoReturns202(t *testing.T) {
	handler, secret := setupTestWebhook(t)

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, newAuthRequestWithSecret(t, "v1.2.0", secret))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first request: expected 202, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, newAuthRequestWithSecret(t, "v1.3.0", secret))
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("second request: expected 202, got %d", rec2.Code)
	}
	resp := decodeResponse(t, rec2)
	if resp.Status != "queued" {
		t.Errorf("expected status=queued, got %q", resp.Status)
	}
}

func TestWebhookDuplicateTagReturnsDuplicateStatus(t *testing.T) {
	handler, secret := setupTestWebhook(t)

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, newAuthRequestWithSecret(t, "v1.2.0", secret))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first request: expected 202, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, newAuthRequestWithSecret(t, "v1.2.0", secret))
	if rec2.Code != http.StatusOK {
		t.Fatalf("duplicate request: expected 200, got %d", rec2.Code)
	}
	resp := decodeResponse(t, rec2)
	if resp.Status != "duplicate" {
		t.Errorf("expected status=duplicate, got %q", resp.Status)
	}
	if resp.Tag != "v1.2.0" {
		t.Errorf("expected tag=v1.2.0, got %q", resp.Tag)
	}
}

func TestWebhookQueueFullReturns503(t *testing.T) {
	handler, secret := setupTestWebhookWithQueueMax(t, 1)

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, newAuthRequestWithSecret(t, "v1.0.0", secret))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first request: expected 202, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, newAuthRequestWithSecret(t, "v2.0.0", secret))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec2.Code)
	}
	resp := decodeResponse(t, rec2)
	if resp.Status != "error" {
		t.Errorf("expected status=error, got %q", resp.Status)
	}
	if resp.Error != "queue full" {
		t.Errorf("expected error=queue full, got %q", resp.Error)
	}
}

func TestWebhookUnauthorizedReturns401(t *testing.T) {
	handler, _ := setupTestWebhook(t)

	cases := []struct {
		name   string
		header string
	}{
		{"no auth header", ""},
		{"wrong bearer token", "Bearer wrong-secret"},
		{"missing bearer prefix", "some-secret"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := bytes.NewBufferString(`{"tag":"v1.0.0"}`)
			r := httptest.NewRequest(http.MethodPost, "/webhook", body)
			r.Header.Set("Content-Type", "application/json")
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
			resp := decodeResponse(t, rec)
			if resp.Status != "error" {
				t.Errorf("expected status=error, got %q", resp.Status)
			}
			if resp.Error != "unauthorized" {
				t.Errorf("expected error=unauthorized, got %q", resp.Error)
			}
		})
	}
}

func TestWebhookInvalidPayloadReturns400(t *testing.T) {
	handler, secret := setupTestWebhook(t)

	cases := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"malformed json", `{not-json}`},
		{"empty tag field", `{"tag":""}`},
		{"missing tag field", `{"other":"value"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+secret)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
			resp := decodeResponse(t, rec)
			if resp.Status != "error" {
				t.Errorf("expected status=error, got %q", resp.Status)
			}
			if resp.Error != "missing or empty tag" {
				t.Errorf("expected error=missing or empty tag, got %q", resp.Error)
			}
		})
	}
}

func TestLoadConfigQueueSettings(t *testing.T) {
	n, path, err := parseQueueConfig("10", "deploy-queue.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 10 {
		t.Errorf("expected QueueMax=10, got %d", n)
	}
	if path != "deploy-queue.db" {
		t.Errorf("expected QueueDBPath=deploy-queue.db, got %s", path)
	}

	n2, path2, err2 := parseQueueConfig("5", "/tmp/custom.db")
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if n2 != 5 {
		t.Errorf("expected QueueMax=5, got %d", n2)
	}
	if path2 != "/tmp/custom.db" {
		t.Errorf("expected QueueDBPath=/tmp/custom.db, got %s", path2)
	}
}

func TestLoadConfigRejectsInvalidQueueMax(t *testing.T) {
	cases := []string{"abc", "0", "-1", ""}
	for _, tc := range cases {
		_, _, err := parseQueueConfig(tc, "deploy-queue.db")
		if err == nil {
			t.Errorf("expected error for DEPLOY_QUEUE_MAX=%q, got nil", tc)
		}
	}
}

func TestWebhookMethodNotAllowedReturns405(t *testing.T) {
	handler, secret := setupTestWebhook(t)

	methods := []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			r := httptest.NewRequest(method, "/webhook", nil)
			r.Header.Set("Authorization", "Bearer "+secret)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405, got %d", rec.Code)
			}
			resp := decodeResponse(t, rec)
			if resp.Status != "error" {
				t.Errorf("expected status=error, got %q", resp.Status)
			}
			if resp.Error != "method not allowed" {
				t.Errorf("expected error=method not allowed, got %q", resp.Error)
			}
		})
	}
}

func testApp(tmpDir string) *App {
	return &App{
		ID:           "test-app-id",
		Name:         "test-app",
		BinaryPath:   filepath.Join(tmpDir, "binary"),
		ServiceName:  "test.service",
		GithubRepo:   "owner/repo",
		ArtifactName: "artifact",
		Enabled:      true,
	}
}

func TestDeployDownloadProgress(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	app := testApp(tmpDir)

	tracker := NewProgressTracker()
	tracker.Start(app.ID, "job-1", "v1.0.0")
	dlClient, _ := client.NewClient(client.WithResponseBodyStream(true))

	tmpPath := filepath.Join(tmpDir, "download.tmp")
	summary, err := downloadBinary(srv.URL+"/artifact", tmpPath, tracker, app.ID, dlClient)
	if err != nil {
		t.Fatalf("downloadBinary: %v", err)
	}

	if summary.Bytes != int64(len(body)) {
		t.Errorf("summary.Bytes = %d, want %d", summary.Bytes, len(body))
	}
	if summary.DurationMs < 0 {
		t.Errorf("summary.DurationMs = %d, want >= 0", summary.DurationMs)
	}

	snap, ok := tracker.Snapshot(app.ID)
	if !ok {
		t.Fatal("expected tracker snapshot for app")
	}
	if snap.DownloadedBytes != int64(len(body)) {
		t.Errorf("tracker downloaded = %d, want %d", snap.DownloadedBytes, len(body))
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("read tmpPath: %v", err)
	}
	if len(data) != len(body) {
		t.Errorf("file size = %d, want %d", len(data), len(body))
	}
}

func TestDeployDownloadRetry(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	app := testApp(tmpDir)

	tracker := NewProgressTracker()
	tracker.Start(app.ID, "job-retry", "v1.0.0")
	dlClient, _ := client.NewClient(client.WithResponseBodyStream(true))

	tmpPath := filepath.Join(tmpDir, "download.tmp")
	summary, err := downloadBinary(srv.URL+"/artifact", tmpPath, tracker, app.ID, dlClient)
	if err != nil {
		t.Fatalf("downloadBinary after retries: %v", err)
	}

	if summary.Bytes != 5 {
		t.Errorf("summary.Bytes = %d, want 5", summary.Bytes)
	}

	if int(attempts.Load()) < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts.Load())
	}
}

func TestDeployDownloadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	app := testApp(tmpDir)

	tracker := NewProgressTracker()
	tracker.Start(app.ID, "job-fail", "v1.0.0")
	dlClient, _ := client.NewClient(client.WithResponseBodyStream(true))

	tmpPath := filepath.Join(tmpDir, "download.tmp")
	_, err := downloadBinary(srv.URL+"/artifact", tmpPath, tracker, app.ID, dlClient)
	if err == nil {
		t.Fatal("expected error from 404 server")
	}
	if !strings.Contains(err.Error(), "download failed") {
		t.Errorf("error = %q, want to contain 'download failed'", err.Error())
	}

	if _, statErr := os.Stat(tmpPath); statErr == nil {
		t.Error("expected temp file to not exist after download failure")
	}
}

func deployClientWithBody(body []byte) *client.Client {
	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	c.Use(func(next client.Endpoint) client.Endpoint {
		return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
			resp.SetStatusCode(http.StatusOK)
			resp.Header.SetContentLength(len(body))
			resp.SetBodyStream(bytes.NewReader(body), len(body))
			return nil
		}
	})
	return c
}

func TestDeployCleanupOnValidationFailure(t *testing.T) {
	body := []byte("not-an-elf-binary")
	dlClient, _ := client.NewClient(client.WithResponseBodyStream(true))
	dlClient.Use(func(next client.Endpoint) client.Endpoint {
		return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
			resp.SetStatusCode(http.StatusOK)
			resp.Header.SetContentLength(len(body))
			resp.SetBodyStream(bytes.NewReader(body), len(body))
			return nil
		}
	})

	tmpDir := t.TempDir()
	app := testApp(tmpDir)
	tracker := NewProgressTracker()

	_, err := deploy(app, "job-elf-fail", "v1.0.0", tracker, dlClient)
	if err == nil {
		t.Fatal("expected deploy to fail on ELF validation")
	}
	if !strings.Contains(err.Error(), "not an ELF executable") {
		t.Errorf("error = %q, want to contain 'not an ELF executable'", err.Error())
	}

	snap, ok := tracker.Snapshot(app.ID)
	if !ok {
		t.Fatal("expected tracker snapshot")
	}
	if snap.Phase != StageFailed {
		t.Errorf("phase = %q, want %q", snap.Phase, StageFailed)
	}

	tmpPath := app.BinaryPath + ".tmp"
	if _, statErr := os.Stat(tmpPath); statErr == nil {
		t.Error("expected temp file cleaned up after ELF validation failure")
	}
}

func TestValidateDownloadedArtifactRejectsZeroLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-artifact")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	err := validateDownloadedArtifact(path)
	if err == nil {
		t.Fatal("expected zero-length artifact to fail validation")
	}
	if !strings.Contains(err.Error(), "artifact validation") || !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want artifact validation empty error", err.Error())
	}
}

func TestValidateDownloadedArtifactRejectsShortArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short-artifact")
	if err := os.WriteFile(path, []byte{0x7f, 'E', 'L'}, 0644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	err := validateDownloadedArtifact(path)
	if err == nil {
		t.Fatal("expected short artifact to fail validation")
	}
	if !strings.Contains(err.Error(), "artifact validation") || !strings.Contains(err.Error(), "too small") {
		t.Errorf("error = %q, want artifact validation too small error", err.Error())
	}
}

func TestValidateDownloadedArtifactRejectsNonELF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "non-elf-artifact")
	if err := os.WriteFile(path, []byte("not-an-elf"), 0644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	err := validateDownloadedArtifact(path)
	if err == nil {
		t.Fatal("expected non-ELF artifact to fail validation")
	}
	if !strings.Contains(err.Error(), "not an ELF executable") {
		t.Errorf("error = %q, want to contain 'not an ELF executable'", err.Error())
	}
}

func TestValidateDownloadedArtifactAcceptsELFMagic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elf-artifact")
	if err := os.WriteFile(path, elfBinary("payload"), 0644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	if err := validateDownloadedArtifact(path); err != nil {
		t.Fatalf("validate ELF artifact: %v", err)
	}
}

func TestDeployInvalidELFLeavesOriginalBinaryUntouched(t *testing.T) {
	tmpDir := t.TempDir()
	app := testApp(tmpDir)
	original := []byte("original-binary")
	if err := os.WriteFile(app.BinaryPath, original, 0755); err != nil {
		t.Fatalf("write original binary: %v", err)
	}

	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		t.Fatalf("systemctl should not run for invalid ELF")
		return nil, nil
	})

	_, err := deploy(app, "job-invalid-elf", "v1.0.0", NewProgressTracker(), deployClientWithBody([]byte("not-an-elf")))
	if err == nil {
		t.Fatal("expected deploy to fail on invalid ELF")
	}
	if !strings.Contains(err.Error(), "not an ELF executable") {
		t.Errorf("error = %q, want to contain 'not an ELF executable'", err.Error())
	}
	assertFileBytes(t, app.BinaryPath, original)
	assertPathMissing(t, app.BinaryPath+".bak")
	assertPathMissing(t, app.BinaryPath+".tmp")
}

func TestDeployShortArtifactLeavesOriginalBinaryUntouched(t *testing.T) {
	tmpDir := t.TempDir()
	app := testApp(tmpDir)
	original := []byte("original-binary")
	if err := os.WriteFile(app.BinaryPath, original, 0755); err != nil {
		t.Fatalf("write original binary: %v", err)
	}

	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		t.Fatalf("systemctl should not run for short artifact")
		return nil, nil
	})
	stubRename(t, func(oldpath, newpath string) error {
		t.Fatalf("rename should not run before short artifact validation succeeds")
		return nil
	})

	_, err := deploy(app, "job-short-artifact", "v1.0.0", NewProgressTracker(), deployClientWithBody([]byte{0x7f, 'E', 'L'}))
	if err == nil {
		t.Fatal("expected deploy to fail on short artifact")
	}
	if !strings.Contains(err.Error(), "artifact validation") || !strings.Contains(err.Error(), "too small") {
		t.Errorf("error = %q, want artifact validation too small error", err.Error())
	}
	assertFileBytes(t, app.BinaryPath, original)
	assertPathMissing(t, app.BinaryPath+".bak")
	assertPathMissing(t, app.BinaryPath+".tmp")
}

func TestDeployReplaceFailureAfterBackupRestoresOriginalBinary(t *testing.T) {
	tmpDir := t.TempDir()
	app := testApp(tmpDir)
	original := []byte("original-binary")
	if err := os.WriteFile(app.BinaryPath, original, 0755); err != nil {
		t.Fatalf("write original binary: %v", err)
	}

	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		t.Fatalf("systemctl should not run when replacement fails")
		return nil, nil
	})
	stubRename(t, func(oldpath, newpath string) error {
		if oldpath == app.BinaryPath+".tmp" && newpath == app.BinaryPath {
			return fmt.Errorf("forced replacement failure")
		}
		return os.Rename(oldpath, newpath)
	})

	_, err := deploy(app, "job-replace-fail", "v1.0.0", NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")))
	if err == nil {
		t.Fatal("expected deploy to fail on replacement")
	}
	if !strings.Contains(err.Error(), "replace binary") {
		t.Errorf("error = %q, want to contain 'replace binary'", err.Error())
	}
	assertFileBytes(t, app.BinaryPath, original)
	assertPathMissing(t, app.BinaryPath+".bak")
	assertPathMissing(t, app.BinaryPath+".tmp")
}

func TestDeployHealthFailureWithBackupRestoresPreviousBinary(t *testing.T) {
	tmpDir := t.TempDir()
	app := testApp(tmpDir)
	original := []byte("original-binary")
	if err := os.WriteFile(app.BinaryPath, original, 0755); err != nil {
		t.Fatalf("write original binary: %v", err)
	}

	stubDeploySleep(t)
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		switch name {
		case "restart":
			return []byte("restarted"), nil
		case "is-active":
			return []byte("inactive\n"), nil
		default:
			return nil, fmt.Errorf("unexpected systemctl command %q", name)
		}
	})

	_, err := deploy(app, "job-health-fail", "v1.0.0", NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")))
	if err == nil {
		t.Fatal("expected deploy to fail health checks")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("error = %q, want to contain 'rolled back'", err.Error())
	}
	assertFileBytes(t, app.BinaryPath, original)
	assertPathMissing(t, app.BinaryPath+".bak")
	assertPathMissing(t, app.BinaryPath+".tmp")
}

func TestDeployHealthFailureNoBackupReturnsExplicitFailureWithoutRollbackClaim(t *testing.T) {
	tmpDir := t.TempDir()
	app := testApp(tmpDir)
	deployed := elfBinary("new-binary")

	stubDeploySleep(t)
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		switch name {
		case "restart":
			return []byte("restarted"), nil
		case "is-active":
			return []byte("inactive\n"), nil
		default:
			return nil, fmt.Errorf("unexpected systemctl command %q", name)
		}
	})

	_, err := deploy(app, "job-health-no-backup", "v1.0.0", NewProgressTracker(), deployClientWithBody(deployed))
	if err == nil {
		t.Fatal("expected deploy to fail health checks without backup")
	}
	if !strings.Contains(err.Error(), "rollback unavailable: no previous binary backup") {
		t.Errorf("error = %q, want explicit missing-backup failure", err.Error())
	}
	if strings.Contains(err.Error(), "rolled back") {
		t.Errorf("error = %q, must not claim rollback without backup", err.Error())
	}
	assertFileBytes(t, app.BinaryPath, deployed)
	assertPathMissing(t, app.BinaryPath+".bak")
	assertPathMissing(t, app.BinaryPath+".tmp")
}

func TestDeploySuccessStageLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	app := testApp(tmpDir)
	if err := os.WriteFile(app.BinaryPath, []byte("original-binary"), 0755); err != nil {
		t.Fatalf("write original binary: %v", err)
	}

	tracker := NewProgressTracker()
	var stages []string
	recordStage := func(label string) {
		t.Helper()
		snap, ok := tracker.Snapshot(app.ID)
		if !ok {
			t.Fatalf("missing tracker snapshot at %s", label)
		}
		stages = append(stages, snap.Phase)
	}

	stubDeploySleep(t)
	stubChmod(t, func(path string, mode os.FileMode) error {
		recordStage("validating")
		return os.Chmod(path, mode)
	})
	stubRename(t, func(oldpath, newpath string) error {
		switch {
		case oldpath == app.BinaryPath && newpath == app.BinaryPath+".bak":
			recordStage("backup")
		case oldpath == app.BinaryPath+".tmp" && newpath == app.BinaryPath:
			recordStage("install")
		}
		return os.Rename(oldpath, newpath)
	})
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		switch name {
		case "restart":
			recordStage("restart")
			return []byte("restarted"), nil
		case "is-active":
			recordStage("healthcheck")
			return []byte("active\n"), nil
		default:
			return nil, fmt.Errorf("unexpected systemctl command %q", name)
		}
	})

	if _, err := deploy(app, "job-success", "v1.0.0", tracker, deployClientWithBody(elfBinary("new-binary"))); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	snap, ok := tracker.Snapshot(app.ID)
	if !ok {
		t.Fatal("expected terminal tracker snapshot")
	}
	if snap.Phase != StageSucceeded {
		t.Fatalf("terminal phase = %q, want %q", snap.Phase, StageSucceeded)
	}

	want := []string{StageValidating, StageBackingUp, StageInstalling, StageRestarting, StageHealthcheck}
	if !equalStrings(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
}

func TestDeployFailureStageLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	app := testApp(tmpDir)
	if err := os.WriteFile(app.BinaryPath, []byte("original-binary"), 0755); err != nil {
		t.Fatalf("write original binary: %v", err)
	}

	tracker := NewProgressTracker()
	var stages []string
	recordStage := func(label string) {
		t.Helper()
		snap, ok := tracker.Snapshot(app.ID)
		if !ok {
			t.Fatalf("missing tracker snapshot at %s", label)
		}
		stages = append(stages, snap.Phase)
	}

	stubDeploySleep(t)
	stubChmod(t, func(path string, mode os.FileMode) error {
		recordStage("validating")
		return os.Chmod(path, mode)
	})
	stubRename(t, func(oldpath, newpath string) error {
		switch {
		case oldpath == app.BinaryPath && newpath == app.BinaryPath+".bak":
			recordStage("backup")
		case oldpath == app.BinaryPath+".tmp" && newpath == app.BinaryPath:
			recordStage("install")
		case oldpath == app.BinaryPath+".bak" && newpath == app.BinaryPath:
			recordStage("rollback")
		}
		return os.Rename(oldpath, newpath)
	})
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		switch name {
		case "restart":
			if len(stages) > 0 && stages[len(stages)-1] == StageRollback {
				recordStage("rollback restart")
			} else {
				recordStage("restart")
			}
			return []byte("restarted"), nil
		case "is-active":
			recordStage("healthcheck")
			return []byte("inactive\n"), nil
		default:
			return nil, fmt.Errorf("unexpected systemctl command %q", name)
		}
	})

	_, err := deploy(app, "job-failed", "v1.0.0", tracker, deployClientWithBody(elfBinary("new-binary")))
	if err == nil {
		t.Fatal("expected deploy to fail health checks")
	}

	snap, ok := tracker.Snapshot(app.ID)
	if !ok {
		t.Fatal("expected terminal tracker snapshot")
	}
	if snap.Phase != StageFailed {
		t.Fatalf("terminal phase = %q, want %q", snap.Phase, StageFailed)
	}

	want := []string{
		StageValidating,
		StageBackingUp,
		StageInstalling,
		StageRestarting,
		StageHealthcheck,
		StageHealthcheck,
		StageHealthcheck,
		StageHealthcheck,
		StageHealthcheck,
		StageRollback,
		StageRollback,
	}
	if !equalStrings(stages, want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
}

func TestDeployChmodFailureSetsFailedAfterValidating(t *testing.T) {
	tmpDir := t.TempDir()
	app := testApp(tmpDir)
	tracker := NewProgressTracker()
	stubChmod(t, func(path string, mode os.FileMode) error {
		snap, ok := tracker.Snapshot(app.ID)
		if !ok {
			t.Fatal("expected tracker snapshot before chmod")
		}
		if snap.Phase != StageValidating {
			t.Fatalf("phase before chmod = %q, want %q", snap.Phase, StageValidating)
		}
		return fmt.Errorf("forced chmod failure")
	})

	_, err := deploy(app, "job-chmod-fail", "v1.0.0", tracker, deployClientWithBody(elfBinary("new-binary")))
	if err == nil {
		t.Fatal("expected deploy to fail chmod")
	}
	snap, ok := tracker.Snapshot(app.ID)
	if !ok {
		t.Fatal("expected tracker snapshot")
	}
	if snap.Phase != StageFailed {
		t.Fatalf("phase = %q, want %q", snap.Phase, StageFailed)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func elfBinary(payload string) []byte {
	return append([]byte{0x7f, 'E', 'L', 'F'}, []byte(payload)...)
}

func stubSystemctl(t *testing.T, fn func(string, ...string) ([]byte, error)) {
	t.Helper()
	original := runSystemctl
	runSystemctl = fn
	t.Cleanup(func() { runSystemctl = original })
}

func stubRename(t *testing.T, fn func(string, string) error) {
	t.Helper()
	original := renameFile
	renameFile = fn
	t.Cleanup(func() { renameFile = original })
}

func stubChmod(t *testing.T, fn func(string, os.FileMode) error) {
	t.Helper()
	original := chmodFile
	chmodFile = fn
	t.Cleanup(func() { chmodFile = original })
}

func stubDeploySleep(t *testing.T) {
	t.Helper()
	original := deployHealthCheckSleep
	deployHealthCheckSleep = func(time.Duration) {}
	t.Cleanup(func() { deployHealthCheckSleep = original })
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected %s to be absent", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}
