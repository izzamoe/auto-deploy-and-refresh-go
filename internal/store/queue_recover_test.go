package store

import "testing"

// FR-044 / NFR-007: after a crash no job stays in_progress / cancel_requested; pending
// and finished jobs are untouched; finished_at is set.
func TestRecoverStale_FailsOnlyOrphanedActiveJobs(t *testing.T) {
	db := newTestDB(t)
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	for i, row := range []struct{ id, status string }{
		{"j-prog", "in_progress"}, {"j-canc", "cancel_requested"}, {"j-pend", "pending"}, {"j-done", "succeeded"},
	} {
		if _, err := db.Exec(`INSERT INTO deploy_jobs (seq, id, app_id, tag, status) VALUES (?, ?, 'a1', ?, ?)`, i+1, row.id, "tag-"+row.id, row.status); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.RecoverStale(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"j-prog": "failed", "j-canc": "failed", "j-pend": "pending", "j-done": "succeeded"}
	for id, status := range want {
		var got string
		if err := db.QueryRow(`SELECT status FROM deploy_jobs WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != status {
			t.Errorf("%s = %q, want %q", id, got, status)
		}
	}
}
