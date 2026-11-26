package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Dashboard statistics (FR-026, NFR-001, Q-8). Everything is derived from ONE scan of
// the deploy_jobs rows of the last two windows, then bucketed in Go so that daily
// buckets follow any IANA zone (including DST and 30/45-minute offsets) instead of a
// fixed UTC offset in SQL.
//
// A job's "event time" is when it reached its outcome (updated_at, set by MarkDone and
// the cancel service) for finished jobs, and created_at for jobs that are still
// pending or running.

// ErrInvalidStatsRange is returned for a range other than 7 or 30 days.
var ErrInvalidStatsRange = errors.New("stats range must be 7 or 30 days")

// DailyBucket counts the finished jobs of one local calendar day.
type DailyBucket struct {
	Date      string // YYYY-MM-DD in the requested zone
	Succeeded int
	Failed    int
	Canceled  int
}

// StatsJob is a job of Stats.Recent.
type StatsJob struct {
	JobRecord
	RolledBack bool
}

// Stats is the payload of GET /admin/api/v1/stats.
type Stats struct {
	GeneratedAt time.Time
	RangeDays   int
	TZ          string
	Apps        struct{ Total, Enabled int }
	Deploys24h  struct {
		Total, Succeeded, Failed, Canceled int
		Hourly                             []int // 24 slots, oldest first: jobs per hour
	}
	Active      struct{ Running, Queued int }
	SuccessRate struct {
		Succeeded, Total int
		Rate             *float64 // nil when nothing finished in the window
		PreviousRate     *float64 // the same, for the preceding window of equal length
	}
	Daily     []DailyBucket
	Rollbacks int
	Recent    []StatsJob
}

const statsTimeLayout = "2006-01-02 15:04:05"

// Stats computes the dashboard data as of now for a range of 7 or 30 local days
// ending today in loc.
func (q *DeployQueue) Stats(now time.Time, rangeDays int, loc *time.Location) (*Stats, error) {
	if rangeDays != 7 && rangeDays != 30 {
		return nil, ErrInvalidStatsRange
	}
	if loc == nil {
		loc = time.UTC
	}
	now = now.UTC()
	localNow := now.In(loc)
	// Local midnight of the first day of the current window and of the previous one.
	todayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	winStart := todayStart.AddDate(0, 0, -(rangeDays - 1))
	prevStart := winStart.AddDate(0, 0, -rangeDays)
	dayStart24h := now.Add(-24 * time.Hour)

	scanFrom := prevStart
	if dayStart24h.Before(scanFrom) {
		scanFrom = dayStart24h
	}

	s := &Stats{GeneratedAt: now, RangeDays: rangeDays, TZ: loc.String()}
	s.Deploys24h.Hourly = make([]int, 24)

	// buckets for the window, in order
	s.Daily = make([]DailyBucket, rangeDays)
	index := make(map[string]int, rangeDays)
	for i := 0; i < rangeDays; i++ {
		date := winStart.AddDate(0, 0, i).Format("2006-01-02")
		s.Daily[i] = DailyBucket{Date: date}
		index[date] = i
	}

	rows, err := q.db.Query(
		`SELECT status, created_at, updated_at, rolled_back FROM deploy_jobs WHERE created_at >= ? OR updated_at >= ?`,
		scanFrom.UTC().Format(statsTimeLayout), scanFrom.UTC().Format(statsTimeLayout),
	)
	if err != nil {
		return nil, fmt.Errorf("stats: scan jobs: %w", err)
	}
	var curOK, curFail, prevOK, prevFail int
	for rows.Next() {
		var status string
		var created, updated sql.NullTime
		var rolledBack int
		if err := rows.Scan(&status, &created, &updated, &rolledBack); err != nil {
			rows.Close()
			return nil, fmt.Errorf("stats: scan row: %w", err)
		}
		finished := status == "succeeded" || status == "failed" || status == "canceled"
		ev := created.Time
		if finished && updated.Valid {
			ev = updated.Time
		}
		if !created.Valid && !updated.Valid {
			continue
		}
		ev = ev.UTC()

		// last 24 hours (every status; queued/running count towards the total only)
		if !ev.Before(dayStart24h) && !ev.After(now) {
			s.Deploys24h.Total++
			switch status {
			case "succeeded":
				s.Deploys24h.Succeeded++
			case "failed":
				s.Deploys24h.Failed++
			case "canceled":
				s.Deploys24h.Canceled++
			}
			slot := int(ev.Sub(dayStart24h) / time.Hour)
			if slot > 23 {
				slot = 23
			}
			s.Deploys24h.Hourly[slot]++
		}

		if !finished {
			continue
		}
		local := ev.In(loc)
		switch {
		case !local.Before(winStart) && local.Before(todayStart.AddDate(0, 0, 1)):
			if i, ok := index[local.Format("2006-01-02")]; ok {
				switch status {
				case "succeeded":
					s.Daily[i].Succeeded++
					curOK++
				case "failed":
					s.Daily[i].Failed++
					curFail++
				case "canceled":
					s.Daily[i].Canceled++
				}
			}
			if rolledBack == 1 {
				s.Rollbacks++
			}
		case !local.Before(prevStart) && local.Before(winStart):
			switch status {
			case "succeeded":
				prevOK++
			case "failed":
				prevFail++
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("stats: iterate jobs: %w", err)
	}
	rows.Close()

	s.SuccessRate.Succeeded = curOK
	s.SuccessRate.Total = curOK + curFail
	s.SuccessRate.Rate = ratio(curOK, curOK+curFail)
	s.SuccessRate.PreviousRate = ratio(prevOK, prevOK+prevFail)

	sum, err := q.Summary()
	if err != nil {
		return nil, err
	}
	s.Apps.Total, s.Apps.Enabled = sum.AppsTotal, sum.AppsEnabled
	s.Active.Running, s.Active.Queued = sum.Running, sum.Queued

	recent, err := q.db.Query(
		`SELECT id, app_id, tag, status, trigger_type, COALESCE(retry_of_job_id, ''), COALESCE(error_msg, ''), download_bytes, download_duration_ms, download_speed_bps, created_at, updated_at, rolled_back
		 FROM deploy_jobs ORDER BY seq DESC LIMIT 10`,
	)
	if err != nil {
		return nil, fmt.Errorf("stats: recent jobs: %w", err)
	}
	defer recent.Close()
	for recent.Next() {
		var j StatsJob
		var rb int
		if err := recent.Scan(&j.ID, &j.AppID, &j.Tag, &j.Status, &j.Trigger, &j.RetryOfJobID, &j.ErrorMsg, &j.DownloadBytes, &j.DownloadDurationMs, &j.DownloadSpeedBPS, &j.CreatedAt, &j.UpdatedAt, &rb); err != nil {
			return nil, fmt.Errorf("stats: scan recent job: %w", err)
		}
		j.RolledBack = rb == 1
		s.Recent = append(s.Recent, j)
	}
	if err := recent.Err(); err != nil {
		return nil, fmt.Errorf("stats: iterate recent jobs: %w", err)
	}
	return s, nil
}

// Summary holds the counts of the realtime summary topic (specs/002 FR-002): apps total and
// enabled, jobs running (in_progress or cancel_requested) and queued (pending).
type Summary struct {
	AppsTotal, AppsEnabled, Running, Queued int
}

// Summary computes the apps and active fields of GET /stats. The summary topic uses this
// same function, so the header and the dashboard never disagree.
func (q *DeployQueue) Summary() (Summary, error) {
	var s Summary
	var enabled sql.NullInt64
	if err := q.db.QueryRow(`SELECT COUNT(*), SUM(enabled) FROM apps`).Scan(&s.AppsTotal, &enabled); err != nil {
		return Summary{}, fmt.Errorf("stats: count apps: %w", err)
	}
	s.AppsEnabled = int(enabled.Int64)
	if err := q.db.QueryRow(
		`SELECT COALESCE(SUM(status IN ('in_progress', 'cancel_requested')), 0), COALESCE(SUM(status = 'pending'), 0) FROM deploy_jobs WHERE status IN ('pending', 'in_progress', 'cancel_requested')`,
	).Scan(&s.Running, &s.Queued); err != nil {
		return Summary{}, fmt.Errorf("stats: count active jobs: %w", err)
	}
	return s, nil
}

func ratio(ok, total int) *float64 {
	if total == 0 {
		return nil
	}
	r := float64(ok) / float64(total)
	return &r
}
