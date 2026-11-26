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
	// SHA256 of the downloaded artifact and whether it was checked against a digest
	// published by the release (FR-045, Q-14); empty/false when the deploy failed
	// before verification.
	SHA256   string
	Verified bool
}

type DeployQueue struct {
	onEnqueue  atomic.Pointer[func()] // wakes the coordinator (FR-048)
	observer   atomic.Pointer[jobObserverBox]
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

	// B13/FR-034: backfill legacy rows BEFORE the active-unique index exists (legacy
	// data may hold the same tag active twice; the dedup below demotes the extras and
	// only then is the index created), and never while a rows cursor is open: the
	// database has a single connection (MaxOpenConns(1)), so an Exec issued under an
	// open cursor waits for itself forever.
	if legacyExists && !newExists {
		if err := q.backfillLegacyQueue(); err != nil {
			return err
		}
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

	q.InvalidateActiveJobsCache()
	return nil
}

// legacyQueueRow is one row of the pre-multi-app deploy_queue table.
type legacyQueueRow struct {
	tag, status, errMsg  string
	createdAt, updatedAt time.Time
}

// backfillLegacyQueue copies the legacy deploy_queue rows into deploy_jobs (app
// "legacy") in ONE transaction: all rows or none. The legacy rows are read fully,
// and the cursor closed, before the first write.
func (q *DeployQueue) backfillLegacyQueue() error {
	legacy, err := q.readLegacyQueue()
	if err != nil {
		return err
	}
	tx, err := q.db.Begin()
	if err != nil {
		return fmt.Errorf("deploy_queue: begin legacy backfill: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	for _, row := range legacy {
		jobID, err := generateJobID()
		if err != nil {
			return err
		}
		var nullErrMsg *string
		if row.errMsg != "" {
			msg := row.errMsg
			nullErrMsg = &msg
		}
		if _, err := tx.Exec(
			`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type, error_msg, created_at, updated_at) VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM deploy_jobs), 'legacy', ?, ?, 'webhook', ?, ?, ?)`,
			jobID, row.tag, row.status, nullErrMsg, row.createdAt, row.updatedAt,
		); err != nil {
			return fmt.Errorf("deploy_queue: backfill legacy row: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("deploy_queue: commit legacy backfill: %w", err)
	}
	return nil
}

func (q *DeployQueue) readLegacyQueue() ([]legacyQueueRow, error) {
	rows, err := q.db.Query(`SELECT tag, status, COALESCE(error_msg, ''), created_at, updated_at FROM deploy_queue ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("deploy_queue: read legacy rows: %w", err)
	}
	defer rows.Close()
	var out []legacyQueueRow
	for rows.Next() {
		var r legacyQueueRow
		if err := rows.Scan(&r.tag, &r.status, &r.errMsg, &r.createdAt, &r.updatedAt); err != nil {
			return nil, fmt.Errorf("deploy_queue: scan legacy row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deploy_queue: iterate legacy rows: %w", err)
	}
	return out, nil
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

// Kinds of JobTransition (specs/002 T041).
const (
	JobEnqueued   = "enqueued"    // a new pending job (Enqueue, EnqueueManual, CreateRetryJob)
	JobStarted    = "started"     // pending -> in_progress (DequeueNext)
	JobFinished   = "finished"    // in_progress/cancel_requested -> succeeded/failed (MarkDone)
	JobRolledBack = "rolled_back" // the finished job restored the previous binary (MarkRolledBack)
	JobRecovered  = "recovered"   // a job a dead process left active -> failed (RecoverStale)
	JobsPruned    = "pruned"      // finished jobs beyond the retention were deleted (PruneHistory)
)

// JobTransition is one committed change of deploy_jobs, reported to the JobObserver.
// JobsPruned carries only Count.
type JobTransition struct {
	Kind           string
	JobID          string
	AppID          string
	Tag            string
	Trigger        string
	Status         string
	PreviousStatus string
	Count          int
}

// JobObserver learns about every job transition AFTER it is committed (plan.md section 3:
// the realtime event source of the jobs, summary and stats topics). It is called on the
// goroutine of the writer and must not block.
type JobObserver interface {
	OnJobTransition(JobTransition)
}

type jobObserverBox struct{ o JobObserver }

// SetJobObserver attaches the observer of job transitions (nil detaches it).
func (q *DeployQueue) SetJobObserver(o JobObserver) {
	if o == nil {
		q.observer.Store(nil)
		return
	}
	q.observer.Store(&jobObserverBox{o: o})
}

func (q *DeployQueue) observing() bool { return q.observer.Load() != nil }

func (q *DeployQueue) notify(t JobTransition) {
	if b := q.observer.Load(); b != nil {
		b.o.OnJobTransition(t)
	}
}

// SetEnqueueHook registers fn, called (without holding any lock) after a job was
// inserted, so the coordinator can start it at once instead of polling (FR-048).
func (q *DeployQueue) SetEnqueueHook(fn func()) { q.onEnqueue.Store(&fn) }

func (q *DeployQueue) enqueued() {
	if fn := q.onEnqueue.Load(); fn != nil && *fn != nil {
		(*fn)()
	}
}

func (q *DeployQueue) DB() *sql.DB {
	return q.db
}

func (q *DeployQueue) Close() error {
	return q.db.Close()
}

func (q *DeployQueue) Enqueue(appID, tag string) error {
	return q.enqueue(appID, tag, "webhook")
}

func (q *DeployQueue) EnqueueManual(appID, tag string) error {
	return q.enqueue(appID, tag, "manual_deploy")
}

// enqueue inserts a pending job. The per-app quota is part of the INSERT itself (one
// statement, atomic in SQLite): a count-then-insert in two statements let concurrent
// callers all pass the count (B24, FR-049).
func (q *DeployQueue) enqueue(appID, tag, trigger string) error {
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

	jobID, err := generateJobID()
	if err != nil {
		return err
	}

	res, err := q.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, trigger_type)
		 SELECT ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM deploy_jobs), ?, ?, 'pending', ?
		 WHERE (SELECT COUNT(*) FROM deploy_jobs WHERE app_id = ? AND status = 'pending') < ?`,
		jobID, appID, tag, trigger, appID, q.maxPending,
	)
	q.InvalidateActiveJobsCache()
	// The partial UNIQUE index is the backstop for the check-then-insert race:
	// a concurrent Enqueue that also passed IsDuplicate loses here.
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n == 0 {
		return ErrQueueFull
	}
	// The observer hears about the pending job before the coordinator is woken, so the
	// pending event precedes the in_progress one.
	q.notify(JobTransition{Kind: JobEnqueued, JobID: jobID, AppID: appID, Tag: tag, Trigger: trigger, Status: "pending"})
	q.enqueued()
	return nil
}

func (q *DeployQueue) DequeueNext(appID string) (id, tag string, err error) {
	tx, err := q.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()

	var trigger string
	row := tx.QueryRow(
		`SELECT id, tag, trigger_type FROM deploy_jobs WHERE app_id = ? AND status = 'pending' ORDER BY seq ASC LIMIT 1`,
		appID,
	)
	if err := row.Scan(&id, &tag, &trigger); err != nil {
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
	q.notify(JobTransition{Kind: JobStarted, JobID: id, AppID: appID, Tag: tag, Trigger: trigger, Status: "in_progress", PreviousStatus: "pending"})
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

	res, err := q.db.Exec(
		`UPDATE deploy_jobs SET status = ?, error_msg = ?, download_bytes = ?, download_duration_ms = ?, download_speed_bps = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status IN ('in_progress', 'cancel_requested')`,
		status, nullMsg, downloadBytes, downloadDurationMs, downloadSpeedBPS, id,
	)
	q.InvalidateActiveJobsCache()
	if err != nil {
		return err
	}
	// Only a MarkDone that ended the job is a transition (a job the cancel service already
	// ended is left as it is and was announced there).
	if n, _ := res.RowsAffected(); n > 0 && q.observing() {
		t := JobTransition{Kind: JobFinished, JobID: id, Status: status}
		if job, gerr := q.GetJob(id); gerr == nil && job != nil {
			t.AppID, t.Tag, t.Trigger = job.AppID, job.Tag, job.Trigger
		}
		q.notify(t)
	}
	return nil
}

// MarkArtifactVerification records the artifact hash and whether it was verified
// against a release digest (deploy_jobs.artifact_sha256 / artifact_verified, FR-045).
func (q *DeployQueue) MarkArtifactVerification(id, sha256 string, verified bool) error {
	v := 0
	if verified {
		v = 1
	}
	_, err := q.db.Exec(`UPDATE deploy_jobs SET artifact_sha256 = ?, artifact_verified = ? WHERE id = ?`, sha256, v, id)
	return err
}

// ErrRolledBack is wrapped by the error a deploy returns when it failed AFTER
// installing the new binary and then restored and restarted the previous one
// (B9). The coordinator turns it into deploy_jobs.rolled_back = 1.
var ErrRolledBack = errors.New("rolled back to the previous binary")

// MarkRolledBack flags job id as rolled back (deploy_jobs.rolled_back = 1). It
// is idempotent and a no-op for unknown ids.
func (q *DeployQueue) MarkRolledBack(id string) error {
	res, err := q.db.Exec(`UPDATE deploy_jobs SET rolled_back = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 && q.observing() {
		t := JobTransition{Kind: JobRolledBack, JobID: id}
		if job, gerr := q.GetJob(id); gerr == nil && job != nil {
			t.AppID, t.Tag, t.Trigger, t.Status = job.AppID, job.Tag, job.Trigger, job.Status
		}
		q.notify(t)
	}
	return nil
}

// SaveJobLog stores captured service logs (e.g. journalctl output) for a job,
// so a failed deploy's health-check logs can be reviewed later. It is a
// best-effort UPDATE keyed by job id; unknown ids are a no-op.
func (q *DeployQueue) SaveJobLog(id, log string) error {
	log, _ = CapLogTail(log, MaxJobLogBytes) // FR-050: a job row never holds an unbounded log
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

// ErrSelfDeployInFlight is returned by a deploy of auto-deploy's own service once the
// restart was requested: the process is about to die and the NEXT process settles the
// job (FR-144). The coordinator must leave the job in_progress.
var ErrSelfDeployInFlight = errors.New("self-deploy in flight: the restarted process settles the job")

// RecoverStale fails every job a dead process left in_progress / cancel_requested, so
// none stays active forever (FR-044, NFR-007). It runs at startup, after a
// self-deploy has been settled.
func (q *DeployQueue) RecoverStale() error {
	if !q.observing() {
		_, err := q.db.Exec(
			`UPDATE deploy_jobs SET status = 'failed', error_msg = 'recovered: process crashed', updated_at = CURRENT_TIMESTAMP WHERE status IN ('in_progress', 'cancel_requested')`,
		)
		q.InvalidateActiveJobsCache()
		return err
	}
	// With an observer the recovered jobs are read in the same transaction, so each one is
	// reported after the commit.
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, app_id, tag, status, trigger_type FROM deploy_jobs WHERE status IN ('in_progress', 'cancel_requested') ORDER BY seq ASC`)
	if err != nil {
		return err
	}
	var recovered []JobTransition
	for rows.Next() {
		t := JobTransition{Kind: JobRecovered, Status: "failed"}
		if err := rows.Scan(&t.JobID, &t.AppID, &t.Tag, &t.PreviousStatus, &t.Trigger); err != nil {
			rows.Close()
			return err
		}
		recovered = append(recovered, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if _, err := tx.Exec(
		`UPDATE deploy_jobs SET status = 'failed', error_msg = 'recovered: process crashed', updated_at = CURRENT_TIMESTAMP WHERE status IN ('in_progress', 'cancel_requested')`,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	q.InvalidateActiveJobsCache()
	for _, t := range recovered {
		q.notify(t)
	}
	return nil
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
	q.notify(JobTransition{Kind: JobEnqueued, JobID: jobID, AppID: appID, Tag: tag, Trigger: "manual_retry", Status: "pending"})
	q.enqueued()
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

// PruneHistory deletes, per app, the finished jobs (succeeded, failed, canceled) beyond
// the newest keep (Q-16: 200). Pending and active jobs are never touched. It returns the
// number of rows removed.
func (q *DeployQueue) PruneHistory(keep int) (int, error) {
	if keep < 1 {
		keep = 1
	}
	res, err := q.db.Exec(
		`DELETE FROM deploy_jobs WHERE id IN (
		   SELECT id FROM (
		     SELECT id, ROW_NUMBER() OVER (PARTITION BY app_id ORDER BY seq DESC) AS rn
		     FROM deploy_jobs WHERE status IN ('succeeded', 'failed', 'canceled')
		   ) WHERE rn > ?
		 )`, keep)
	if err != nil {
		return 0, fmt.Errorf("deploy_queue: prune history: %w", err)
	}
	n, err := res.RowsAffected()
	if err == nil && n > 0 {
		q.notify(JobTransition{Kind: JobsPruned, Count: int(n)})
	}
	return int(n), err
}
