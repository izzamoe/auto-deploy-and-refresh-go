package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrDuplicate = errors.New("tag already pending or in progress")
var ErrQueueFull = errors.New("deploy queue is full")
var ErrInvalidTag = errors.New("invalid tag")

// validateTag guards the release tag before it is interpolated into the GitHub
// download URL (https://github.com/{repo}/releases/download/{tag}/{artifact}).
// It rejects path-traversal and URL-metacharacter sequences that could redirect
// the download to an unintended repository or asset.
func validateTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("%w: empty", ErrInvalidTag)
	}
	if len(tag) > 128 {
		return fmt.Errorf("%w: too long", ErrInvalidTag)
	}
	if strings.Contains(tag, "..") {
		return fmt.Errorf("%w: contains %q", ErrInvalidTag, "..")
	}
	for _, r := range tag {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == '/':
		default:
			return fmt.Errorf("%w: illegal character %q", ErrInvalidTag, r)
		}
	}
	if tag[0] == '/' || tag[len(tag)-1] == '/' {
		return fmt.Errorf("%w: leading or trailing slash", ErrInvalidTag)
	}
	return nil
}

type JobRecord struct {
	ID                 string
	AppID              string
	Tag                string
	Status             string
	Trigger            string
	RetryOfJobID       string
	ErrorMsg           string
	DownloadBytes      int64
	DownloadDurationMs int64
	DownloadSpeedBPS   float64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type DownloadSummary struct {
	Bytes      int64
	DurationMs int64
	SpeedBPS   float64
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
	return &DeployQueue{db: db, maxPending: maxPending}, nil
}

func (q *DeployQueue) Migrate() error {
	if _, err := q.db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return fmt.Errorf("deploy_queue: set WAL: %w", err)
	}
	if _, err := q.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("deploy_queue: set busy_timeout: %w", err)
	}

	var legacyExists bool
	err := q.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='deploy_queue'`).Scan(&legacyExists)
	if err != nil {
		return fmt.Errorf("deploy_queue: check legacy table: %w", err)
	}

	var newExists bool
	err = q.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='deploy_jobs'`).Scan(&newExists)
	if err != nil {
		return fmt.Errorf("deploy_queue: check new table: %w", err)
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
		download_bytes INTEGER NOT NULL DEFAULT 0,
		download_duration_ms INTEGER NOT NULL DEFAULT 0,
		download_speed_bps REAL NOT NULL DEFAULT 0,
		created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
	)`
	if _, err := q.db.Exec(schema); err != nil {
		return fmt.Errorf("deploy_queue: create table: %w", err)
	}
	for _, stmt := range []string{
		`ALTER TABLE deploy_jobs ADD COLUMN download_bytes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE deploy_jobs ADD COLUMN download_duration_ms INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE deploy_jobs ADD COLUMN download_speed_bps REAL NOT NULL DEFAULT 0`,
	} {
		if _, err := q.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("deploy_queue: migrate deploy_jobs: %w", err)
		}
	}
	if _, err := q.db.Exec(`CREATE INDEX IF NOT EXISTS idx_deploy_jobs_app_id ON deploy_jobs(app_id)`); err != nil {
		return fmt.Errorf("deploy_queue: create index: %w", err)
	}

	if legacyExists && !newExists {
		rows, err := q.db.Query(`SELECT tag, status, COALESCE(error_msg, ''), created_at, updated_at FROM deploy_queue ORDER BY id ASC`)
		if err != nil {
			return fmt.Errorf("deploy_queue: read legacy rows: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var tag, status, errMsg string
			var createdAt, updatedAt time.Time
			if err := rows.Scan(&tag, &status, &errMsg, &createdAt, &updatedAt); err != nil {
				return fmt.Errorf("deploy_queue: scan legacy row: %w", err)
			}
			jobID, err := generateJobID()
			if err != nil {
				return err
			}
			var nullErrMsg *string
			if errMsg != "" {
				nullErrMsg = &errMsg
			}
			_, err = q.db.Exec(
				`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, error_msg, created_at, updated_at) VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM deploy_jobs), 'legacy', ?, ?, 'webhook', ?, ?, ?)`,
				jobID, tag, status, nullErrMsg, createdAt, updatedAt,
			)
			if err != nil {
				return fmt.Errorf("deploy_queue: backfill legacy row: %w", err)
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("deploy_queue: iterate legacy rows: %w", err)
		}
	}

	return nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJobRecord reads a full deploy_jobs row (in the column order used by
// ListHistory/GetJob/ListByStatus) into a JobRecord.
func scanJobRecord(sc rowScanner) (JobRecord, error) {
	var item JobRecord
	err := sc.Scan(&item.ID, &item.AppID, &item.Tag, &item.Status, &item.Trigger, &item.RetryOfJobID, &item.ErrorMsg, &item.DownloadBytes, &item.DownloadDurationMs, &item.DownloadSpeedBPS, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func (q *DeployQueue) DB() *sql.DB {
	return q.db
}

func (q *DeployQueue) Close() error {
	return q.db.Close()
}

func (q *DeployQueue) Enqueue(appID, tag string) error {
	if err := validateTag(tag); err != nil {
		return err
	}
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
	if err := validateTag(tag); err != nil {
		return err
	}
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

func (q *DeployQueue) MarkDone(id string, success bool, errMsg string, summary *DownloadSummary) error {
	status := "succeeded"
	if !success {
		status = "failed"
	}

	var nullMsg *string
	if errMsg != "" {
		nullMsg = &errMsg
	}

	var downloadBytes, downloadDurationMs int64
	var downloadSpeedBPS float64
	if summary != nil {
		downloadBytes = summary.Bytes
		downloadDurationMs = summary.DurationMs
		downloadSpeedBPS = summary.SpeedBPS
	}

	_, err := q.db.Exec(
		`UPDATE deploy_jobs SET status = ?, error_msg = ?, download_bytes = ?, download_duration_ms = ?, download_speed_bps = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status IN ('in_progress', 'cancel_requested')`,
		status, nullMsg, downloadBytes, downloadDurationMs, downloadSpeedBPS, id,
	)
	return err
}

func (q *DeployQueue) RecoverStale() error {
	_, err := q.db.Exec(
		`UPDATE deploy_jobs SET status = 'failed', error_msg = 'recovered: process crashed', updated_at = CURRENT_TIMESTAMP WHERE status IN ('in_progress', 'cancel_requested')`,
	)
	return err
}

func (q *DeployQueue) IsDuplicate(appID, tag string) (bool, error) {
	var count int
	err := q.db.QueryRow(
		`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ? AND tag = ? AND status IN ('pending', 'in_progress', 'cancel_requested')`,
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
		`SELECT id, app_id, tag, status, trigger_type, COALESCE(retry_of_job_id, ''), COALESCE(error_msg, ''), download_bytes, download_duration_ms, download_speed_bps, created_at, updated_at
		 FROM deploy_jobs WHERE app_id = ? ORDER BY seq DESC LIMIT ?`,
		appID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []JobRecord
	for rows.Next() {
		item, err := scanJobRecord(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (q *DeployQueue) CreateRetryJob(originalJobID, appID, tag string) (string, error) {
	if err := validateTag(tag); err != nil {
		return "", err
	}
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
	item, err := scanJobRecord(q.db.QueryRow(
		`SELECT id, app_id, tag, status, trigger_type, COALESCE(retry_of_job_id, ''), COALESCE(error_msg, ''), download_bytes, download_duration_ms, download_speed_bps, created_at, updated_at
		 FROM deploy_jobs WHERE id = ?`, jobID,
	))
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// ListByStatus returns jobs matching the given status, ordered by creation time ascending.
// Kept for backward compatibility with worker_test.go.
func (q *DeployQueue) ListByStatus(status string) ([]JobRecord, error) {
	rows, err := q.db.Query(
		`SELECT id, app_id, tag, status, trigger_type, COALESCE(retry_of_job_id, ''), COALESCE(error_msg, ''), download_bytes, download_duration_ms, download_speed_bps, created_at, updated_at
		 FROM deploy_jobs WHERE status = ? ORDER BY seq ASC`,
		status,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []JobRecord
	for rows.Next() {
		item, err := scanJobRecord(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
