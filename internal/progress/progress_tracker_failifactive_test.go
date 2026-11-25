package progress

import (
	"testing"
	"time"
)

// B10 (T043): FailIfActive finishes an entry that never reached a terminal
// phase, and leaves finished entries, unknown apps and newer jobs alone.
func TestProgressTrackerFailIfActive(t *testing.T) {
	t.Parallel()
	pt := newProgressTrackerWithClock(time.Minute, time.Now)

	pt.Start("a", "job1", "v1")
	pt.SetPhase("a", ProgressStageInstalling)
	pt.FailIfActive("a", "job1")
	if snap, _ := pt.Snapshot("a"); snap.Phase != ProgressStageFailed {
		t.Errorf("active entry: phase = %q, want %q", snap.Phase, ProgressStageFailed)
	}

	pt.Start("b", "job2", "v1")
	pt.Finish("b")
	pt.FailIfActive("b", "job2")
	if snap, _ := pt.Snapshot("b"); snap.Phase != ProgressStageSucceeded {
		t.Errorf("succeeded entry must stay %q, got %q", ProgressStageSucceeded, snap.Phase)
	}

	pt.Start("c", "job3-new", "v2")
	pt.FailIfActive("c", "job3-old") // a newer deployment replaced the entry
	if snap, _ := pt.Snapshot("c"); snap.Phase != ProgressStageDownloading {
		t.Errorf("entry of another job must be untouched, got phase %q", snap.Phase)
	}

	pt.FailIfActive("missing", "x") // must not panic or create an entry
	if _, ok := pt.Snapshot("missing"); ok {
		t.Error("FailIfActive must not create entries")
	}
}

// A terminal entry created by FailIfActive is removed by Cleanup after the grace window.
func TestProgressTrackerFailIfActiveEntryIsCleanedUp(t *testing.T) {
	t.Parallel()
	now := time.Now()
	pt := newProgressTrackerWithClock(30*time.Second, func() time.Time { return now })
	pt.Start("a", "job1", "v1")
	pt.FailIfActive("a", "job1")
	now = now.Add(31 * time.Second)
	pt.Cleanup()
	if _, ok := pt.Snapshot("a"); ok {
		t.Error("entry must be removed by Cleanup after the grace window")
	}
}
