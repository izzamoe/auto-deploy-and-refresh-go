package store

import (
	"testing"
)

func TestJobLogSaveAndGet(t *testing.T) {
	t.Parallel()
	q := newTestQueue(t, 10)

	if err := q.Enqueue("app1", "v1.0.0"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	id, _, err := q.DequeueNext("app1")
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}

	// No log yet -> empty, no error.
	if got, err := q.GetJobLog(id); err != nil || got != "" {
		t.Fatalf("GetJobLog before save = (%q, %v), want empty", got, err)
	}

	const logText = "journalctl: service crashed\nexit code 1\n"
	if err := q.SaveJobLog(id, logText); err != nil {
		t.Fatalf("SaveJobLog: %v", err)
	}
	got, err := q.GetJobLog(id)
	if err != nil {
		t.Fatalf("GetJobLog: %v", err)
	}
	if got != logText {
		t.Fatalf("GetJobLog = %q, want %q", got, logText)
	}
}

func TestJobLogGetUnknownJob(t *testing.T) {
	t.Parallel()
	q := newTestQueue(t, 10)

	got, err := q.GetJobLog("does-not-exist")
	if err != nil {
		t.Fatalf("GetJobLog unknown = %v, want nil error", err)
	}
	if got != "" {
		t.Fatalf("GetJobLog unknown = %q, want empty", got)
	}
}
