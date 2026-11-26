package deploy

import (
	"fmt"
	"testing"
	"time"
)

// T083 (NFR-011, FR-008, Q-3=A): the cost of one watcher round for N=50 apps. The
// benchmark replaces systemctl with an in-process fake, so it measures the watcher's own
// work (unit list, parsing, change detection); the real process cost is measured by
// TestServiceWatcher_RealSystemctl50 (build tag realsystemctl).

// benchUnits returns appID -> unit of n apps.
func benchUnits(n int) map[string]string {
	units := make(map[string]string, n)
	for i := 0; i < n; i++ {
		units[fmt.Sprintf("app-%02d", i)] = fmt.Sprintf("ad-bench-%02d.service", i)
	}
	return units
}

func BenchmarkServiceWatcherRound50(b *testing.B) {
	f := &fakeIsActive{state: map[string]string{}}
	units := benchUnits(50)
	for _, u := range units {
		f.set(u, "active")
	}
	restore := SetRunSystemctlForTest(func(verb string, args ...string) ([]byte, error) {
		out, err := f.run(verb, args...)
		f.mu.Lock()
		f.calls = f.calls[:0] // keep memory flat over b.N rounds
		f.mu.Unlock()
		return out, err
	})
	defer restore()
	changes := 0
	w := NewServiceWatcher(func() map[string]string { return units }, func(string, string) { changes++ }, ServiceWatcherOptions{Interval: time.Hour, Debounce: time.Millisecond})
	w.RoundForTest() // first observation
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.RoundForTest()
	}
	b.StopTimer()
	if changes != 0 {
		b.Fatalf("rounds without a state change reported %d changes", changes)
	}
	perRound := time.Duration(int64(b.Elapsed()) / int64(b.N))
	roundsPerMinute := int64(time.Minute / DefaultServiceWatchInterval)
	b.ReportMetric(float64(perRound.Microseconds()*roundsPerMinute)/1000, "ms-cpu/min@5s")
}
