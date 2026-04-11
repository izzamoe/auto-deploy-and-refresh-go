package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var ErrDuplicate = errors.New("tag already pending or in progress")
var ErrQueueFull = errors.New("deploy queue is full")

type JobRecord struct {
	ID           string
	AppID        string
	Tag          string
	Status       string
	Trigger      string
	RetryOfJobID string
	ErrorMsg     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type DeployQueue struct {
	db         *sql.DB
	maxPending int
}

func generateJobID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func NewDeployQueue(db *sql.DB, maxPending int) (*DeployQueue, error) {
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, fmt.Errorf("deploy_queue: set WAL: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return nil, fmt.Errorf("deploy_queue: set busy_timeout: %w", err)
	}

	var legacyExists bool
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='deploy_queue'`).Scan(&legacyExists)
	if err != nil {
		return nil, fmt.Errorf("deploy_queue: check legacy table: %w", err)
	}

	var newExists bool
	err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='deploy_jobs'`).Scan(&newExists)
	if err != nil {
		return nil, fmt.Errorf("deploy_queue: check new table: %w", err)
	}

	schema := `CREATE TABLE IF NOT EXISTS deploy_jobs (
		id              TEXT PRIMARY KEY,
		seq             INTEGER NOT NULL,
		app_id          TEXT NOT NULL,
		tag             TEXT NOT NULL,
		status          TEXT NOT NULL DEFAULT 'pending',
		trigger_type    TEXT NOT NULL DEFAULT 'webhook',
		retry_of_job_id TEXT,
		error_msg       TEXT,
		created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("deploy_queue: create table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_deploy_jobs_app_id ON deploy_jobs(app_id)`); err != nil {
		return nil, fmt.Errorf("deploy_queue: create index: %w", err)
	}

	if legacyExists && !newExists {
		rows, err := db.Query(`SELECT tag, status, COALESCE(error_msg, ''), created_at, updated_at FROM deploy_queue ORDER BY id ASC`)
		if err != nil {
			return nil, fmt.Errorf("deploy_queue: read legacy rows: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var tag, status, errMsg string
			var createdAt, updatedAt time.Time
			if err := rows.Scan(&tag, &status, &errMsg, &createdAt, &updatedAt); err != nil {
				return nil, fmt.Errorf("deploy_queue: scan legacy row: %w", err)
			}
			jobID, err := generateJobID()
			if err != nil {
				return nil, err
			}
			var nullErrMsg *string
			if errMsg != "" {
				nullErrMsg = &errMsg
			}
			_, err = db.Exec(
				`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, error_msg, created_at, updated_at) VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM deploy_jobs), 'legacy', ?, ?, 'webhook', ?, ?, ?)`,
				jobID, tag, status, nullErrMsg, createdAt, updatedAt,
			)
			if err != nil {
				return nil, fmt.Errorf("deploy_queue: backfill legacy row: %w", err)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("deploy_queue: iterate legacy rows: %w", err)
		}
	}

	return &DeployQueue{db: db, maxPending: maxPending}, nil
}

func (q *DeployQueue) Close() error {
	return q.db.Close()
}

func (q *DeployQueue) Enqueue(appID, tag string) error {
	dup, err := q.IsDuplicate(appID, tag)
	if err != nil {
		return err
	}
	if dup {
		return ErrDuplicate
	}

	count, err := q.PendingCount(appID)
	if err != nil {
		return err
	}
	if count >= q.maxPending {
		return ErrQueueFull
	}

	jobID, err := generateJobID()
	if err != nil {
		return err
	}

	_, err = q.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM deploy_jobs), ?, ?, 'pending', 'webhook')`,
		jobID, appID, tag,
	)
	return err
}

func (q *DeployQueue) EnqueueManual(appID, tag string) error {
	dup, err := q.IsDuplicate(appID, tag)
	if err != nil {
		return err
	}
	if dup {
		return ErrDuplicate
	}

	count, err := q.PendingCount(appID)
	if err != nil {
		return err
	}
	if count >= q.maxPending {
		return ErrQueueFull
	}

	jobID, err := generateJobID()
	if err != nil {
		return err
	}

	_, err = q.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type) VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM deploy_jobs), ?, ?, 'pending', 'manual_deploy')`,
		jobID, appID, tag,
	)
	return err
}

func (q *DeployQueue) DequeueNext(appID string) (id, tag string, err error) {
	tx, err := q.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()

	row := tx.QueryRow(
		`SELECT id, tag FROM deploy_jobs WHERE app_id = ? AND status = 'pending' ORDER BY seq ASC LIMIT 1`,
		appID,
	)
	if err := row.Scan(&id, &tag); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", nil
		}
		return "", "", err
	}

	_, err = tx.Exec(
		`UPDATE deploy_jobs SET status = 'in_progress', updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		id,
	)
	if err != nil {
		return "", "", err
	}

	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return id, tag, nil
}

func (q *DeployQueue) MarkDone(id string, success bool, errMsg string) error {
	status := "succeeded"
	if !success {
		status = "failed"
	}

	var nullMsg *string
	if errMsg != "" {
		nullMsg = &errMsg
	}

	_, err := q.db.Exec(
		`UPDATE deploy_jobs SET status = ?, error_msg = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, nullMsg, id,
	)
	return err
}

func (q *DeployQueue) RecoverStale() error {
	_, err := q.db.Exec(
		`UPDATE deploy_jobs SET status = 'failed', error_msg = 'recovered: process crashed', updated_at = CURRENT_TIMESTAMP WHERE status = 'in_progress'`,
	)
	return err
}

func (q *DeployQueue) IsDuplicate(appID, tag string) (bool, error) {
	var count int
	err := q.db.QueryRow(
		`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ? AND tag = ? AND status IN ('pending', 'in_progress')`,
		appID, tag,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (q *DeployQueue) PendingCount(appID string) (int, error) {
	var count int
	err := q.db.QueryRow(
		`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ? AND status = 'pending'`,
		appID,
	).Scan(&count)
	return count, err
}

func (q *DeployQueue) ListHistory(appID string, limit int) ([]JobRecord, error) {
	rows, err := q.db.Query(
		`SELECT id, app_id, tag, status, trigger_type, COALESCE(retry_of_job_id, ''), COALESCE(error_msg, ''), created_at, updated_at
		 FROM deploy_jobs WHERE app_id = ? ORDER BY seq DESC LIMIT ?`,
		appID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []JobRecord
	for rows.Next() {
		var item JobRecord
		if err := rows.Scan(&item.ID, &item.AppID, &item.Tag, &item.Status, &item.Trigger, &item.RetryOfJobID, &item.ErrorMsg, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (q *DeployQueue) CreateRetryJob(originalJobID, appID, tag string) (string, error) {
	var exists int
	err := q.db.QueryRow(`SELECT COUNT(*) FROM deploy_jobs WHERE id = ?`, originalJobID).Scan(&exists)
	if err != nil {
		return "", fmt.Errorf("create retry job: query original: %w", err)
	}
	if exists == 0 {
		return "", fmt.Errorf("create retry job: original job %q not found", originalJobID)
	}

	dup, err := q.IsDuplicate(appID, tag)
	if err != nil {
		return "", err
	}
	if dup {
		return "", ErrDuplicate
	}

	jobID, err := generateJobID()
	if err != nil {
		return "", err
	}

	_, err = q.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, retry_of_job_id) VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM deploy_jobs), ?, ?, 'pending', 'manual_retry', ?)`,
		jobID, appID, tag, originalJobID,
	)
	if err != nil {
		return "", fmt.Errorf("create retry job: insert: %w", err)
	}
	return jobID, nil
}

func (q *DeployQueue) GetJob(jobID string) (*JobRecord, error) {
	var item JobRecord
	err := q.db.QueryRow(
		`SELECT id, app_id, tag, status, trigger_type, COALESCE(retry_of_job_id, ''), COALESCE(error_msg, ''), created_at, updated_at
		 FROM deploy_jobs WHERE id = ?`, jobID,
	).Scan(&item.ID, &item.AppID, &item.Tag, &item.Status, &item.Trigger, &item.RetryOfJobID, &item.ErrorMsg, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// ListByStatus returns jobs matching the given status, ordered by creation time ascending.
// Kept for backward compatibility with worker_test.go.
func (q *DeployQueue) ListByStatus(status string) ([]JobRecord, error) {
	rows, err := q.db.Query(
		`SELECT id, app_id, tag, status, trigger_type, COALESCE(retry_of_job_id, ''), COALESCE(error_msg, ''), created_at, updated_at
		 FROM deploy_jobs WHERE status = ? ORDER BY seq ASC`,
		status,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []JobRecord
	for rows.Next() {
		var item JobRecord
		if err := rows.Scan(&item.ID, &item.AppID, &item.Tag, &item.Status, &item.Trigger, &item.RetryOfJobID, &item.ErrorMsg, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
