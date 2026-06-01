package progress

import (
	"sync"
	"testing"
	"time"
)

func TestProgressTrackerStartAndSnapshot(t *testing.T) {
	pt := NewProgressTracker()
	pt.Start("app1", "job1", "v1.0")

	snap, ok := pt.Snapshot("app1")
	if !ok {
		t.Fatal("expected entry after Start")
	}
	if snap.AppID != "app1" {
		t.Errorf("AppID = %q, want app1", snap.AppID)
	}
	if snap.JobID != "job1" {
		t.Errorf("JobID = %q, want job1", snap.JobID)
	}
	if snap.Tag != "v1.0" {
		t.Errorf("Tag = %q, want v1.0", snap.Tag)
	}
	if snap.Phase != ProgressStageDownloading {
		t.Errorf("Phase = %q, want %q", snap.Phase, ProgressStageDownloading)
	}
	if snap.TotalBytes != -1 {
		t.Errorf("TotalBytes = %d, want -1 before any Update", snap.TotalBytes)
	}
	if snap.Percent != -1 {
		t.Errorf("Percent = %v, want -1 before any Update", snap.Percent)
	}
}

func TestProgressTrackerSnapshotMissingApp(t *testing.T) {
	pt := NewProgressTracker()
	_, ok := pt.Snapshot("nonexistent")
	if ok {
		t.Error("expected false for nonexistent app")
	}
}

func TestProgressTrackerUpdate(t *testing.T) {
	pt := NewProgressTracker()
	pt.Start("app1", "job1", "v1")
	pt.Update("app1", 500, 1000, 250.0)

	snap, _ := pt.Snapshot("app1")
	if snap.DownloadedBytes != 500 {
		t.Errorf("DownloadedBytes = %d, want 500", snap.DownloadedBytes)
	}
	if snap.TotalBytes != 1000 {
		t.Errorf("TotalBytes = %d, want 1000", snap.TotalBytes)
	}
	if snap.SpeedBPS != 250.0 {
		t.Errorf("SpeedBPS = %v, want 250", snap.SpeedBPS)
	}
	if snap.Percent != 50.0 {
		t.Errorf("Percent = %v, want 50", snap.Percent)
	}
}

func TestProgressTrackerUnknownTotalBytesNoDiv(t *testing.T) {
	pt := NewProgressTracker()
	pt.Start("app1", "job1", "v1")
	pt.Update("app1", 1024, -1, 100.0)

	snap, _ := pt.Snapshot("app1")
	if snap.Percent != -1 {
		t.Errorf("Percent = %v, want -1 when TotalBytes is -1", snap.Percent)
	}
	if snap.TotalBytes != -1 {
		t.Errorf("TotalBytes = %d, want -1 when total is unknown", snap.TotalBytes)
	}

	pt.Update("app1", 1024, 0, 100.0)
	snap, _ = pt.Snapshot("app1")
	if snap.Percent != -1 {
		t.Errorf("Percent = %v, want -1 when TotalBytes is 0", snap.Percent)
	}
	if snap.TotalBytes != -1 {
		t.Errorf("TotalBytes = %d, want -1 when total is 0", snap.TotalBytes)
	}

	pt.Update("app1", 1024, -5, 100.0)
	snap, _ = pt.Snapshot("app1")
	if snap.Percent != -1 {
		t.Errorf("Percent = %v, want -1 when TotalBytes is negative", snap.Percent)
	}
	if snap.TotalBytes != -1 {
		t.Errorf("TotalBytes = %d, want -1 when total is negative", snap.TotalBytes)
	}
}

func TestProgressTrackerFinish(t *testing.T) {
	pt := NewProgressTracker()
	pt.Start("app1", "job1", "v1")
	pt.Finish("app1")

	snap, ok := pt.Snapshot("app1")
	if !ok {
		t.Fatal("entry should still be readable after Finish (within grace window)")
	}
	if snap.Phase != ProgressStageSucceeded {
		t.Errorf("Phase = %q, want %q", snap.Phase, ProgressStageSucceeded)
	}
}

func TestProgressTrackerSetPhase(t *testing.T) {
	pt := NewProgressTracker()
	pt.Start("app1", "job1", "v1")
	pt.SetPhase("app1", ProgressStageInstalling)

	snap, ok := pt.Snapshot("app1")
	if !ok {
		t.Fatal("entry should still be readable after SetPhase")
	}
	if snap.Phase != ProgressStageInstalling {
		t.Errorf("Phase = %q, want %q", snap.Phase, ProgressStageInstalling)
	}
}

func TestProgressTrackerStageConstants(t *testing.T) {
	want := map[string]string{
		"queued":      ProgressStageQueued,
		"downloading": ProgressStageDownloading,
		"validating":  ProgressStageValidating,
		"backing_up":  ProgressStageBackingUp,
		"installing":  ProgressStageInstalling,
		"restarting":  ProgressStageRestarting,
		"healthcheck": ProgressStageHealthcheck,
		"rollback":    ProgressStageRollback,
		"succeeded":   ProgressStageSucceeded,
		"failed":      ProgressStageFailed,
	}
	for expected, got := range want {
		if got != expected {
			t.Errorf("stage constant = %q, want %q", got, expected)
		}
	}
}

func TestProgressTrackerFail(t *testing.T) {
	pt := NewProgressTracker()
	pt.Start("app1", "job1", "v1")
	pt.Fail("app1")

	snap, ok := pt.Snapshot("app1")
	if !ok {
		t.Fatal("entry should still be readable after Fail (within grace window)")
	}
	if snap.Phase != ProgressStageFailed {
		t.Errorf("Phase = %q, want %q", snap.Phase, ProgressStageFailed)
	}
}

func TestProgressTrackerCleanupRemovesExpiredEntries(t *testing.T) {
	var fakeNow time.Time
	fakeNow = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	pt := newProgressTrackerWithClock(30*time.Second, func() time.Time { return fakeNow })

	pt.Start("app1", "job1", "v1")
	pt.Finish("app1")

	fakeNow = fakeNow.Add(29 * time.Second)
	pt.Cleanup()

	if _, ok := pt.Snapshot("app1"); !ok {
		t.Error("entry should still exist before grace window expires")
	}

	fakeNow = fakeNow.Add(2 * time.Second)
	pt.Cleanup()

	if _, ok := pt.Snapshot("app1"); ok {
		t.Error("entry should be removed after grace window expires")
	}
}

func TestProgressTrackerCleanupKeepsActiveEntries(t *testing.T) {
	var fakeNow time.Time
	fakeNow = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	pt := newProgressTrackerWithClock(1*time.Second, func() time.Time { return fakeNow })

	pt.Start("app1", "job1", "v1")

	fakeNow = fakeNow.Add(1 * time.Hour)
	pt.Cleanup()

	if _, ok := pt.Snapshot("app1"); !ok {
		t.Error("active entry must never be removed by Cleanup")
	}
}

func TestProgressTrackerMultipleAppsIndependent(t *testing.T) {
	pt := NewProgressTracker()
	pt.Start("app1", "job1", "v1")
	pt.Start("app2", "job2", "v2")

	pt.Update("app1", 100, 200, 50.0)
	pt.Update("app2", 300, 600, 150.0)

	s1, ok1 := pt.Snapshot("app1")
	s2, ok2 := pt.Snapshot("app2")

	if !ok1 || !ok2 {
		t.Fatal("both apps should have entries")
	}
	if s1.DownloadedBytes != 100 {
		t.Errorf("app1 DownloadedBytes = %d, want 100", s1.DownloadedBytes)
	}
	if s2.DownloadedBytes != 300 {
		t.Errorf("app2 DownloadedBytes = %d, want 300", s2.DownloadedBytes)
	}

	pt.Finish("app1")
	if s, _ := pt.Snapshot("app1"); s.Phase != ProgressStageSucceeded {
		t.Errorf("app1 Phase = %q after Finish, want %q", s.Phase, ProgressStageSucceeded)
	}
	if s, _ := pt.Snapshot("app2"); s.Phase != ProgressStageDownloading {
		t.Errorf("app2 Phase should remain %q, got %q", ProgressStageDownloading, s.Phase)
	}
}

func TestProgressTrackerSnapshotAll(t *testing.T) {
	pt := NewProgressTracker()
	pt.Start("app1", "job1", "v1")
	pt.Start("app2", "job2", "v2")
	pt.Start("app3", "job3", "v3")

	all := pt.SnapshotAll()
	if len(all) != 3 {
		t.Errorf("SnapshotAll len = %d, want 3", len(all))
	}

	ids := make(map[string]bool)
	for _, s := range all {
		ids[s.AppID] = true
	}
	for _, id := range []string{"app1", "app2", "app3"} {
		if !ids[id] {
			t.Errorf("SnapshotAll missing AppID %q", id)
		}
	}
}

func TestProgressTrackerSnapshotAllEmpty(t *testing.T) {
	pt := NewProgressTracker()
	all := pt.SnapshotAll()
	if len(all) != 0 {
		t.Errorf("SnapshotAll on empty tracker should return empty slice, got len %d", len(all))
	}
}

func TestProgressTrackerConcurrentStartUpdateSnapshot(t *testing.T) {
	pt := NewProgressTracker()
	const goroutines = 20
	const apps = 4

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			appID := "app" + string(rune('0'+n%apps))
			pt.Start(appID, "job", "v1")
			pt.Update(appID, int64(n*100), int64(n*200+1), float64(n*10))
			pt.Snapshot(appID)
			pt.SnapshotAll()
		}(i)
	}
	wg.Wait()
}

func TestProgressTrackerConcurrentFinishCleanup(t *testing.T) {
	var fakeNow time.Time
	fakeNow = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex

	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return fakeNow
	}

	pt := newProgressTrackerWithClock(10*time.Second, now)

	const apps = 10
	for i := 0; i < apps; i++ {
		appID := "app" + string(rune('0'+i))
		pt.Start(appID, "job", "v1")
	}

	var wg sync.WaitGroup
	for i := 0; i < apps; i++ {
		wg.Add(1)
		appID := "app" + string(rune('0'+i))
		go func(id string) {
			defer wg.Done()
			pt.Finish(id)
		}(appID)
	}
	wg.Wait()

	mu.Lock()
	fakeNow = fakeNow.Add(11 * time.Second)
	mu.Unlock()

	var cleanupWg sync.WaitGroup
	for i := 0; i < 5; i++ {
		cleanupWg.Add(1)
		go func() {
			defer cleanupWg.Done()
			pt.Cleanup()
		}()
	}
	cleanupWg.Wait()

	all := pt.SnapshotAll()
	if len(all) != 0 {
		t.Errorf("expected all entries cleaned up, got %d remaining", len(all))
	}
}

func TestProgressTrackerGraceWindowReadable(t *testing.T) {
	var fakeNow time.Time
	fakeNow = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	pt := newProgressTrackerWithClock(30*time.Second, func() time.Time { return fakeNow })

	pt.Start("app1", "job1", "v1")
	pt.Update("app1", 1000, 1000, 500.0)
	pt.Finish("app1")

	snap, ok := pt.Snapshot("app1")
	if !ok {
		t.Fatal("entry must be readable immediately after Finish")
	}
	if snap.Phase != ProgressStageSucceeded {
		t.Errorf("Phase = %q, want succeeded", snap.Phase)
	}

	fakeNow = fakeNow.Add(29 * time.Second)
	pt.Cleanup()

	if _, ok := pt.Snapshot("app1"); !ok {
		t.Error("entry must still be readable at 29s (grace=30s)")
	}

	fakeNow = fakeNow.Add(2 * time.Second)
	pt.Cleanup()

	if _, ok := pt.Snapshot("app1"); ok {
		t.Error("entry must be gone after grace window")
	}
}

func TestProgressTrackerUpdateIgnoresUnknownApp(t *testing.T) {
	pt := NewProgressTracker()
	pt.Update("ghost", 100, 200, 50.0)

	if _, ok := pt.Snapshot("ghost"); ok {
		t.Error("Update on unknown app should not create an entry")
	}
}

func TestProgressTrackerFinishIgnoresUnknownApp(t *testing.T) {
	pt := NewProgressTracker()
	pt.Finish("ghost")
	pt.Fail("ghost")
}

func TestProgressTrackerStartOverwritesPreviousEntry(t *testing.T) {
	pt := NewProgressTracker()
	pt.Start("app1", "job1", "v1")
	pt.Update("app1", 500, 1000, 100.0)
	pt.Start("app1", "job2", "v2")

	snap, _ := pt.Snapshot("app1")
	if snap.JobID != "job2" {
		t.Errorf("JobID = %q after re-Start, want job2", snap.JobID)
	}
	if snap.Tag != "v2" {
		t.Errorf("Tag = %q after re-Start, want v2", snap.Tag)
	}
	if snap.DownloadedBytes != 0 {
		t.Errorf("DownloadedBytes should reset to 0 on re-Start, got %d", snap.DownloadedBytes)
	}
	if snap.Phase != ProgressStageDownloading {
		t.Errorf("Phase = %q after re-Start, want %q", snap.Phase, ProgressStageDownloading)
	}
}
