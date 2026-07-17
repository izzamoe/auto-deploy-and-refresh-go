package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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
	// activeCache is an immutable snapshot of every non-terminal deploy job,
	// keyed by app ID then tag (two levels instead of a concatenated key so
	// lookups are allocation-free). It serves IsDuplicate — hit once per
	// webhook request — straight from memory, avoiding a SQLite COUNT query
	// (and its SQL re-parse plus a scan of the app's whole job history) per
	// call. nil means "not loaded"; the snapshot is rebuilt lazily on the next
	// read and dropped by every write to deploy_jobs.
	activeCache atomic.Pointer[map[string]map[string]struct{}]
	// activeCacheMu serializes rebuilds against invalidations so a rebuild
	// that raced a concurrent write can never publish a pre-write snapshot
	// over that write's invalidation (double-checked locking; reads that hit
	// the cache never touch the mutex).
	activeCacheMu sync.Mutex
}

// loadActiveCache returns the cached active-jobs snapshot, building it from
// the database on a cache miss. The returned map must be treated as read-only.
func (q *DeployQueue) loadActiveCache() (map[string]map[string]struct{}, error) {
	if m := q.activeCache.Load(); m != nil {
		return *m, nil
	}
	q.activeCacheMu.Lock()
	defer q.activeCacheMu.Unlock()
	if m := q.activeCache.Load(); m != nil {
		return *m, nil
	}
	rows, err := q.db.Query(
		`SELECT app_id, tag FROM deploy_jobs WHERE status IN ('pending', 'in_progress', 'cancel_requested')`,
	)
	if err != nil {
		return nil, fmt.Errorf("deploy_queue: load active cache: %w", err)
	}
	defer rows.Close()

	m := make(map[string]map[string]struct{})
	for rows.Next() {
		var appID, tag string
		if err := rows.Scan(&appID, &tag); err != nil {
			return nil, fmt.Errorf("deploy_queue: load active cache: %w", err)
		}
		tags := m[appID]
		if tags == nil {
			tags = make(map[string]struct{})
			m[appID] = tags
		}
		tags[tag] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deploy_queue: load active cache: %w", err)
	}
	q.activeCache.Store(&m)
	return m, nil
}

// InvalidateActiveJobsCache drops the cached snapshot so the next IsDuplicate
// rebuilds it. Every write to deploy_jobs must call it — including writers
// outside this type (the cancel service's status transitions and
// AppStore.Delete's job cleanup).
func (q *DeployQueue) InvalidateActiveJobsCache() {
	q.activeCacheMu.Lock()
	q.activeCache.Store(nil)
	q.activeCacheMu.Unlock()
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
		`ALTER TABLE deploy_jobs ADD COLUMN deploy_log TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := q.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("deploy_queue: migrate deploy_jobs: %w", err)
		}
	}
	if _, err := q.db.Exec(`CREATE INDEX IF NOT EXISTS idx_deploy_jobs_app_id ON deploy_jobs(app_id)`); err != nil {
		return fmt.Errorf("deploy_queue: create index: %w", err)
	}

	// A partial UNIQUE index makes the database the final arbiter of "one active
	// job per (app_id, tag)". IsDuplicate's check-then-insert has a race window
	// (two concurrent webhooks can both pass the check, then both insert); this
	// index turns the loser's INSERT into a UNIQUE violation that Enqueue maps
	// back to ErrDuplicate. Terminal jobs (succeeded/failed/canceled) fall out
	// of the WHERE clause, so redeploying a tag later stays allowed.
	//
	// Creating the index fails if the table already holds duplicate active rows
	// (from before this guard existed), so demote the extras first: keep the
	// earliest-enqueued job per (app_id, tag) and mark the rest failed. This
	// runs at startup before RecoverStale, when no deploy is actually executing.
	if _, err := q.db.Exec(
		`UPDATE deploy_jobs SET status = 'failed',
		        error_msg = 'superseded: duplicate active job removed during migration',
		        updated_at = CURRENT_TIMESTAMP
		 WHERE id IN (
		     SELECT id FROM (
		         SELECT id, ROW_NUMBER() OVER (
		             PARTITION BY app_id, tag ORDER BY seq ASC
		         ) AS rn
		         FROM deploy_jobs
		         WHERE status IN ('pending', 'in_progress', 'cancel_requested')
		     ) WHERE rn > 1
		 )`,
	); err != nil {
		return fmt.Errorf("deploy_queue: dedup active jobs: %w", err)
	}
	if _, err := q.db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_deploy_jobs_active_unique
		 ON deploy_jobs(app_id, tag)
		 WHERE status IN ('pending', 'in_progress', 'cancel_requested')`,
	); err != nil {
		return fmt.Errorf("deploy_queue: create active-unique index: %w", err)
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

	q.InvalidateActiveJobsCache()
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
	q.InvalidateActiveJobsCache()
	// The partial UNIQUE index is the backstop for the check-then-insert race:
	// a concurrent Enqueue that also passed IsDuplicate loses here.
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
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
	q.InvalidateActiveJobsCache()
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
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
	q.InvalidateActiveJobsCache()
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
	q.InvalidateActiveJobsCache()
	return err
}

// SaveJobLog stores captured service logs (e.g. journalctl output) for a job,
// so a failed deploy's health-check logs can be reviewed later. It is a
// best-effort UPDATE keyed by job id; unknown ids are a no-op.
func (q *DeployQueue) SaveJobLog(id, log string) error {
	_, err := q.db.Exec(
		`UPDATE deploy_jobs SET deploy_log = ? WHERE id = ?`,
		log, id,
	)
	if err != nil {
		return fmt.Errorf("deploy_queue: save job log: %w", err)
	}
	q.InvalidateActiveJobsCache()
	return nil
}

// GetJobLog returns the stored service log for a job, or an empty string if the
// job has none. A missing job returns ("", nil) so callers need not special
// case it.
func (q *DeployQueue) GetJobLog(id string) (string, error) {
	var log string
	err := q.db.QueryRow(`SELECT COALESCE(deploy_log, '') FROM deploy_jobs WHERE id = ?`, id).Scan(&log)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("deploy_queue: get job log: %w", err)
	}
	return log, nil
}

func (q *DeployQueue) RecoverStale() error {
	_, err := q.db.Exec(
		`UPDATE deploy_jobs SET status = 'failed', error_msg = 'recovered: process crashed', updated_at = CURRENT_TIMESTAMP WHERE status IN ('in_progress', 'cancel_requested')`,
	)
	q.InvalidateActiveJobsCache()
	return err
}

func (q *DeployQueue) IsDuplicate(appID, tag string) (bool, error) {
	m, err := q.loadActiveCache()
	if err != nil {
		return false, err
	}
	_, ok := m[appID][tag]
	return ok, nil
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

// ListHistoryPaged returns one page of history for appID, ordered newest
// first, skipping offset rows and returning at most limit rows.
func (q *DeployQueue) ListHistoryPaged(appID string, limit, offset int) ([]JobRecord, error) {
	rows, err := q.db.Query(
		`SELECT id, app_id, tag, status, trigger_type, COALESCE(retry_of_job_id, ''), COALESCE(error_msg, ''), download_bytes, download_duration_ms, download_speed_bps, created_at, updated_at
		 FROM deploy_jobs WHERE app_id = ? ORDER BY seq DESC LIMIT ? OFFSET ?`,
		appID, limit, offset,
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

// CountHistory returns the total number of history rows for appID.
func (q *DeployQueue) CountHistory(appID string) (int, error) {
	var total int
	err := q.db.QueryRow(`SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ?`, appID).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}

// ListAllHistoryPaged returns one page of history across every app, ordered
// newest first globally (by seq), skipping offset rows and returning at most
// limit rows.
func (q *DeployQueue) ListAllHistoryPaged(limit, offset int) ([]JobRecord, error) {
	rows, err := q.db.Query(
		`SELECT id, app_id, tag, status, trigger_type, COALESCE(retry_of_job_id, ''), COALESCE(error_msg, ''), download_bytes, download_duration_ms, download_speed_bps, created_at, updated_at
		 FROM deploy_jobs ORDER BY seq DESC LIMIT ? OFFSET ?`,
		limit, offset,
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

// CountAllHistory returns the total number of history rows across every app.
func (q *DeployQueue) CountAllHistory() (int, error) {
	var total int
	err := q.db.QueryRow(`SELECT COUNT(*) FROM deploy_jobs`).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
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
	q.InvalidateActiveJobsCache()
	if isUniqueViolation(err) {
		return "", ErrDuplicate
	}
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
