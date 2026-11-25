package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B19 / FR-044 / NFR-007: after a SIGKILL anywhere in a deploy, the next start must
// find the binary path consistent again, quickly. The tests build the on-disk state
// each kill point leaves behind and run the recovery that main() runs at startup.

func writeMarker(t *testing.T, bin, job string, hadBinary bool) {
	t.Helper()
	data, _ := json.Marshal(DeployingMarker{JobID: job, Tag: "v2", HadBinary: hadBinary})
	if err := os.WriteFile(deployingMarkerPath(bin), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Kill between the old "bin -> bin.bak" rename and "tmp -> bin" (the pre-FR-043 window)
// or any state with the binary path missing and a backup present.
func TestRecover_RestoresMissingBinaryFromBackup(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "app")
	if err := os.WriteFile(bin+".bak", elfBinary("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".tmp", elfBinary("half-written"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMarker(t, bin, "job-1", true)

	actions := RecoverInterrupted([]string{bin})
	assertFileBytes(t, bin, elfBinary("old-binary"))
	assertPathMissing(t, deployingMarkerPath(bin))
	assertPathMissing(t, bin+".tmp")
	if len(actions) != 1 || actions[0].Action != "restored_backup" || actions[0].JobID != "job-1" {
		t.Errorf("actions = %+v, want one restored_backup for job-1", actions)
	}
}

// No marker at all (a build from before the marker existed crashed): bin missing +
// bin.bak present is still recovered.
func TestRecover_RestoresWithoutMarker(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(bin+".bak", elfBinary("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	RecoverInterrupted([]string{bin})
	assertFileBytes(t, bin, elfBinary("old-binary"))
}

// Kill after the new binary was installed but before the deploy was confirmed healthy:
// the unconfirmed binary is rolled back to the backup (marker + backup present).
func TestRecover_UnconfirmedInstallRollsBackToBackup(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(bin, elfBinary("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".bak", elfBinary("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMarker(t, bin, "job-2", true)

	RecoverInterrupted([]string{bin})
	assertFileBytes(t, bin, elfBinary("old-binary"))
	assertPathMissing(t, deployingMarkerPath(bin))
}

// First deploy (no previous binary) killed mid-way: nothing to roll back to; the marker
// and the temp file are cleared and an installed binary is left alone.
func TestRecover_FirstDeployMarkerIsCleared(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(bin, elfBinary("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMarker(t, bin, "job-3", false)
	RecoverInterrupted([]string{bin})
	assertFileBytes(t, bin, elfBinary("new-binary"))
	assertPathMissing(t, deployingMarkerPath(bin))
}

// A healthy install (no marker) is never touched, even if a stale backup exists.
func TestRecover_HealthyStateIsUntouched(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(bin, elfBinary("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".bak", elfBinary("previous"), 0o755); err != nil {
		t.Fatal(err)
	}
	if actions := RecoverInterrupted([]string{bin}); len(actions) != 0 {
		t.Errorf("unexpected actions %+v", actions)
	}
	assertFileBytes(t, bin, elfBinary("current"))
	assertFileBytes(t, bin+".bak", elfBinary("previous"))
}

// NFR-007: recovery of 50 apps takes well under 5 s.
func TestRecover_IsFast(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 50; i++ {
		bin := filepath.Join(dir, "app"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		if err := os.WriteFile(bin+".bak", elfBinary("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeMarker(t, bin, "j", true)
		paths = append(paths, bin)
	}
	start := time.Now()
	RecoverInterrupted(paths)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("recovery took %v, want < 5 s (NFR-007)", d)
	}
	for _, p := range paths {
		assertFileBytes(t, p, elfBinary("old"))
	}
}

type fakeResolver struct{ succeeded, rolledBack []string }

func (f *fakeResolver) MarkSelfDeploySucceeded(id string) error {
	f.succeeded = append(f.succeeded, id)
	return nil
}
func (f *fakeResolver) MarkSelfDeployRolledBack(id, msg string) error {
	f.rolledBack = append(f.rolledBack, id)
	return nil
}

// Q-19: the process that finds pending-self-deploy at startup IS the new binary and it
// started: the job is settled as succeeded and the transaction files are cleared.
func TestSelfDeploy_PendingFileMeansNewBinaryStarted(t *testing.T) {
	state := t.TempDir()
	bin := filepath.Join(t.TempDir(), "auto-deploy")
	if err := os.WriteFile(bin, elfBinary("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".bak", elfBinary("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMarker(t, bin, "job-self", true)
	data, _ := json.Marshal(PendingSelfDeploy{JobID: "job-self", BinaryPath: bin, BackupPath: bin + ".bak", ServiceName: "auto-deploy.service"})
	if err := os.WriteFile(PendingSelfDeployPath(state), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var stopped []string
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		stopped = append(stopped, name+" "+strings.Join(args, " "))
		return nil, nil
	})
	r := &fakeResolver{}
	resolved, err := RecoverSelfDeploy(state, r)
	if err != nil || !resolved {
		t.Fatalf("RecoverSelfDeploy = %v, %v", resolved, err)
	}
	if len(stopped) != 1 || stopped[0] != "stop auto-deploy-guard-jobself.timer" {
		t.Errorf("the guard timer must be stopped once the new binary is up; systemctl calls = %v", stopped)
	}
	if len(r.succeeded) != 1 || r.succeeded[0] != "job-self" {
		t.Errorf("succeeded = %v, want [job-self] (no job stays in_progress forever)", r.succeeded)
	}
	assertPathMissing(t, PendingSelfDeployPath(state))
	assertPathMissing(t, deployingMarkerPath(bin))
	assertFileBytes(t, bin, elfBinary("new-binary"))
}

// Q-19: the guard restored the backup and left "<pending>.rolledback": the job is
// settled as failed + rolled back.
func TestSelfDeploy_GuardRollbackIsReported(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(PendingSelfDeployPath(state)+".rolledback", []byte("job-guard\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &fakeResolver{}
	resolved, err := RecoverSelfDeploy(state, r)
	if err != nil || !resolved {
		t.Fatalf("RecoverSelfDeploy = %v, %v", resolved, err)
	}
	if len(r.rolledBack) != 1 || r.rolledBack[0] != "job-guard" {
		t.Errorf("rolledBack = %v, want [job-guard]", r.rolledBack)
	}
	assertPathMissing(t, PendingSelfDeployPath(state)+".rolledback")
}

func TestSelfDeploy_NothingPendingIsANoop(t *testing.T) {
	resolved, err := RecoverSelfDeploy(t.TempDir(), &fakeResolver{})
	if err != nil || resolved {
		t.Errorf("RecoverSelfDeploy on a clean dir = %v, %v", resolved, err)
	}
}
