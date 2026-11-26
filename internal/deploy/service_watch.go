package deploy

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/izzamoe/auto-deploy/internal/events"
)

// ServiceWatcher is the shared observer of the systemd state of every app's service
// (002 FR-007, FR-008, data-model.md section ServiceWatcher, Q-3=A): while at least one
// subscriber holds it (Acquire/Release), ONE `systemctl is-active -- u1 … uN` per round
// (every Interval, plus Refresh after a panel action) and only CHANGES are reported to
// onChange; the first observation of a unit is not a change. Without subscribers it runs
// no process and no goroutine.
type ServiceWatcher struct {
	units    func() map[string]string
	onChange func(appID, state string)
	opts     ServiceWatcherOptions

	mu      sync.Mutex
	refs    int
	stop    chan struct{} // closed by the last Release; one per running loop
	refresh chan struct{} // coalesced Refresh requests of the running loop
	last    map[string]observedUnit

	roundMu sync.Mutex // one round at a time (loop, Refresh, Acquire, RoundForTest)
}

type observedUnit struct {
	unit  string
	state string
}

// ServiceWatcherOptions tunes the watcher: Interval between rounds while subscribed
// (SERVICE_WATCH_INTERVAL, default 5 s) and Debounce of Refresh (default 250 ms).
type ServiceWatcherOptions struct {
	Interval time.Duration
	Debounce time.Duration
}

// Defaults and bounds of ServiceWatcherOptions (data-model.md: min 2 s, max 60 s).
const (
	DefaultServiceWatchInterval = 5 * time.Second
	MinServiceWatchInterval     = 2 * time.Second
	MaxServiceWatchInterval     = 60 * time.Second
	DefaultServiceWatchDebounce = 250 * time.Millisecond
)

// ServiceStates are the systemd active states the service topic reports
// (events-ws.v3.md 2.2); anything else is reported as "unknown".
var ServiceStates = []string{"active", "inactive", "failed", "activating", "deactivating", "reloading", "unknown"}

// NewServiceWatcher builds an idle watcher. units returns appID -> serviceName of every
// app; onChange receives each state change.
func NewServiceWatcher(units func() map[string]string, onChange func(appID, state string), opts ServiceWatcherOptions) *ServiceWatcher {
	if opts.Interval <= 0 {
		opts.Interval = DefaultServiceWatchInterval
	}
	if opts.Debounce <= 0 {
		opts.Debounce = DefaultServiceWatchDebounce
	}
	return &ServiceWatcher{units: units, onChange: onChange, opts: opts, last: make(map[string]observedUnit)}
}

// Acquire registers one subscriber. The first one (refs 0 -> 1) runs a round at once, so
// the state of every app is known before the caller acknowledges the subscription
// (events-ws.v3.md section 3), then starts the periodic rounds.
func (w *ServiceWatcher) Acquire() {
	w.mu.Lock()
	w.refs++
	first := w.refs == 1
	var stop, refresh chan struct{}
	if first {
		stop, refresh = make(chan struct{}), make(chan struct{}, 1)
		w.stop, w.refresh = stop, refresh
	}
	w.mu.Unlock()
	if !first {
		return
	}
	w.round()
	go w.loop(stop, refresh)
}

// Release drops one subscriber; the last one (refs 1 -> 0) stops the rounds.
func (w *ServiceWatcher) Release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refs == 0 {
		return
	}
	w.refs--
	if w.refs == 0 {
		close(w.stop)
		w.stop, w.refresh = nil, nil
	}
}

// Refresh asks for a round soon (debounced), e.g. after a service control or a deploy.
// Without subscribers nobody listens and nothing runs.
func (w *ServiceWatcher) Refresh(appID string) {
	w.mu.Lock()
	ch := w.refresh
	w.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default: // a refresh is already pending
	}
}

// RoundForTest runs one observation round synchronously, without the clock.
func (w *ServiceWatcher) RoundForTest() { w.round() }

// Snapshot returns the last observed state of every app (for the `subscribed` ack).
func (w *ServiceWatcher) Snapshot() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]string, len(w.last))
	for id, o := range w.last {
		out[id] = o.state
	}
	return out
}

func (w *ServiceWatcher) loop(stop <-chan struct{}, refresh <-chan struct{}) {
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	var debounce *time.Timer
	var due <-chan time.Time
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			w.round()
		case <-refresh:
			if debounce == nil {
				debounce = time.NewTimer(w.opts.Debounce)
				due = debounce.C
			}
		case <-due:
			debounce, due = nil, nil
			w.round()
		}
	}
}

// round asks systemd for the state of every unit with ONE process and reports the
// changes. "--" ends option parsing: a unit name can never be read as an option (B22).
func (w *ServiceWatcher) round() {
	w.roundMu.Lock()
	defer w.roundMu.Unlock()
	start := time.Now()
	defer func() {
		events.M().ServiceWatchRuns.Add(1)
		events.M().ServiceWatchMsTotal.Add(time.Since(start).Milliseconds())
	}()

	units := map[string]string{}
	if w.units != nil {
		units = w.units()
	}
	appIDs := make([]string, 0, len(units))
	for id, unit := range units {
		if strings.TrimSpace(unit) != "" {
			appIDs = append(appIDs, id)
		}
	}
	sort.Strings(appIDs)
	states := make(map[string]string, len(appIDs))
	if len(appIDs) > 0 {
		args := make([]string, 0, len(appIDs)+1)
		args = append(args, "--")
		for _, id := range appIDs {
			args = append(args, units[id])
		}
		// is-active exits non-zero when a unit is not active; stdout still holds one state
		// per unit, in argument order.
		out, _ := runSystemctl("is-active", args...)
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		for i, id := range appIDs {
			st := "unknown"
			if i < len(lines) {
				st = normalizeServiceState(lines[i])
			}
			states[id] = st
		}
	}

	type change struct{ appID, state string }
	var changes []change
	w.mu.Lock()
	for id, st := range states {
		prev, known := w.last[id]
		w.last[id] = observedUnit{unit: units[id], state: st}
		// The first observation of a unit (a new app, a renamed unit) is not a change.
		if known && prev.unit == units[id] && prev.state != st {
			changes = append(changes, change{id, st})
		}
	}
	for id := range w.last {
		if _, ok := states[id]; !ok {
			delete(w.last, id) // the app is gone (or has no unit any more)
		}
	}
	w.mu.Unlock()
	sort.Slice(changes, func(i, j int) bool { return changes[i].appID < changes[j].appID })
	if w.onChange != nil {
		for _, c := range changes {
			w.onChange(c.appID, c.state)
		}
	}
}

func normalizeServiceState(s string) string {
	s = strings.TrimSpace(s)
	for _, known := range ServiceStates {
		if s == known {
			return s
		}
	}
	return "unknown"
}
