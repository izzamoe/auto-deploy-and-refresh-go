package cancel

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/izzamoe/auto-deploy/internal/store"
)

const (
	JobStatusPending         = "pending"
	JobStatusInProgress      = "in_progress"
	JobStatusCancelRequested = "cancel_requested"
	JobStatusSucceeded       = "succeeded"
	JobStatusFailed          = "failed"
	JobStatusCanceled        = "canceled"
)

type DeployPhase string

const (
	DeployPhasePending            DeployPhase = "pending"
	DeployPhaseDownloading        DeployPhase = "downloading"
	DeployPhaseDownloaded         DeployPhase = "downloaded"
	DeployPhaseValidating         DeployPhase = "validating"
	DeployPhaseBackingUp          DeployPhase = "backing_up"
	DeployPhaseBackupComplete     DeployPhase = "backup_complete"
	DeployPhaseInstalling         DeployPhase = "installing"
	DeployPhaseRestarting         DeployPhase = "restarting"
	DeployPhaseHealthcheck        DeployPhase = "healthcheck"
	DeployPhaseRollback           DeployPhase = "rollback"
	DeployPhaseRollbackComplete   DeployPhase = "rollback_complete"
	DeployPhaseRollbackFailed     DeployPhase = "rollback_failed"
	DeployPhaseCleanup            DeployPhase = "cleanup"
	DeployPhaseCleanupComplete    DeployPhase = "cleanup_complete"
	DeployPhaseCleanupFailed      DeployPhase = "cleanup_failed"
	DeployPhaseSystemConsistent   DeployPhase = "system_consistent"
	DeployPhaseSystemInconsistent DeployPhase = "system_inconsistent"
	DeployPhaseSucceeded          DeployPhase = "succeeded"
	DeployPhaseFailed             DeployPhase = "failed"
	DeployPhaseCanceled           DeployPhase = "canceled"
)

type CancelOutcome string

const (
	CancelOutcomePendingCanceled CancelOutcome = "pending_canceled"
	CancelOutcomeActiveRequested CancelOutcome = "active_cancel_requested"
	CancelOutcomeTerminalNoop    CancelOutcome = "terminal_noop"
	CancelOutcomeUnknownJob      CancelOutcome = "unknown_job"
)

type CancelAction string

const (
	CancelActionContinue                 CancelAction = "continue"
	CancelActionNoop                     CancelAction = "noop"
	CancelActionMarkCanceled             CancelAction = "mark_canceled"
	CancelActionMarkFailed               CancelAction = "mark_failed"
	CancelActionAbortDownloadCleanupTemp CancelAction = "abort_download_cleanup_temp"
	CancelActionCleanupTemp              CancelAction = "cleanup_temp"
	CancelActionRestoreBackup            CancelAction = "restore_backup"
	CancelActionRollbackCleanup          CancelAction = "rollback_cleanup"
)

type CancelEvent struct {
	Type    string
	AppID   string
	JobID   string
	Payload map[string]any
}

type EventSink interface {
	BroadcastCancelEvent(CancelEvent)
}

const (
	CancelEventTypeCancelRequested = "cancel_requested"
	CancelEventTypeJobStatus       = "job_status"
)

type CancelJobResult struct {
	JobID         string
	AppID         string
	PreviousState string
	Status        string
	Outcome       CancelOutcome
	Message       string
}

type CancelAppResult struct {
	AppID           string
	Total           int
	Pending         int
	Active          int
	Terminal        int
	Unknown         int
	PendingCanceled int
	ActiveSignaled  int
	AlreadyTerminal int
	Requested       []CancelJobResult
}

type CancelCheckpointDecision struct {
	JobID            string
	Phase            DeployPhase
	Status           string
	CancelRequested  bool
	Action           CancelAction
	TerminalStatus   string
	RequiresCleanup  bool
	RequiresRollback bool
	AbortDownload    bool
	Message          string
}

type CancelService struct {
	q      *store.DeployQueue
	events EventSink
}

func NewCancelService(q *store.DeployQueue) *CancelService {
	return &CancelService{q: q}
}

func (s *CancelService) SetEventSink(events EventSink) {
	if s != nil {
		s.events = events
	}
}

func (s *CancelService) RequestJobCancel(jobID string) (CancelJobResult, error) {
	if s == nil || s.q == nil || s.q.DB() == nil {
		return CancelJobResult{}, errors.New("cancel service is not initialized")
	}

	tx, err := s.q.DB().Begin()
	if err != nil {
		return CancelJobResult{}, err
	}
	defer tx.Rollback()

	job, err := getJobForCancel(tx, jobID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CancelJobResult{JobID: jobID, Outcome: CancelOutcomeUnknownJob, Message: "job not found"}, nil
		}
		return CancelJobResult{}, err
	}

	result := CancelJobResult{JobID: job.ID, AppID: job.AppID, PreviousState: job.Status, Status: job.Status}
	switch job.Status {
	case JobStatusPending:
		if err := setCancelStatus(tx, job.ID, JobStatusCanceled, "canceled before dequeue"); err != nil {
			return CancelJobResult{}, err
		}
		result.Status = JobStatusCanceled
		result.Outcome = CancelOutcomePendingCanceled
		result.Message = "pending job canceled before dequeue"
	case JobStatusInProgress:
		if err := setCancelStatus(tx, job.ID, JobStatusCancelRequested, "cancel requested"); err != nil {
			return CancelJobResult{}, err
		}
		result.Status = JobStatusCancelRequested
		result.Outcome = CancelOutcomeActiveRequested
		result.Message = "active job will stop at the next safe checkpoint"
	case JobStatusCancelRequested:
		result.Outcome = CancelOutcomeActiveRequested
		result.Message = "cancel already requested"
	case JobStatusSucceeded, JobStatusFailed, JobStatusCanceled:
		result.Outcome = CancelOutcomeTerminalNoop
		result.Message = "terminal job unchanged"
	default:
		result.Outcome = CancelOutcomeUnknownJob
		result.Message = fmt.Sprintf("unknown job status %q", job.Status)
	}

	if err := tx.Commit(); err != nil {
		return CancelJobResult{}, err
	}
	s.publishCancelResult(result)
	return result, nil
}

func (s *CancelService) RequestAppCancel(appID string) (CancelAppResult, error) {
	if s == nil || s.q == nil || s.q.DB() == nil {
		return CancelAppResult{}, errors.New("cancel service is not initialized")
	}

	tx, err := s.q.DB().Begin()
	if err != nil {
		return CancelAppResult{}, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id, app_id, status FROM deploy_jobs WHERE app_id = ? ORDER BY seq ASC`, appID)
	if err != nil {
		return CancelAppResult{}, err
	}
	defer rows.Close()

	result := CancelAppResult{AppID: appID}
	for rows.Next() {
		var job store.JobRecord
		if err := rows.Scan(&job.ID, &job.AppID, &job.Status); err != nil {
			return CancelAppResult{}, err
		}
		result.Total++
		jobResult := CancelJobResult{JobID: job.ID, AppID: job.AppID, PreviousState: job.Status, Status: job.Status}
		switch job.Status {
		case JobStatusPending:
			if err := setCancelStatus(tx, job.ID, JobStatusCanceled, "canceled before dequeue"); err != nil {
				return CancelAppResult{}, err
			}
			result.Pending++
			result.PendingCanceled++
			jobResult.Status = JobStatusCanceled
			jobResult.Outcome = CancelOutcomePendingCanceled
			jobResult.Message = "pending job canceled before dequeue"
		case JobStatusInProgress:
			if err := setCancelStatus(tx, job.ID, JobStatusCancelRequested, "cancel requested"); err != nil {
				return CancelAppResult{}, err
			}
			result.Active++
			result.ActiveSignaled++
			jobResult.Status = JobStatusCancelRequested
			jobResult.Outcome = CancelOutcomeActiveRequested
			jobResult.Message = "active job will stop at the next safe checkpoint"
		case JobStatusCancelRequested:
			result.Active++
			result.ActiveSignaled++
			jobResult.Outcome = CancelOutcomeActiveRequested
			jobResult.Message = "cancel already requested"
		case JobStatusSucceeded, JobStatusFailed, JobStatusCanceled:
			result.Terminal++
			result.AlreadyTerminal++
			jobResult.Outcome = CancelOutcomeTerminalNoop
			jobResult.Message = "terminal job unchanged"
		default:
			result.Unknown++
			jobResult.Outcome = CancelOutcomeUnknownJob
			jobResult.Message = fmt.Sprintf("unknown job status %q", job.Status)
		}
		result.Requested = append(result.Requested, jobResult)
	}
	if err := rows.Err(); err != nil {
		return CancelAppResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return CancelAppResult{}, err
	}
	for _, item := range result.Requested {
		s.publishCancelResult(item)
	}
	return result, nil
}

func (s *CancelService) IsCancelRequested(jobID string) (bool, error) {
	if s == nil || s.q == nil || s.q.DB() == nil {
		return false, errors.New("cancel service is not initialized")
	}
	var status string
	err := s.q.DB().QueryRow(`SELECT status FROM deploy_jobs WHERE id = ?`, jobID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return status == JobStatusCancelRequested || status == JobStatusCanceled, nil
}

func (s *CancelService) Checkpoint(jobID string, phase DeployPhase) (CancelCheckpointDecision, error) {
	if s == nil || s.q == nil || s.q.DB() == nil {
		return CancelCheckpointDecision{}, errors.New("cancel service is not initialized")
	}

	var status string
	err := s.q.DB().QueryRow(`SELECT status FROM deploy_jobs WHERE id = ?`, jobID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CancelCheckpointDecision{JobID: jobID, Phase: phase, Action: CancelActionNoop, Message: "job not found"}, nil
		}
		return CancelCheckpointDecision{}, err
	}

	decision := cancelDecisionForStatus(jobID, status, phase)
	if decision.TerminalStatus != "" && status == JobStatusCancelRequested {
		if err := s.markTerminal(jobID, decision.TerminalStatus, decision.Message); err != nil {
			return CancelCheckpointDecision{}, err
		}
		decision.Status = decision.TerminalStatus
		s.publishJobStatus(jobID, decision.TerminalStatus, decision.Message)
	}
	return decision, nil
}

func (s *CancelService) publishCancelResult(result CancelJobResult) {
	if s == nil || s.events == nil || result.Outcome == CancelOutcomeUnknownJob || result.Outcome == CancelOutcomeTerminalNoop {
		return
	}
	s.events.BroadcastCancelEvent(CancelEvent{
		Type:  CancelEventTypeCancelRequested,
		AppID: result.AppID,
		JobID: result.JobID,
		Payload: map[string]any{
			"previousState": result.PreviousState,
			"status":        result.Status,
			"outcome":       result.Outcome,
			"message":       result.Message,
		},
	})
	if result.Status == JobStatusCanceled || result.Status == JobStatusFailed || result.Status == JobStatusSucceeded {
		s.publishJobStatus(result.JobID, result.Status, result.Message)
	}
}

func (s *CancelService) publishJobStatus(jobID, status, message string) {
	if s == nil || s.events == nil {
		return
	}
	s.events.BroadcastCancelEvent(CancelEvent{
		Type:  CancelEventTypeJobStatus,
		JobID: jobID,
		Payload: map[string]any{
			"status":  status,
			"message": message,
		},
	})
}

func (s *CancelService) markTerminal(jobID, status, message string) error {
	_, err := s.q.DB().Exec(
		`UPDATE deploy_jobs SET status = ?, error_msg = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = 'cancel_requested'`,
		status, message, jobID,
	)
	return err
}

func cancelDecisionForStatus(jobID, status string, phase DeployPhase) CancelCheckpointDecision {
	decision := CancelCheckpointDecision{JobID: jobID, Phase: phase, Status: status}
	if isTerminalJobStatus(status) {
		decision.Action = CancelActionNoop
		decision.TerminalStatus = status
		decision.Message = "terminal job unchanged"
		return decision
	}
	if status != JobStatusCancelRequested {
		decision.Action = CancelActionContinue
		decision.Message = "no cancel requested"
		return decision
	}

	decision.CancelRequested = true
	switch phase {
	case DeployPhasePending:
		decision.Action = CancelActionMarkCanceled
		decision.TerminalStatus = JobStatusCanceled
		decision.Message = "pending job can stop before dequeue"
	case DeployPhaseDownloading:
		decision.Action = CancelActionAbortDownloadCleanupTemp
		decision.AbortDownload = true
		decision.RequiresCleanup = true
		decision.Message = "abort download and remove temporary artifact before canceling"
	case DeployPhaseDownloaded, DeployPhaseValidating:
		decision.Action = CancelActionCleanupTemp
		decision.RequiresCleanup = true
		decision.Message = "remove downloaded temporary artifact before canceling"
	case DeployPhaseCleanup, DeployPhaseBackingUp:
		decision.Action = CancelActionCleanupTemp
		decision.RequiresCleanup = true
		decision.Message = "finish cleanup before canceling"
	case DeployPhaseCleanupComplete:
		decision.Action = CancelActionMarkCanceled
		decision.TerminalStatus = JobStatusCanceled
		decision.Message = "cleanup complete; job canceled safely"
	case DeployPhaseCleanupFailed:
		decision.Action = CancelActionMarkFailed
		decision.TerminalStatus = JobStatusFailed
		decision.Message = "cleanup failed during cancel"
	case DeployPhaseBackupComplete:
		decision.Action = CancelActionRestoreBackup
		decision.RequiresRollback = true
		decision.Message = "restore backup before canceling"
	case DeployPhaseInstalling, DeployPhaseRestarting, DeployPhaseHealthcheck, DeployPhaseRollback:
		decision.Action = CancelActionRollbackCleanup
		decision.RequiresRollback = true
		decision.RequiresCleanup = true
		decision.Message = "rollback and cleanup before canceling"
	case DeployPhaseRollbackComplete, DeployPhaseSystemConsistent:
		decision.Action = CancelActionMarkCanceled
		decision.TerminalStatus = JobStatusCanceled
		decision.Message = "system consistent after rollback; job canceled"
	case DeployPhaseRollbackFailed, DeployPhaseSystemInconsistent:
		decision.Action = CancelActionMarkFailed
		decision.TerminalStatus = JobStatusFailed
		decision.Message = "rollback failed or left system inconsistent during cancel"
	case DeployPhaseSucceeded, DeployPhaseFailed, DeployPhaseCanceled:
		decision.Action = CancelActionNoop
		decision.Message = "deploy already reached a terminal phase"
	default:
		decision.Action = CancelActionContinue
		decision.Message = "unknown phase; continue until a safe checkpoint"
	}
	return decision
}

func getJobForCancel(tx *sql.Tx, jobID string) (store.JobRecord, error) {
	var job store.JobRecord
	err := tx.QueryRow(`SELECT id, app_id, status FROM deploy_jobs WHERE id = ?`, jobID).Scan(&job.ID, &job.AppID, &job.Status)
	return job, err
}

func setCancelStatus(tx *sql.Tx, jobID, status, message string) error {
	_, err := tx.Exec(
		`UPDATE deploy_jobs SET status = ?, error_msg = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, message, jobID,
	)
	return err
}

func isTerminalJobStatus(status string) bool {
	switch status {
	case JobStatusSucceeded, JobStatusFailed, JobStatusCanceled:
		return true
	default:
		return false
	}
}
