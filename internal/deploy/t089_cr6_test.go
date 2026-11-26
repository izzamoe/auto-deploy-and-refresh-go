package deploy

import (
	"testing"
	"time"
)

// T089 CR-6 (independent code review): RED before the fix, GREEN after
// (reference/executor/T089-red.log, T089.log).

// CR-6: the recent lines of a stopped process are dropped when a new one starts: a
// subscriber whose cursor is in the old lines would otherwise get a replay with a hole
// (the lines between the stop and the new start) and no gap.
func TestT089_CR6_RecentLinesClearedOnRestart(t *testing.T) {
	fj := newFakeJournal(t)
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: 20 * time.Millisecond}, &b)
	t.Cleanup(f.Close)
	f.Acquire("")
	p := fj.proc(t, 0)
	p.write(t, entry("s=a;i=1", "one", 6)+entry("s=a;i=2", "two", 6))
	waitFor(t, "two lines", func() bool { return len(b.lines()) == 2 })
	f.Release()
	waitFor(t, "the stop after the grace", func() bool { return !f.Running() })

	f.Acquire("")
	p2 := fj.proc(t, 1)
	p2.write(t, entry("s=a;i=9", "new", 6))
	waitFor(t, "the new line", func() bool { return f.Head() == "s=a;i=9" })
	join := f.Acquire("s=a;i=1")
	if !join.Behind {
		t.Fatalf("CR-6: a cursor from before the restart got %+v, want Behind (cursor_behind: reload)", join)
	}
}
