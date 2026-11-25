package store

import "testing"

// FR-045 / Q-14: the artifact hash and verification flag are recorded on the job.
func TestMarkArtifactVerification_RecordsHashAndFlag(t *testing.T) {
	db := newTestDB(t)
	q, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAdminUserStore(db); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAppStore(db); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAppEnvStore(db); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAppArgsStore(db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deploy_jobs (seq, id, app_id, tag, status) VALUES (1, 'j1', 'a1', 'v1', 'succeeded')`); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkArtifactVerification("j1", "abc123", true); err != nil {
		t.Fatal(err)
	}
	var sha string
	var verified int
	if err := db.QueryRow(`SELECT artifact_sha256, artifact_verified FROM deploy_jobs WHERE id = 'j1'`).Scan(&sha, &verified); err != nil {
		t.Fatal(err)
	}
	if sha != "abc123" || verified != 1 {
		t.Errorf("sha=%q verified=%d", sha, verified)
	}
	if err := q.MarkArtifactVerification("j1", "abc123", false); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT artifact_verified FROM deploy_jobs WHERE id = 'j1'`).Scan(&verified)
	if verified != 0 {
		t.Errorf("artifact_verified = %d, want 0 for an unverified artifact", verified)
	}
}
