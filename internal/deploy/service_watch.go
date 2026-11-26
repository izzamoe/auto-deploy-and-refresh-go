package deploy

import "time"

// ServiceWatcher is the shared observer of the systemd state of every app's service
// (002 FR-007, FR-008, data-model.md section ServiceWatcher, Q-3=A): while at least one
// subscriber holds it (Acquire/Release), ONE `systemctl is-active -- u1 … uN` per round
// (every Interval, plus Refresh after a panel action) and only CHANGES are reported to
// onChange; the first observation of a unit is not a change.
//
// STUB (T016, RV-5): the API exists so the package compiles and the RED tests fail on
// their assertions; nothing is observed yet. T045 implements it.
type ServiceWatcher struct {
	units    func() map[string]string
	onChange func(appID, state string)
	opts     ServiceWatcherOptions
}

// ServiceWatcherOptions tunes the watcher: Interval between rounds while subscribed
// (SERVICE_WATCH_INTERVAL, default 5 s) and Debounce of Refresh (default 250 ms).
type ServiceWatcherOptions struct {
	Interval time.Duration
	Debounce time.Duration
}

// NewServiceWatcher builds an idle watcher. units returns appID -> serviceName of every
// app; onChange receives each state change.
func NewServiceWatcher(units func() map[string]string, onChange func(appID, state string), opts ServiceWatcherOptions) *ServiceWatcher {
	return &ServiceWatcher{units: units, onChange: onChange, opts: opts}
}

// Acquire registers one subscriber (refs 0 -> 1 starts the rounds). STUB: no-op.
func (w *ServiceWatcher) Acquire() {}

// Release drops one subscriber (refs 1 -> 0 stops the rounds). STUB: no-op.
func (w *ServiceWatcher) Release() {}

// Refresh asks for a round soon (debounced), e.g. after a service control or a deploy.
// STUB: no-op.
func (w *ServiceWatcher) Refresh(appID string) {}

// RoundForTest runs one observation round synchronously, without the clock. STUB: no-op.
func (w *ServiceWatcher) RoundForTest() {}

// Snapshot returns the last observed state of every app (for the `subscribed` ack).
// STUB: empty.
func (w *ServiceWatcher) Snapshot() map[string]string { return map[string]string{} }
