package store

import (
	"fmt"
	"strings"
	"testing"
)

// B25 / FR-050 / Q-16: a job's stored log is capped at 256 KiB (the tail is kept, with a
// marker) and only the newest 200 finished jobs per app are retained.

func newRetentionQueue(t *testing.T) *DeployQueue {
	t.Helper()
	q, err := NewDeployQueue(newTestDB(t), 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	return q
}

func insertJob(t *testing.T, q *DeployQueue, seq int, id, app, status string) {
	t.Helper()
	if _, err := q.DB().Exec(`INSERT INTO deploy_jobs (seq, id, app_id, tag, status) VALUES (?, ?, ?, ?, ?)`, seq, id, app, fmt.Sprintf("v%d", seq), status); err != nil {
		t.Fatal(err)
	}
}

func TestLogCap_OneMiBLogIsNotStoredWhole(t *testing.T) {
	q := newRetentionQueue(t)
	insertJob(t, q, 1, "j1", "a", "failed")
	big := strings.Repeat("0123456789abcdef\n", (1<<20)/17+1)[:1<<20]
	if err := q.SaveJobLog("j1", big); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetJobLog("j1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > MaxJobLogBytes+256 {
		t.Fatalf("B25: a %d byte log was stored with %d bytes; the cap is %d", len(big), len(got), MaxJobLogBytes)
	}
	if !IsTruncatedJobLog(got) {
		t.Error("a truncated log must say so")
	}
	if !strings.HasSuffix(got, big[len(big)-1000:]) {
		t.Error("the TAIL of the log (the end of the deploy) must be what is kept")
	}
}

func TestLogCap_SmallLogIsStoredUnchanged(t *testing.T) {
	q := newRetentionQueue(t)
	insertJob(t, q, 1, "j1", "a", "failed")
	if err := q.SaveJobLog("j1", "line one\nline two\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetJobLog("j1")
	if got != "line one\nline two\n" || IsTruncatedJobLog(got) {
		t.Errorf("log = %q", got)
	}
}

func TestLogCap_NeverSplitsAMultibyteCharacter(t *testing.T) {
	q := newRetentionQueue(t)
	insertJob(t, q, 1, "j1", "a", "failed")
	if err := q.SaveJobLog("j1", strings.Repeat("é", MaxJobLogBytes)); err != nil { // 2 bytes each
		t.Fatal(err)
	}
	got, _ := q.GetJobLog("j1")
	body := got[strings.Index(got, "\n")+1:]
	if strings.ContainsRune(body, '�') {
		t.Error("the cut landed inside a UTF-8 sequence")
	}
}

func TestRetention_KeepsNewest200FinishedJobsPerApp(t *testing.T) {
	q := newRetentionQueue(t)
	seq := 0
	for i := 0; i < 250; i++ {
		seq++
		insertJob(t, q, seq, fmt.Sprintf("a-%d", i), "a", []string{"succeeded", "failed", "canceled"}[i%3])
	}
	for i := 0; i < 30; i++ {
		seq++
		insertJob(t, q, seq, fmt.Sprintf("b-%d", i), "b", "succeeded")
	}
	seq++
	insertJob(t, q, seq, "a-active", "a", "in_progress")
	seq++
	insertJob(t, q, seq, "a-pending", "a", "pending")

	deleted, err := q.PruneHistory(200)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 50 {
		t.Errorf("deleted = %d, want 50", deleted)
	}
	count := func(where string, args ...any) int {
		var n int
		if err := q.DB().QueryRow(`SELECT COUNT(*) FROM deploy_jobs WHERE `+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`app_id = 'a' AND status IN ('succeeded','failed','canceled')`); n != 200 {
		t.Errorf("app a keeps %d finished jobs, want 200", n)
	}
	if n := count(`id = 'a-0'`); n != 0 {
		t.Error("the OLDEST jobs must be the ones removed")
	}
	if n := count(`id = 'a-249'`); n != 1 {
		t.Error("the newest finished job must survive")
	}
	if n := count(`app_id = 'b'`); n != 30 {
		t.Errorf("app b has %d jobs, want all 30 (under the limit)", n)
	}
	if n := count(`id IN ('a-active', 'a-pending')`); n != 2 {
		t.Error("an active or pending job must never be pruned")
	}
	if again, _ := q.PruneHistory(200); again != 0 {
		t.Errorf("a second prune deleted %d rows, want 0", again)
	}
}
