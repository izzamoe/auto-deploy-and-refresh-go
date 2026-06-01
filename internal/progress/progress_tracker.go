package progress

import (
	"sync"
	"time"
)

// ProgressSnapshot is a read-only view of one deployment's progress.
// TotalBytes and Percent are -1 when the download size is unknown.
type ProgressSnapshot struct {
	AppID           string
	JobID           string
	Tag             string
	Phase           string
	DownloadedBytes int64
	TotalBytes      int64
	SpeedBPS        float64
	Percent         float64
	UpdatedAt       time.Time
}

type progressEntry struct {
	snapshot   ProgressSnapshot
	finishedAt time.Time
}

// ProgressSink receives a copy of the latest progress state after tracker updates.
type ProgressSink interface {
	PublishProgress(ProgressSnapshot)
}

// ProgressTracker keeps one in-memory entry per app for the active deployment.
// Finished entries persist for graceWindow before Cleanup removes them.
type ProgressTracker struct {
	mu          sync.RWMutex
	entries     map[string]*progressEntry
	sink        ProgressSink
	graceWindow time.Duration
	now         func() time.Time
}

// NewProgressTracker creates a ProgressTracker with a 30-second grace window.
func NewProgressTracker() *ProgressTracker {
	return newProgressTrackerWithClock(30*time.Second, time.Now)
}

func newProgressTrackerWithClock(grace time.Duration, now func() time.Time) *ProgressTracker {
	return &ProgressTracker{
		entries:     make(map[string]*progressEntry),
		graceWindow: grace,
		now:         now,
	}
}

// SetProgressSink connects the tracker to a fanout-only progress sink.
func (pt *ProgressTracker) SetProgressSink(sink ProgressSink) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.sink = sink
}

// Start registers a new in-progress deployment for appID, overwriting any existing entry.
func (pt *ProgressTracker) Start(appID, jobID, tag string) {
	pt.mu.Lock()

	pt.entries[appID] = &progressEntry{
		snapshot: ProgressSnapshot{
			AppID:      appID,
			JobID:      jobID,
			Tag:        tag,
			Phase:      ProgressStageDownloading,
			TotalBytes: -1,
			Percent:    -1,
			UpdatedAt:  pt.now(),
		},
	}
	snapshot := pt.entries[appID].snapshot
	sink := pt.sink
	pt.mu.Unlock()
	publishProgress(sink, snapshot)
}

// Update sets the download progress for appID.
// total == -1 means unknown size; Percent will be set to -1 in that case.
func (pt *ProgressTracker) Update(appID string, downloaded, total int64, speedBPS float64) {
	pt.mu.Lock()

	e, ok := pt.entries[appID]
	if !ok {
		pt.mu.Unlock()
		return
	}

	var pct float64
	if total > 0 {
		pct = float64(downloaded) / float64(total) * 100
	} else {
		pct = -1
		total = -1
	}

	e.snapshot.DownloadedBytes = downloaded
	e.snapshot.TotalBytes = total
	e.snapshot.SpeedBPS = speedBPS
	e.snapshot.Percent = pct
	e.snapshot.UpdatedAt = pt.now()
	snapshot := e.snapshot
	sink := pt.sink
	pt.mu.Unlock()
	publishProgress(sink, snapshot)
}

// SetPhase updates the current non-terminal phase for appID.
// Missing entries are ignored.
func (pt *ProgressTracker) SetPhase(appID, phase string) {
	pt.mu.Lock()

	e, ok := pt.entries[appID]
	if !ok {
		pt.mu.Unlock()
		return
	}

	e.snapshot.Phase = phase
	e.snapshot.UpdatedAt = pt.now()
	snapshot := e.snapshot
	sink := pt.sink
	pt.mu.Unlock()
	publishProgress(sink, snapshot)
}

// Finish marks the deployment for appID as succeeded.
// The entry remains readable until Cleanup removes it after the grace window.
func (pt *ProgressTracker) Finish(appID string) {
	pt.mu.Lock()

	e, ok := pt.entries[appID]
	if !ok {
		pt.mu.Unlock()
		return
	}
	e.snapshot.Phase = ProgressStageSucceeded
	e.snapshot.UpdatedAt = pt.now()
	e.finishedAt = pt.now()
	snapshot := e.snapshot
	sink := pt.sink
	pt.mu.Unlock()
	publishProgress(sink, snapshot)
}

// Fail marks the deployment for appID as failed.
// The entry remains readable until Cleanup removes it after the grace window.
func (pt *ProgressTracker) Fail(appID string) {
	pt.mu.Lock()

	e, ok := pt.entries[appID]
	if !ok {
		pt.mu.Unlock()
		return
	}
	e.snapshot.Phase = ProgressStageFailed
	e.snapshot.UpdatedAt = pt.now()
	e.finishedAt = pt.now()
	snapshot := e.snapshot
	sink := pt.sink
	pt.mu.Unlock()
	publishProgress(sink, snapshot)
}

func publishProgress(sink ProgressSink, snapshot ProgressSnapshot) {
	if sink == nil {
		return
	}
	sink.PublishProgress(snapshot)
}

// Snapshot returns the current progress for appID.
// Returns (nil, false) if no entry exists.
func (pt *ProgressTracker) Snapshot(appID string) (*ProgressSnapshot, bool) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	e, ok := pt.entries[appID]
	if !ok {
		return nil, false
	}
	cp := e.snapshot
	return &cp, true
}

// SnapshotAll returns a copy of all currently tracked entries.
func (pt *ProgressTracker) SnapshotAll() []ProgressSnapshot {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	out := make([]ProgressSnapshot, 0, len(pt.entries))
	for _, e := range pt.entries {
		out = append(out, e.snapshot)
	}
	return out
}

// Cleanup removes entries whose grace window has elapsed since Finish or Fail was called.
// Active (not yet finished) entries are never removed.
func (pt *ProgressTracker) Cleanup() {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	now := pt.now()
	for appID, e := range pt.entries {
		if !e.finishedAt.IsZero() && now.Sub(e.finishedAt) >= pt.graceWindow {
			delete(pt.entries, appID)
		}
	}
}
