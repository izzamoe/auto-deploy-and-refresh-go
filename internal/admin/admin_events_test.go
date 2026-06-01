package admin

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/progress"
)

func TestAdminEventsLowFrequencyEventTypeConstants(t *testing.T) {
	tests := map[string]string{
		"hello":            adminEventTypeHello,
		"snapshot":         adminEventTypeSnapshot,
		"job_status":       adminEventTypeJobStatus,
		"cancel_requested": adminEventTypeCancelRequested,
		"notice":           adminEventTypeNotice,
		"heartbeat":        adminEventTypeHeartbeat,
	}

	for want, got := range tests {
		if got != want {
			t.Fatalf("admin event type constant = %q, want %q", got, want)
		}
	}
}

func TestProgressEventKnownTotalIncludesPercentAndSpeed(t *testing.T) {
	tracker := progress.NewProgressTracker()
	tracker.Start("app-known", "job-known", "v1.2.3")
	hub := NewAdminEventHub(tracker)
	tracker.SetProgressSink(hub)
	client := hub.addClient()
	defer hub.removeClient(client)

	tracker.Update("app-known", 512, 1024, 128)

	frame := readAdminEventClientProgress(t, client)
	if frame["t"] != adminEventCompactProgress {
		t.Fatalf("event type = %v, want %q", frame["t"], adminEventCompactProgress)
	}
	if frame["a"] != "app-known" || frame["j"] != "job-known" || frame["ph"] != progress.ProgressStageDownloading || frame["st"] != progress.ProgressStatusInProgress {
		t.Fatalf("unexpected progress identity/status: %+v", frame)
	}
	if got := int64(frame["db"].(float64)); got != 512 {
		t.Fatalf("db = %d, want 512", got)
	}
	if got := int64(frame["tb"].(float64)); got != 1024 {
		t.Fatalf("tb = %d, want 1024", got)
	}
	pct := int64(frame["pct"].(float64))
	if pct < 0 || pct > 100 || pct != 50 {
		t.Fatalf("pct = %d, want bounded 50", pct)
	}
	if got := int64(frame["bps"].(float64)); got < 0 {
		t.Fatalf("bps = %d, want nonnegative", got)
	}
}

func TestProgressEventUnknownTotalIsIndeterminate(t *testing.T) {
	tracker := progress.NewProgressTracker()
	tracker.Start("app-unknown", "job-unknown", "v9.9.9")
	hub := NewAdminEventHub(tracker)
	tracker.SetProgressSink(hub)
	client := hub.addClient()
	defer hub.removeClient(client)

	tracker.Update("app-unknown", 2048, -1, 256)

	frame := readAdminEventClientProgress(t, client)
	if _, ok := frame["pct"]; ok {
		t.Fatalf("unknown total progress must omit pct: %+v", frame)
	}
	if _, ok := frame["tb"]; ok {
		t.Fatalf("unknown total progress must omit tb instead of fabricating a total: %+v", frame)
	}
	if got := int64(frame["db"].(float64)); got != 2048 {
		t.Fatalf("db = %d, want 2048", got)
	}
	if got := int64(frame["bps"].(float64)); got < 0 {
		t.Fatalf("bps = %d, want nonnegative", got)
	}
}

func TestProgressTrackerUpdateDoesNotBlockWithoutClients(t *testing.T) {
	tracker := progress.NewProgressTracker()
	tracker.Start("app-no-clients", "job-no-clients", "v1")
	hub := NewAdminEventHub(tracker)
	tracker.SetProgressSink(hub)

	done := make(chan struct{})
	go func() {
		tracker.Update("app-no-clients", 10, 100, 5)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("progress update blocked with no event clients")
	}
}

func TestProgressTrackerUpdateDropsSlowEventClient(t *testing.T) {
	tracker := progress.NewProgressTracker()
	tracker.Start("app-slow", "job-slow", "v1")
	hub := NewAdminEventHub(tracker)
	tracker.SetProgressSink(hub)
	client := hub.addClient()
	for i := 0; i < adminEventClientBuffer; i++ {
		client.send <- []byte(`{"queued":true}`)
	}

	done := make(chan struct{})
	go func() {
		tracker.Update("app-slow", 20, 100, 10)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("progress update blocked on slow event client")
	}

	hub.mu.Lock()
	_, stillRegistered := hub.clients[client]
	hub.mu.Unlock()
	if stillRegistered {
		t.Fatal("slow progress client should be dropped from hub")
	}
	for range client.send {
	}
}

func TestAdminEventHubSlowClientDoesNotBlockBroadcast(t *testing.T) {
	hub := NewAdminEventHub(progress.NewProgressTracker())
	client := hub.addClient()
	for i := 0; i < adminEventClientBuffer; i++ {
		client.send <- []byte(`{"queued":true}`)
	}

	done := make(chan struct{})
	go func() {
		hub.Broadcast(AdminEvent{Type: adminEventTypeNotice, Payload: map[string]string{"message": "must not block"}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Broadcast blocked on slow client")
	}

	hub.mu.Lock()
	_, stillRegistered := hub.clients[client]
	hub.mu.Unlock()
	if stillRegistered {
		t.Fatal("slow client should be dropped from hub")
	}
	for range client.send {
	}
}

func readAdminEventClientProgress(t *testing.T, client *adminEventClient) map[string]any {
	t.Helper()
	select {
	case payload, ok := <-client.send:
		if !ok {
			t.Fatal("progress client channel closed before event")
		}
		var frame map[string]any
		if err := json.Unmarshal(payload, &frame); err != nil {
			t.Fatalf("decode progress event %s: %v", payload, err)
		}
		return frame
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for progress event")
	}
	return nil
}
