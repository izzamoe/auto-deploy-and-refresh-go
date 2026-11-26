//go:build realsystemctl && linux

package deploy

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// T083 (NFR-011): one minute of watcher rounds (60 s / 5 s = 12 rounds) for N=50 apps with
// the REAL systemctl (`is-active` only: a read-only query of units that do not exist; no
// unit is installed, started or stopped). CPU = this process + its children (getrusage).
// Run with: go test -count=1 -v -run TestServiceWatcher_RealSystemctl50 -tags realsystemctl ./internal/deploy
func TestServiceWatcher_RealSystemctl50(t *testing.T) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("systemctl is not available on this host")
	}
	units := benchUnits(50)
	if out, err := runSystemctl("is-active", "--", "ad-bench-00.service"); len(out) == 0 {
		t.Skipf("systemctl is-active does not answer here (no systemd?): %v", err)
	}
	w := NewServiceWatcher(func() map[string]string { return units }, func(string, string) {}, ServiceWatcherOptions{Interval: time.Hour, Debounce: time.Millisecond})
	w.RoundForTest() // warm-up, first observation

	cpu := func() time.Duration {
		var self, children syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &self)
		_ = syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children)
		tv := func(v syscall.Timeval) time.Duration {
			return time.Duration(v.Sec)*time.Second + time.Duration(v.Usec)*time.Microsecond
		}
		return tv(self.Utime) + tv(self.Stime) + tv(children.Utime) + tv(children.Stime)
	}
	rounds := int(time.Minute / DefaultServiceWatchInterval)
	before, start := cpu(), time.Now()
	for i := 0; i < rounds; i++ {
		w.RoundForTest()
	}
	used, wall := cpu()-before, time.Since(start)
	snap := w.Snapshot()
	if len(snap) != len(units) {
		t.Fatalf("Snapshot() has %d units, want %d", len(snap), len(units))
	}
	t.Logf("N=50, %d rounds (one minute at %s): CPU %d ms (process + systemctl children), wall %d ms, %.1f ms CPU/round",
		rounds, DefaultServiceWatchInterval, used.Milliseconds(), wall.Milliseconds(), float64(used.Microseconds())/1000/float64(rounds))
	if used > 100*time.Millisecond {
		t.Fatalf("NFR-011: watcher CPU per minute = %d ms, want <= 100 ms", used.Milliseconds())
	}
}
