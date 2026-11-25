package store

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// T051 (FR-026, NFR-001, Q-8): dashboard statistics computed from deploy_jobs.

var (
	utc = time.UTC
	wib = time.FixedZone("WIB", 7*3600) // Asia/Jakarta, no DST
)

// statsNow is a fixed "now": 2026-11-25 10:00:00 UTC = 17:00 WIB the same day.
var statsNow = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

type statsFixture struct {
	db   *sql.DB
	q    *DeployQueue
	apps *AppStore
	seq  int
}

func newStatsFixture(t testing.TB) *statsFixture {
	t.Helper()
	db := newTestDB(t.(*testing.T))
	return buildStatsFixture(t, db)
}

func buildStatsFixture(t testing.TB, db *sql.DB) *statsFixture {
	t.Helper()
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	apps, err := NewAppStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewAdminUserStore(db); err != nil { // store.Migrate alters admin_users
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	return &statsFixture{db: db, q: q, apps: apps}
}

// job inserts a deploy_jobs row with canonical SQLite timestamps (UTC).
func (f *statsFixture) job(t testing.TB, appID, status string, created, updated time.Time, rolledBack int) string {
	t.Helper()
	f.seq++
	id := fmt.Sprintf("job-%06d", f.seq)
	const layout = "2006-01-02 15:04:05"
	if _, err := f.db.Exec(
		`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, created_at, updated_at, rolled_back) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, f.seq, appID, fmt.Sprintf("v%d", f.seq), status, created.UTC().Format(layout), updated.UTC().Format(layout), rolledBack,
	); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *statsFixture) app(t testing.TB, name string, enabled bool) *App {
	t.Helper()
	a, err := f.apps.Create(name, "sec-"+name, "/opt/"+name+"/"+name, name+".service", "o/"+name, name)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		if err := f.apps.SetEnabled(a.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func dayOf(s *Stats, date string) (DailyBucket, bool) {
	for _, d := range s.Daily {
		if d.Date == date {
			return d, true
		}
	}
	return DailyBucket{}, false
}

func TestStats_DailyBucketsDependOnTheTimeZone(t *testing.T) {
	f := newStatsFixture(t)
	a := f.app(t, "a", true)
	// Finished 2026-11-24 20:00 UTC = 2026-11-25 03:00 in Jakarta: across midnight.
	finished := time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)
	f.job(t, a.ID, "succeeded", finished.Add(-time.Minute), finished, 0)

	inUTC, err := f.q.Stats(statsNow, 7, utc)
	if err != nil {
		t.Fatal(err)
	}
	inWIB, err := f.q.Stats(statsNow, 7, wib)
	if err != nil {
		t.Fatal(err)
	}
	if len(inUTC.Daily) != 7 || len(inWIB.Daily) != 7 {
		t.Fatalf("want 7 daily buckets, got %d (UTC) and %d (WIB)", len(inUTC.Daily), len(inWIB.Daily))
	}
	if last := inUTC.Daily[6].Date; last != "2026-11-25" {
		t.Errorf("last UTC bucket = %s, want today 2026-11-25", last)
	}
	if first := inUTC.Daily[0].Date; first != "2026-11-19" {
		t.Errorf("first UTC bucket = %s, want 2026-11-19 (7 days ending today)", first)
	}
	if d, _ := dayOf(inUTC, "2026-11-24"); d.Succeeded != 1 {
		t.Errorf("UTC: 2026-11-24 succeeded = %d, want 1", d.Succeeded)
	}
	if d, _ := dayOf(inUTC, "2026-11-25"); d.Succeeded != 0 {
		t.Errorf("UTC: 2026-11-25 succeeded = %d, want 0", d.Succeeded)
	}
	if d, _ := dayOf(inWIB, "2026-11-24"); d.Succeeded != 0 {
		t.Errorf("WIB: 2026-11-24 succeeded = %d, want 0 (the job finished on the 25th in Jakarta)", d.Succeeded)
	}
	if d, _ := dayOf(inWIB, "2026-11-25"); d.Succeeded != 1 {
		t.Errorf("WIB: 2026-11-25 succeeded = %d, want 1", d.Succeeded)
	}
	if inUTC.TZ != "UTC" || inWIB.TZ != "WIB" {
		t.Errorf("tz names = %q, %q", inUTC.TZ, inWIB.TZ)
	}
}

func TestStats_RateIsNullWhenNothingFinished(t *testing.T) {
	f := newStatsFixture(t)
	a := f.app(t, "a", true)
	empty, err := f.q.Stats(statsNow, 7, utc)
	if err != nil {
		t.Fatal(err)
	}
	if empty.SuccessRate.Rate != nil || empty.SuccessRate.PreviousRate != nil || empty.SuccessRate.Total != 0 {
		t.Errorf("empty db: %+v, want rate=nil previousRate=nil total=0", empty.SuccessRate)
	}
	// only a canceled job and a pending one: still no success/failure to rate
	f.job(t, a.ID, "canceled", statsNow.Add(-time.Hour), statsNow.Add(-time.Hour), 0)
	f.job(t, a.ID, "pending", statsNow.Add(-time.Minute), statsNow.Add(-time.Minute), 0)
	s, _ := f.q.Stats(statsNow, 7, utc)
	if s.SuccessRate.Rate != nil || s.SuccessRate.Total != 0 {
		t.Errorf("canceled/pending jobs must not count towards the rate: %+v", s.SuccessRate)
	}
}

func TestStats_RateAndPreviousPeriod(t *testing.T) {
	f := newStatsFixture(t)
	a := f.app(t, "a", true)
	day := 24 * time.Hour
	// current 7 days: 3 succeeded, 1 failed -> 0.75
	for i := 0; i < 3; i++ {
		f.job(t, a.ID, "succeeded", statsNow.Add(-time.Duration(i+1)*day), statsNow.Add(-time.Duration(i+1)*day), 0)
	}
	f.job(t, a.ID, "failed", statsNow.Add(-2*day), statsNow.Add(-2*day), 0)
	// previous 7 days (days 8..14 back): 1 succeeded, 1 failed -> 0.5
	f.job(t, a.ID, "succeeded", statsNow.Add(-10*day), statsNow.Add(-10*day), 0)
	f.job(t, a.ID, "failed", statsNow.Add(-11*day), statsNow.Add(-11*day), 0)
	// older than both windows: ignored
	f.job(t, a.ID, "failed", statsNow.Add(-40*day), statsNow.Add(-40*day), 0)

	s, err := f.q.Stats(statsNow, 7, utc)
	if err != nil {
		t.Fatal(err)
	}
	if s.SuccessRate.Succeeded != 3 || s.SuccessRate.Total != 4 || s.SuccessRate.Rate == nil || *s.SuccessRate.Rate != 0.75 {
		t.Errorf("rate = %+v (rate %v), want 3/4 = 0.75", s.SuccessRate, s.SuccessRate.Rate)
	}
	if s.SuccessRate.PreviousRate == nil || *s.SuccessRate.PreviousRate != 0.5 {
		t.Errorf("previousRate = %v, want 0.5", s.SuccessRate.PreviousRate)
	}
	s30, _ := f.q.Stats(statsNow, 30, utc)
	if len(s30.Daily) != 30 || s30.SuccessRate.Total != 6 {
		t.Errorf("30d: %d buckets, total %d; want 30 buckets and 6 finished jobs in the window", len(s30.Daily), s30.SuccessRate.Total)
	}
}

func TestStats_Deploys24hHourlyActiveAndApps(t *testing.T) {
	f := newStatsFixture(t)
	a := f.app(t, "a", true)
	f.app(t, "b", false)
	// within the last 24 h
	f.job(t, a.ID, "succeeded", statsNow.Add(-30*time.Minute), statsNow.Add(-30*time.Minute), 0)
	f.job(t, a.ID, "succeeded", statsNow.Add(-90*time.Minute), statsNow.Add(-90*time.Minute), 0)
	f.job(t, a.ID, "failed", statsNow.Add(-5*time.Hour), statsNow.Add(-5*time.Hour), 0)
	f.job(t, a.ID, "canceled", statsNow.Add(-23*time.Hour), statsNow.Add(-23*time.Hour), 0)
	f.job(t, a.ID, "in_progress", statsNow.Add(-time.Minute), statsNow.Add(-time.Minute), 0)
	f.job(t, a.ID, "cancel_requested", statsNow.Add(-2*time.Minute), statsNow.Add(-2*time.Minute), 0)
	f.job(t, a.ID, "pending", statsNow.Add(-3*time.Minute), statsNow.Add(-3*time.Minute), 0)
	// outside 24 h
	f.job(t, a.ID, "succeeded", statsNow.Add(-26*time.Hour), statsNow.Add(-26*time.Hour), 0)

	s, err := f.q.Stats(statsNow, 7, utc)
	if err != nil {
		t.Fatal(err)
	}
	d := s.Deploys24h
	if d.Succeeded != 2 || d.Failed != 1 || d.Canceled != 1 || d.Total != 7 {
		t.Errorf("deploys24h = %+v, want succeeded 2, failed 1, canceled 1, total 7 (3 more are queued/running)", d)
	}
	sum := 0
	for _, n := range d.Hourly {
		sum += n
	}
	if len(d.Hourly) != 24 || sum != d.Total {
		t.Errorf("hourly = %v (sum %d), want 24 slots summing to total %d", d.Hourly, sum, d.Total)
	}
	// slot 23 = the last hour (30 min, 1 min, 2 min, 3 min ago), slot 22 = 90 min ago,
	// slot 19 = 5 h ago, slot 1 = 23 h ago; everything else empty.
	wantHourly := make([]int, 24)
	wantHourly[23], wantHourly[22], wantHourly[19], wantHourly[1] = 4, 1, 1, 1
	for i := range wantHourly {
		if d.Hourly[i] != wantHourly[i] {
			t.Errorf("hourly = %v, want %v", d.Hourly, wantHourly)
			break
		}
	}
	if s.Active.Running != 2 || s.Active.Queued != 1 {
		t.Errorf("active = %+v, want running 2 (in_progress + cancel_requested), queued 1", s.Active)
	}
	if s.Apps.Total != 2 || s.Apps.Enabled != 1 {
		t.Errorf("apps = %+v, want total 2 enabled 1", s.Apps)
	}
}

func TestStats_RollbacksAndRecent(t *testing.T) {
	f := newStatsFixture(t)
	a := f.app(t, "a", true)
	f.job(t, a.ID, "failed", statsNow.Add(-time.Hour), statsNow.Add(-time.Hour), 1)
	f.job(t, a.ID, "failed", statsNow.Add(-2*time.Hour), statsNow.Add(-2*time.Hour), 1)
	f.job(t, a.ID, "failed", statsNow.Add(-3*time.Hour), statsNow.Add(-3*time.Hour), 0)
	f.job(t, a.ID, "failed", statsNow.Add(-40*24*time.Hour), statsNow.Add(-40*24*time.Hour), 1) // outside the window
	for i := 0; i < 12; i++ {
		f.job(t, a.ID, "succeeded", statsNow.Add(-time.Duration(i)*time.Minute), statsNow.Add(-time.Duration(i)*time.Minute), 0)
	}
	s, err := f.q.Stats(statsNow, 7, utc)
	if err != nil {
		t.Fatal(err)
	}
	if s.Rollbacks != 2 {
		t.Errorf("rollbacks = %d, want 2 within the window", s.Rollbacks)
	}
	if len(s.Recent) != 10 {
		t.Fatalf("recent has %d jobs, want 10", len(s.Recent))
	}
	for i := 1; i < len(s.Recent); i++ {
		if s.Recent[i-1].ID <= s.Recent[i].ID {
			t.Errorf("recent must be newest first (by seq): %s before %s", s.Recent[i-1].ID, s.Recent[i].ID)
		}
	}
}

func TestStats_RejectsUnsupportedRange(t *testing.T) {
	f := newStatsFixture(t)
	for _, days := range []int{0, 1, 14, 31, -7} {
		if _, err := f.q.Stats(statsNow, days, utc); err == nil {
			t.Errorf("range of %d days must be rejected", days)
		}
	}
}

// NFR-001: 50 apps and 10,000 jobs spread over 30 days; target < 200 ms per call.
func BenchmarkStats_10kJobs(b *testing.B) {
	db, err := sql.Open("sqlite", b.TempDir()+"/bench.db")
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	b.Cleanup(func() { _ = db.Close() })
	f := buildStatsFixture(b, db)

	var appIDs []string
	for i := 0; i < 50; i++ {
		a, err := f.apps.Create(fmt.Sprintf("app%02d", i), fmt.Sprintf("secret-%02d", i), fmt.Sprintf("/opt/app%02d/app", i), fmt.Sprintf("app%02d.service", i), "o/r", "a")
		if err != nil {
			b.Fatal(err)
		}
		appIDs = append(appIDs, a.ID)
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO deploy_jobs (id, seq, app_id, tag, status, created_at, updated_at, rolled_back) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	statuses := []string{"succeeded", "succeeded", "succeeded", "failed", "canceled"}
	const layout = "2006-01-02 15:04:05"
	for i := 0; i < 10000; i++ {
		ts := statsNow.Add(-time.Duration(i) * 259 * time.Second) // ~30 days
		if _, err := stmt.Exec(fmt.Sprintf("bj-%06d", i), i+1, appIDs[i%50], fmt.Sprintf("v%d", i), statuses[i%5], ts.Format(layout), ts.Format(layout), i%17/16); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, err := f.q.Stats(statsNow, 30, wib)
		if err != nil {
			b.Fatal(err)
		}
		if s.SuccessRate.Total == 0 {
			b.Fatal("empty stats")
		}
	}
}
