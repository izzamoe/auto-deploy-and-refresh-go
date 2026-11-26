package deploy

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// T016 (RED, FR-007, FR-008, Q-3=A): one shared watcher observes the service of every app
// with ONE `systemctl is-active -- u1 … uN` per round, reports only changes, and costs
// nothing while nobody subscribes.

// fakeIsActive answers `systemctl is-active -- <units…>` like systemd: one state per
// unit in argument order, exit status 3 when one unit is not active. It records every
// call.
type fakeIsActive struct {
	mu    sync.Mutex
	state map[string]string
	calls [][]string // verb + args of every systemctl call
}

func (f *fakeIsActive) set(unit, state string) {
	f.mu.Lock()
	f.state[unit] = state
	f.mu.Unlock()
}

func (f *fakeIsActive) count(verb string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c[0] == verb {
			n++
		}
	}
	return n
}

func (f *fakeIsActive) run(verb string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{verb}, args...))
	if verb != "is-active" {
		return nil, nil
	}
	var units []string
	for i, a := range args {
		if a == "--" {
			units = args[i+1:]
			break
		}
	}
	var out strings.Builder
	all := true
	for _, u := range units {
		st, ok := f.state[u]
		if !ok {
			st = "unknown"
		}
		if st != "active" {
			all = false
		}
		out.WriteString(st + "\n")
	}
	if !all {
		return []byte(out.String()), errors.New("exit status 3")
	}
	return []byte(out.String()), nil
}

func installFakeIsActive(t *testing.T) *fakeIsActive {
	t.Helper()
	f := &fakeIsActive{state: map[string]string{}}
	restore := SetRunSystemctlForTest(f.run)
	t.Cleanup(restore)
	return f
}

type changeLog struct {
	mu      sync.Mutex
	changes []string
}

func (c *changeLog) add(appID, state string) {
	c.mu.Lock()
	c.changes = append(c.changes, appID+"="+state)
	c.mu.Unlock()
}

func (c *changeLog) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.changes...)
}

// An app's service fails outside the panel: round 1 sees it active (first observation,
// not a change), round 2 sees it failed -> exactly one change.
func TestServiceWatcher_DetectsExternalChange(t *testing.T) {
	f := installFakeIsActive(t)
	f.set("svc-a.service", "active")
	f.set("svc-b.service", "active")
	var log changeLog
	w := NewServiceWatcher(func() map[string]string {
		return map[string]string{"a": "svc-a.service", "b": "svc-b.service"}
	}, log.add, ServiceWatcherOptions{Interval: time.Hour, Debounce: time.Millisecond})

	w.RoundForTest()
	if got := log.list(); len(got) != 0 {
		t.Fatalf("round 1 (first observation) must not report changes, got %v", got)
	}
	if got := w.Snapshot(); got["a"] != "active" || got["b"] != "active" {
		t.Fatalf("after round 1 Snapshot() = %v, want a=active b=active", got)
	}

	f.set("svc-a.service", "failed") // systemctl stop/crash outside the panel
	w.RoundForTest()
	if got := log.list(); len(got) != 1 || got[0] != "a=failed" {
		t.Fatalf("round 2: changes = %v, want exactly [a=failed]", got)
	}
	if got := w.Snapshot(); got["a"] != "failed" || got["b"] != "active" {
		t.Fatalf("after round 2 Snapshot() = %v, want a=failed b=active", got)
	}

	w.RoundForTest() // nothing changed
	if got := log.list(); len(got) != 1 {
		t.Fatalf("round 3 without a change reported %v", got)
	}
}

// One process per round for every unit (research.md section 7.1: 50 units in one
// is-active = 4.3 ms vs 50 processes), with "--" before the unit list (B22).
func TestServiceWatcher_OneProcessForAllUnits(t *testing.T) {
	f := installFakeIsActive(t)
	units := map[string]string{}
	for i := 0; i < 50; i++ {
		u := fmt.Sprintf("svc-%02d.service", i)
		units[fmt.Sprintf("app-%02d", i)] = u
		f.set(u, "active")
	}
	w := NewServiceWatcher(func() map[string]string { return units }, func(string, string) {}, ServiceWatcherOptions{Interval: time.Hour, Debounce: time.Millisecond})

	w.RoundForTest()
	if n := f.count("is-active"); n != 1 {
		t.Fatalf("one round for 50 units ran systemctl is-active %d times, want 1", n)
	}
	f.mu.Lock()
	call := append([]string(nil), f.calls[0]...)
	f.mu.Unlock()
	dash := -1
	for i, a := range call {
		if a == "--" {
			dash = i
			break
		}
	}
	if dash < 0 {
		t.Fatalf("is-active call has no \"--\" before the units (B22): %v", call)
	}
	got := map[string]bool{}
	for _, u := range call[dash+1:] {
		got[u] = true
	}
	for _, u := range units {
		if !got[u] {
			t.Errorf("unit %s missing from the batched is-active call", u)
		}
	}
	if len(call[dash+1:]) != len(units) {
		t.Errorf("batched call has %d units, want %d", len(call[dash+1:]), len(units))
	}
	if snap := w.Snapshot(); len(snap) != 50 {
		t.Errorf("Snapshot() has %d apps after one round, want 50", len(snap))
	}
}

// Regression (RV2-6; may already pass on the stub): without subscribers the watcher
// runs no systemctl and leaves no goroutine; after the last Release it stops again.
func TestServiceWatcher_IdleWithoutSubscribers(t *testing.T) {
	f := installFakeIsActive(t)
	f.set("svc-a.service", "active")
	before := runtime.NumGoroutine()
	w := NewServiceWatcher(func() map[string]string { return map[string]string{"a": "svc-a.service"} },
		func(string, string) {}, ServiceWatcherOptions{Interval: 10 * time.Millisecond, Debounce: time.Millisecond})

	time.Sleep(100 * time.Millisecond)
	if n := f.count("is-active"); n != 0 {
		t.Fatalf("no subscriber: systemctl is-active ran %d times, want 0", n)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("no subscriber: goroutines %d -> %d, want no new goroutine", before, after)
	}

	w.Acquire()
	w.Release()
	time.Sleep(50 * time.Millisecond) // let a stopping loop finish
	settled := f.count("is-active")
	time.Sleep(100 * time.Millisecond)
	if n := f.count("is-active"); n != settled {
		t.Fatalf("after the last Release systemctl still runs (%d -> %d calls)", settled, n)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("after the last Release goroutines %d -> %d, want the watcher loop gone", before, after)
	}
}
