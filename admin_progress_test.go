package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func makeProgressMux(tracker *ProgressTracker) *http.ServeMux {
	mux := http.NewServeMux()
	handler := NewProgressAdminHandler(tracker)
	middleware := BasicAuthMiddleware("admin", "secret")
	RegisterAdminProgressRoutes(mux, handler, middleware)
	return mux
}

func TestProgressStreamSSEHeaders(t *testing.T) {
	tracker := NewProgressTracker()
	srv := httptest.NewServer(makeProgressMux(tracker))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/admin/progress/stream", nil)
	req.SetBasicAuth("admin", "secret")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("expected Cache-Control: no-cache, got %q", resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("expected X-Accel-Buffering: no, got %q", resp.Header.Get("X-Accel-Buffering"))
	}
}

func TestProgressStreamNoProgress(t *testing.T) {
	tracker := NewProgressTracker()
	srv := httptest.NewServer(makeProgressMux(tracker))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/admin/progress/stream", nil)
	req.SetBasicAuth("admin", "secret")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			var snapshots []ProgressSnapshot
			if err := json.Unmarshal([]byte(payload), &snapshots); err != nil {
				t.Fatalf("failed to parse SSE data as JSON array: %v — payload: %q", err, payload)
			}
			if len(snapshots) != 0 {
				t.Errorf("expected empty array, got %d snapshots", len(snapshots))
			}
			return
		}
	}
	t.Fatal("no SSE data event received")
}

func TestProgressStreamWithProgress(t *testing.T) {
	tracker := NewProgressTracker()
	tracker.Start("app1", "job123", "v1.0.0")
	tracker.Update("app1", 512, 1024, 100.0)

	srv := httptest.NewServer(makeProgressMux(tracker))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/admin/progress/stream", nil)
	req.SetBasicAuth("admin", "secret")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			var snapshots []ProgressSnapshot
			if err := json.Unmarshal([]byte(payload), &snapshots); err != nil {
				t.Fatalf("failed to parse SSE data: %v — payload: %q", err, payload)
			}
			if len(snapshots) == 0 {
				t.Fatal("expected at least one snapshot, got empty array")
			}
			if snapshots[0].AppID != "app1" {
				t.Errorf("expected app_id=app1, got %q", snapshots[0].AppID)
			}
			if snapshots[0].JobID != "job123" {
				t.Errorf("expected job_id=job123, got %q", snapshots[0].JobID)
			}
			return
		}
	}
	t.Fatal("no SSE data event received")
}

func TestProgressStreamRequiresAuth(t *testing.T) {
	tracker := NewProgressTracker()
	mux := makeProgressMux(tracker)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/progress/stream", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}
