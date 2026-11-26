package deploy

import (
	"strings"
	"testing"
	"time"
)

// T089 CR-7 (independent code review): RED before the fix, GREEN after
// (reference/executor/T089-red.log, T089.log).

// CR-7: a first subscriber without a cursor after a stop starts at the end of the journal,
// not at the head of the previous process (hours of old lines and a false logs.gap{rate}).
func TestT089_CR7_NewProcessWithoutCursorStartsAtEnd(t *testing.T) {
	fj := newFakeJournal(t)
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: 20 * time.Millisecond}, &b)
	t.Cleanup(f.Close)
	f.Acquire("")
	p := fj.proc(t, 0)
	p.write(t, entry("s=a;i=1", "old", 6))
	waitFor(t, "the line", func() bool { return len(b.lines()) == 1 })
	f.Release()
	waitFor(t, "the stop after the grace", func() bool { return !f.Running() })

	f.Acquire("")
	fj.proc(t, 1)
	if args := fj.args(1); !strings.Contains(args, "--lines=0") || strings.Contains(args, "--after-cursor") {
		t.Fatalf("CR-7: new process args = %q, want --lines=0 (no cursor from the subscriber)", args)
	}
}
