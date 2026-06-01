package cancel

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"
)

func TestCancelServicePendingJobIdempotent(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := enqueueJob(t, q, "app1", "v1")
	svc := NewCancelService(q)

	first, err := svc.RequestJobCancel(jobID)
	if err != nil {
		t.Fatalf("RequestJobCancel first: %v", err)
	}
	if first.Outcome != CancelOutcomePendingCanceled || first.Status != JobStatusCanceled {
		t.Fatalf("first cancel = %+v, want pending canceled", first)
	}
	assertJobStatus(t, q, jobID, JobStatusCanceled)

	second, err := svc.RequestJobCancel(jobID)
	if err != nil {
		t.Fatalf("RequestJobCancel second: %v", err)
	}
	if second.Outcome != CancelOutcomeTerminalNoop || second.Status != JobStatusCanceled {
		t.Fatalf("second cancel = %+v, want terminal no-op", second)
	}

	requested, err := svc.IsCancelRequested(jobID)
	if err != nil {
		t.Fatalf("IsCancelRequested: %v", err)
	}
	if !requested {
		t.Fatal("IsCancelRequested = false, want true for canceled job")
	}
}

func TestCancelServiceActiveJobIdempotent(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v1")
	svc := NewCancelService(q)

	first, err := svc.RequestJobCancel(jobID)
	if err != nil {
		t.Fatalf("RequestJobCancel first: %v", err)
	}
	if first.Outcome != CancelOutcomeActiveRequested || first.Status != JobStatusCancelRequested {
		t.Fatalf("first cancel = %+v, want active cancel requested", first)
	}
	assertJobStatus(t, q, jobID, JobStatusCancelRequested)

	second, err := svc.RequestJobCancel(jobID)
	if err != nil {
		t.Fatalf("RequestJobCancel second: %v", err)
	}
	if second.Outcome != CancelOutcomeActiveRequested || second.Status != JobStatusCancelRequested {
		t.Fatalf("second cancel = %+v, want repeated active request", second)
	}
}

func TestCancelServiceUnknownJobNoop(t *testing.T) {
	q := newTestQueue(t, 10)
	svc := NewCancelService(q)

	result, err := svc.RequestJobCancel("missing-job")
	if err != nil {
		t.Fatalf("RequestJobCancel: %v", err)
	}
	if result.Outcome != CancelOutcomeUnknownJob {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, CancelOutcomeUnknownJob)
	}

	decision, err := svc.Checkpoint("missing-job", DeployPhaseDownloading)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if decision.Action != CancelActionNoop {
		t.Fatalf("Action = %q, want noop", decision.Action)
	}
}

func TestCancelServiceTerminalJobNoop(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v1")
	if err := q.MarkDone(jobID, true, "", nil); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	svc := NewCancelService(q)

	result, err := svc.RequestJobCancel(jobID)
	if err != nil {
		t.Fatalf("RequestJobCancel: %v", err)
	}
	if result.Outcome != CancelOutcomeTerminalNoop || result.Status != JobStatusSucceeded {
		t.Fatalf("cancel terminal = %+v, want terminal no-op", result)
	}
	assertJobStatus(t, q, jobID, JobStatusSucceeded)

	decision, err := svc.Checkpoint(jobID, DeployPhaseHealthcheck)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if decision.Action != CancelActionNoop || decision.TerminalStatus != JobStatusSucceeded {
		t.Fatalf("terminal checkpoint = %+v, want no-op succeeded", decision)
	}
}

func TestCancelServiceCheckpointSafeStopContract(t *testing.T) {
	tests := []struct {
		name            string
		phase           DeployPhase
		wantAction      CancelAction
		wantCleanup     bool
		wantRollback    bool
		wantAbort       bool
		wantTerminal    string
		wantFinalStatus string
	}{
		{
			name:            "downloading aborts and cleans temp file",
			phase:           DeployPhaseDownloading,
			wantAction:      CancelActionAbortDownloadCleanupTemp,
			wantCleanup:     true,
			wantAbort:       true,
			wantFinalStatus: JobStatusCancelRequested,
		},
		{
			name:            "downloaded before validation cleans temp file",
			phase:           DeployPhaseDownloaded,
			wantAction:      CancelActionCleanupTemp,
			wantCleanup:     true,
			wantFinalStatus: JobStatusCancelRequested,
		},
		{
			name:            "backup complete restores backup",
			phase:           DeployPhaseBackupComplete,
			wantAction:      CancelActionRestoreBackup,
			wantRollback:    true,
			wantFinalStatus: JobStatusCancelRequested,
		},
		{
			name:            "installing rolls back and cleans up",
			phase:           DeployPhaseInstalling,
			wantAction:      CancelActionRollbackCleanup,
			wantCleanup:     true,
			wantRollback:    true,
			wantFinalStatus: JobStatusCancelRequested,
		},
		{
			name:            "cleanup complete marks canceled",
			phase:           DeployPhaseCleanupComplete,
			wantAction:      CancelActionMarkCanceled,
			wantTerminal:    JobStatusCanceled,
			wantFinalStatus: JobStatusCanceled,
		},
		{
			name:            "rollback complete marks canceled",
			phase:           DeployPhaseRollbackComplete,
			wantAction:      CancelActionMarkCanceled,
			wantTerminal:    JobStatusCanceled,
			wantFinalStatus: JobStatusCanceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newTestQueue(t, 10)
			jobID := dequeueJob(t, q, "app1", "v1")
			svc := NewCancelService(q)
			if _, err := svc.RequestJobCancel(jobID); err != nil {
				t.Fatalf("RequestJobCancel: %v", err)
			}

			decision, err := svc.Checkpoint(jobID, tt.phase)
			if err != nil {
				t.Fatalf("Checkpoint: %v", err)
			}
			if decision.Action != tt.wantAction {
				t.Errorf("Action = %q, want %q", decision.Action, tt.wantAction)
			}
			if decision.RequiresCleanup != tt.wantCleanup {
				t.Errorf("RequiresCleanup = %v, want %v", decision.RequiresCleanup, tt.wantCleanup)
			}
			if decision.RequiresRollback != tt.wantRollback {
				t.Errorf("RequiresRollback = %v, want %v", decision.RequiresRollback, tt.wantRollback)
			}
			if decision.AbortDownload != tt.wantAbort {
				t.Errorf("AbortDownload = %v, want %v", decision.AbortDownload, tt.wantAbort)
			}
			if decision.TerminalStatus != tt.wantTerminal {
				t.Errorf("TerminalStatus = %q, want %q", decision.TerminalStatus, tt.wantTerminal)
			}
			assertJobStatus(t, q, jobID, tt.wantFinalStatus)
		})
	}
}

func TestDeployControlCancelRollbackFailureMarksFailed(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v1")
	control := NewCancelService(q)

	if _, err := control.RequestJobCancel(jobID); err != nil {
		t.Fatalf("RequestJobCancel: %v", err)
	}
	decision, err := control.Checkpoint(jobID, DeployPhaseRollbackFailed)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if decision.Action != CancelActionMarkFailed || decision.TerminalStatus != JobStatusFailed {
		t.Fatalf("decision = %+v, want mark failed", decision)
	}
	assertJobStatus(t, q, jobID, JobStatusFailed)
}

func TestCancelServiceCleanupFailureMarksFailed(t *testing.T) {
	q := newTestQueue(t, 10)
	jobID := dequeueJob(t, q, "app1", "v1")
	svc := NewCancelService(q)

	if _, err := svc.RequestJobCancel(jobID); err != nil {
		t.Fatalf("RequestJobCancel: %v", err)
	}
	decision, err := svc.Checkpoint(jobID, DeployPhaseCleanupFailed)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if decision.Action != CancelActionMarkFailed || decision.TerminalStatus != JobStatusFailed {
		t.Fatalf("decision = %+v, want mark failed", decision)
	}
	assertJobStatus(t, q, jobID, JobStatusFailed)
}

func TestCancelServiceAppCancelAggregatesOutcomes(t *testing.T) {
	q := newTestQueue(t, 10)
	activeID := dequeueJob(t, q, "app1", "v-active")
	terminalID := dequeueJob(t, q, "app1", "v-terminal")
	if err := q.MarkDone(terminalID, true, "", nil); err != nil {
		t.Fatalf("MarkDone terminal: %v", err)
	}
	pendingID := enqueueJob(t, q, "app1", "v-pending")
	alreadyRequestedID := insertJobWithStatus(t, q, "app1", "v-requested", JobStatusCancelRequested)
	unknownStatusID := insertJobWithStatus(t, q, "app1", "v-unknown", "paused")
	otherAppID := enqueueJob(t, q, "app2", "v-other")

	result, err := NewCancelService(q).RequestAppCancel("app1")
	if err != nil {
		t.Fatalf("RequestAppCancel: %v", err)
	}
	if result.Total != 5 || result.Pending != 1 || result.Active != 2 || result.Terminal != 1 || result.Unknown != 1 {
		t.Fatalf("result = %+v, want total=5 pending=1 active=2 terminal=1 unknown=1", result)
	}
	if len(result.Requested) != 5 {
		t.Fatalf("Requested len = %d, want 5", len(result.Requested))
	}
	assertJobStatus(t, q, pendingID, JobStatusCanceled)
	assertJobStatus(t, q, activeID, JobStatusCancelRequested)
	assertJobStatus(t, q, alreadyRequestedID, JobStatusCancelRequested)
	assertJobStatus(t, q, terminalID, JobStatusSucceeded)
	assertJobStatus(t, q, unknownStatusID, "paused")
	assertJobStatus(t, q, otherAppID, JobStatusPending)
}

func enqueueJob(t *testing.T, q *store.DeployQueue, appID, tag string) string {
	t.Helper()
	if err := q.Enqueue(appID, tag); err != nil {
		t.Fatalf("Enqueue(%s, %s): %v", appID, tag, err)
	}
	var id string
	if err := q.DB().QueryRow(`SELECT id FROM deploy_jobs WHERE app_id = ? AND tag = ?`, appID, tag).Scan(&id); err != nil {
		t.Fatalf("query job id: %v", err)
	}
	return id
}

func dequeueJob(t *testing.T, q *store.DeployQueue, appID, tag string) string {
	t.Helper()
	if err := q.Enqueue(appID, tag); err != nil {
		t.Fatalf("Enqueue(%s, %s): %v", appID, tag, err)
	}
	id, gotTag, err := q.DequeueNext(appID)
	if err != nil {
		t.Fatalf("DequeueNext(%s): %v", appID, err)
	}
	if id == "" || gotTag != tag {
		t.Fatalf("DequeueNext = (%q, %q), want non-empty id and tag %q", id, gotTag, tag)
	}
	return id
}

func insertJobWithStatus(t *testing.T, q *store.DeployQueue, appID, tag, status string) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generateJobID: %v", err)
	}
	jobID := hex.EncodeToString(b)
	_, err := q.DB().Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM deploy_jobs), ?, ?, ?, 'test')`,
		jobID, appID, tag, status,
	)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	return jobID
}

func assertJobStatus(t *testing.T, q *store.DeployQueue, jobID, want string) {
	t.Helper()
	var status string
	if err := q.DB().QueryRow(`SELECT status FROM deploy_jobs WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != want {
		t.Fatalf("status = %q, want %q", status, want)
	}
}
