package cancel

import (
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"
)

// These tests prove the cancel service's out-of-band deploy_jobs writes keep
// DeployQueue's IsDuplicate cache coherent: a job canceled through each write
// path must stop counting as a duplicate immediately.

func mustDup(t *testing.T, q *store.DeployQueue, appID, tag string, want bool) {
	t.Helper()
	dup, err := q.IsDuplicate(appID, tag)
	if err != nil {
		t.Fatalf("IsDuplicate: %v", err)
	}
	if dup != want {
		t.Fatalf("IsDuplicate(%q, %q) = %v, want %v", appID, tag, dup, want)
	}
}

func pendingJobID(t *testing.T, q *store.DeployQueue, appID, tag string) string {
	t.Helper()
	if err := q.Enqueue(appID, tag); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	var id string
	err := q.DB().QueryRow(`SELECT id FROM deploy_jobs WHERE app_id = ? AND tag = ?`, appID, tag).Scan(&id)
	if err != nil {
		t.Fatalf("query job id: %v", err)
	}
	return id
}

func TestRequestJobCancel_InvalidatesDuplicateCache(t *testing.T) {
	q := newTestQueue(t, 10)
	svc := NewCancelService(q)
	jobID := pendingJobID(t, q, "app-1", "v1.0.0")
	mustDup(t, q, "app-1", "v1.0.0", true) // warms cache

	result, err := svc.RequestJobCancel(jobID)
	if err != nil {
		t.Fatalf("RequestJobCancel: %v", err)
	}
	if result.Status != JobStatusCanceled {
		t.Fatalf("status = %q, want %q", result.Status, JobStatusCanceled)
	}
	mustDup(t, q, "app-1", "v1.0.0", false)
}

func TestRequestAppCancel_InvalidatesDuplicateCache(t *testing.T) {
	q := newTestQueue(t, 10)
	svc := NewCancelService(q)
	pendingJobID(t, q, "app-1", "v1.0.0")
	mustDup(t, q, "app-1", "v1.0.0", true)

	if _, err := svc.RequestAppCancel("app-1"); err != nil {
		t.Fatalf("RequestAppCancel: %v", err)
	}
	mustDup(t, q, "app-1", "v1.0.0", false)
}

func TestCheckpointMarkTerminal_InvalidatesDuplicateCache(t *testing.T) {
	q := newTestQueue(t, 10)
	svc := NewCancelService(q)
	pendingJobID(t, q, "app-1", "v1.0.0")
	id, _, err := q.DequeueNext("app-1")
	if err != nil {
		t.Fatalf("DequeueNext: %v", err)
	}

	// in_progress → cancel_requested keeps the job active (still a duplicate).
	if _, err := svc.RequestJobCancel(id); err != nil {
		t.Fatalf("RequestJobCancel: %v", err)
	}
	mustDup(t, q, "app-1", "v1.0.0", true)

	// Checkpoint at a safe phase marks it canceled via markTerminal.
	decision, err := svc.Checkpoint(id, DeployPhaseCleanupComplete)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if decision.Status != JobStatusCanceled {
		t.Fatalf("decision status = %q, want %q", decision.Status, JobStatusCanceled)
	}
	mustDup(t, q, "app-1", "v1.0.0", false)
}
