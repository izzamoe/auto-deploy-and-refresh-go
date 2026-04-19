package main

import (
	"bufio"
	"bytes"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func makeProgressMux(t *testing.T, tracker *ProgressTracker) *http.ServeMux {
	t.Helper()
	tmpls := make(map[string]*template.Template)
	for _, page := range []string{"apps_list.html", "history.html"} {
		tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/"+page)
		if err != nil {
			t.Fatalf("parse %s: %v", page, err)
		}
		tmpls[page] = tmpl
	}
	store := newTestAppStoreWithJobs(t)
	queue, err := NewDeployQueue(store.db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	mux := http.NewServeMux()
	handler := NewProgressAdminHandler(tracker, store, queue, tmpls)
	middleware := BasicAuthMiddleware("admin", "secret")
	RegisterAdminProgressRoutes(mux, handler, middleware)
	return mux
}

func TestProgressStreamSSEHeaders(t *testing.T) {
	tracker := NewProgressTracker()
	srv := httptest.NewServer(makeProgressMux(t, tracker))
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
	srv := httptest.NewServer(makeProgressMux(t, tracker))
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
		if strings.HasPrefix(line, "event: ") {
			if got := strings.TrimPrefix(line, "event: "); got != "progress" {
				t.Fatalf("expected progress event, got %q", got)
			}
		}
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if !strings.Contains(payload, "<div") {
				t.Fatalf("expected HTML fragment payload, got %q", payload)
			}
			return
		}
	}
	t.Fatal("no SSE data event received")
}

func TestProgressStreamWithProgress(t *testing.T) {
	tracker := NewProgressTracker()
	store := newTestAppStoreWithJobs(t)
	app, err := store.Create("Progress App", "secret-1", "/tmp/bin", "progress.service", "example/progress", "progress")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	queue, err := NewDeployQueue(store.db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES ('job123', 1, ?, 'v1.0.0', 'in_progress', 'webhook')`, app.ID); err != nil {
		t.Fatalf("insert job: %v", err)
	}

	tmpls := make(map[string]*template.Template)
	for _, page := range []string{"apps_list.html", "history.html"} {
		tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/"+page)
		if err != nil {
			t.Fatalf("parse %s: %v", page, err)
		}
		tmpls[page] = tmpl
	}

	tracker.Start(app.ID, "job123", "v1.0.0")
	tracker.Update(app.ID, 512, 1024, 100.0)

	mux := http.NewServeMux()
	handler := NewProgressAdminHandler(tracker, store, queue, tmpls)
	RegisterAdminProgressRoutes(mux, handler, BasicAuthMiddleware("admin", "secret"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/admin/progress/stream?app_id="+app.ID+"&job_id=job123", nil)
	req.SetBasicAuth("admin", "secret")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	eventName, payload := readFirstSSEEvent(t, resp.Body)
	if eventName != "progress" {
		t.Fatalf("expected named progress event, got %q", eventName)
	}
	if !strings.Contains(payload, "app-card-") {
		t.Fatalf("expected app card fragment in payload, got %q", payload)
	}
	if !strings.Contains(payload, "hx-swap-oob=\"outerHTML\"") {
		t.Fatalf("expected OOB swap in payload, got %q", payload)
	}
}

func TestProgressStreamSettlesTerminalAppCardAndSubscription(t *testing.T) {
	tracker := NewProgressTracker()
	store := newTestAppStoreWithJobs(t)
	app, err := store.Create("Settled App", "secret-2", "/tmp/bin-2", "settled.service", "example/settled", "settled")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	queue, err := NewDeployQueue(store.db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, download_bytes, download_speed_bps) VALUES ('job-terminal', 1, ?, 'v1.0.1', 'succeeded', 'webhook', 2048, 512.0)`, app.ID); err != nil {
		t.Fatalf("insert terminal job: %v", err)
	}

	tmpls := make(map[string]*template.Template)
	for _, page := range []string{"apps_list.html", "history.html"} {
		tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/"+page)
		if err != nil {
			t.Fatalf("parse %s: %v", page, err)
		}
		tmpls[page] = tmpl
	}

	tracker.Start(app.ID, "job-terminal", "v1.0.1")
	tracker.Update(app.ID, 1024, 2048, 128)
	tracker.Finish(app.ID)

	handler := NewProgressAdminHandler(tracker, store, queue, tmpls)
	req := httptest.NewRequest("GET", "/admin/progress/stream?app_id="+app.ID+"&job_id=job-terminal", nil)
	payload, err := handler.renderStreamPayload(req)
	if err != nil {
		t.Fatalf("renderStreamPayload: %v", err)
	}
	if !strings.Contains(payload, `id="app-card-`+app.ID+`"`) {
		t.Fatalf("expected full app card OOB payload, got %s", payload)
	}
	if strings.Contains(payload, `data-progress-job="job-terminal"`) {
		t.Fatalf("expected terminal app card to clear stale data-progress-job, got %s", payload)
	}
	if !strings.Contains(payload, `status-succeeded`) {
		t.Fatalf("expected terminal succeeded state in payload, got %s", payload)
	}
	if strings.Contains(payload, `sse-connect=`) {
		t.Fatalf("expected subscription OOB payload to disconnect when nothing is active, got %s", payload)
	}
	if strings.Contains(payload, `data-progress-percent`) {
		t.Fatalf("expected terminal app card to render final stats instead of active progress chips, got %s", payload)
	}
	if strings.Contains(payload, `data-progress-speed`) {
		t.Fatalf("expected terminal app card to clear active speed chip markers, got %s", payload)
	}
	if !strings.Contains(payload, `Speed: 512 B/s`) {
		t.Fatalf("expected final summary stats in payload, got %s", payload)
	}
}

func TestProgressStreamSettlesTerminalHistoryRow(t *testing.T) {
	tracker := NewProgressTracker()
	store := newTestAppStoreWithJobs(t)
	app, err := store.Create("History Settled App", "secret-3", "/tmp/bin-3", "history-settled.service", "example/history", "history")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	queue, err := NewDeployQueue(store.db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, download_bytes, download_speed_bps) VALUES ('job-history-terminal', 1, ?, 'v2.0.0', 'failed', 'webhook', 4096, 256.0)`, app.ID); err != nil {
		t.Fatalf("insert terminal history job: %v", err)
	}

	tmpls := make(map[string]*template.Template)
	for _, page := range []string{"apps_list.html", "history.html"} {
		tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/"+page)
		if err != nil {
			t.Fatalf("parse %s: %v", page, err)
		}
		tmpls[page] = tmpl
	}

	tracker.Start(app.ID, "job-history-terminal", "v2.0.0")
	tracker.Update(app.ID, 2048, 4096, 64)
	tracker.Fail(app.ID)

	handler := NewProgressAdminHandler(tracker, store, queue, tmpls)
	req := httptest.NewRequest("GET", "/admin/progress/stream?app_id="+app.ID+"&job_id=job-history-terminal", nil)
	payload, err := handler.renderStreamPayload(req)
	if err != nil {
		t.Fatalf("renderStreamPayload: %v", err)
	}
	if !strings.Contains(payload, `id="history-row-job-history-terminal"`) {
		t.Fatalf("expected terminal history row OOB payload, got %s", payload)
	}
	if !strings.Contains(payload, `data-status="failed"`) {
		t.Fatalf("expected failed row status in payload, got %s", payload)
	}
	if strings.Contains(payload, `data-progress-percent`) || strings.Contains(payload, `data-progress-speed`) {
		t.Fatalf("expected terminal history row to drop active progress markers, got %s", payload)
	}
	if strings.Contains(payload, `sse-connect=`) {
		t.Fatalf("expected subscription OOB payload to disconnect when history jobs are terminal, got %s", payload)
	}
}

func TestProgressStreamRequiresAuth(t *testing.T) {
	tracker := NewProgressTracker()
	mux := makeProgressMux(t, tracker)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/progress/stream", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="auto-deploy admin"` {
		t.Errorf("expected WWW-Authenticate header, got %q", got)
	}
}

func TestProgressStreamHTMXRequiresAuth(t *testing.T) {
	tracker := NewProgressTracker()
	mux := makeProgressMux(t, tracker)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/progress/stream", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="auto-deploy admin"` {
		t.Fatalf("expected WWW-Authenticate header, got %q", got)
	}
}

func readFirstSSEEvent(t *testing.T, body io.ReadCloser) (string, string) {
	t.Helper()
	defer body.Close()

	scanner := bufio.NewScanner(body)
	var eventName string
	var data bytes.Buffer
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if eventName != "" || data.Len() > 0 {
				return eventName, strings.TrimSuffix(data.String(), "\n")
			}
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			eventName = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data.WriteString(strings.TrimPrefix(line, "data: "))
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("failed to read SSE event: %v", err)
	}
	t.Fatal("no SSE data event received")
	return "", ""
}
