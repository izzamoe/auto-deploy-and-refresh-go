package deploy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// T089 CR-5 (independent code review): RED before the fix, GREEN after
// (reference/executor/T089-red.log, T089.log).

// CR-5: the watcher reads ONLY the standard output of `systemctl is-active`: a warning on
// stderr (an escaped unit name) must not shift the states onto the wrong apps.
func TestT089_CR5_IsActiveIgnoresStderr(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'Invalid unit name \"a b\" escaped as \"a\\x20b\".' >&2\necho failed\necho active\nexit 3\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	w := NewServiceWatcher(func() map[string]string {
		return map[string]string{"app1": "a b", "app2": "web.service"}
	}, nil, ServiceWatcherOptions{Interval: time.Hour, Debounce: time.Millisecond})
	w.RoundForTest()
	if got := w.Snapshot(); got["app1"] != "failed" || got["app2"] != "active" {
		t.Fatalf("CR-5: Snapshot() = %v, want app1=failed app2=active", got)
	}
}

// CR-5: when the output does not have one line per unit, no state is guessed by position.
func TestT089_CR5_LineCountMismatchIsNotMisattributed(t *testing.T) {
	calls := 0
	t.Cleanup(SetRunSystemctlForTest(func(verb string, args ...string) ([]byte, error) {
		calls++
		return []byte("warning\nfailed\nactive\n"), errors.New("exit status 3")
	}))
	w := NewServiceWatcher(func() map[string]string {
		return map[string]string{"app1": "a.service", "app2": "web.service"}
	}, nil, ServiceWatcherOptions{Interval: time.Hour, Debounce: time.Millisecond})
	w.RoundForTest()
	got := w.Snapshot()
	if got["app1"] == "warning" || got["app1"] == "failed" || got["app2"] == "failed" {
		t.Fatalf("CR-5: Snapshot() = %v, a state was attributed by position to the wrong app", got)
	}
	if got["app1"] != "unknown" || got["app2"] != "unknown" {
		t.Fatalf("CR-5: Snapshot() = %v, want unknown for every app when the lines do not match the units", got)
	}
}
