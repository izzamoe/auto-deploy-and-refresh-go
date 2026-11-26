package deploy

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// T089 CR-2 (independent code review): RED before the fix, GREEN after
// (reference/executor/T089-red.log, T089.log).

// badCursorJournal behaves like journalctl: a start with --after-cursor=<a cursor of
// another journal or garbage> exits at once without a line ("Failed to seek to cursor");
// any other start follows (the test writes to it).
type badCursorJournal struct {
	*fakeJournal
	bad string
}

func (b *badCursorJournal) start(ctx context.Context, args ...string) (io.ReadCloser, func() error, error) {
	for _, a := range args {
		if a == "--after-cursor="+b.bad {
			b.mu.Lock()
			b.starts = append(b.starts, args)
			b.procs = append(b.procs, nil)
			b.mu.Unlock()
			return io.NopCloser(strings.NewReader("")), func() error { return errors.New("exit status 1: Failed to seek to cursor: Invalid argument") }, nil
		}
	}
	return b.fakeJournal.start(ctx, args...)
}

func (b *badCursorJournal) lastFollowing(t *testing.T) (*fakeJournalProc, string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b.mu.Lock()
		for i := len(b.procs) - 1; i >= 0; i-- {
			if b.procs[i] != nil {
				p, args := b.procs[i], strings.Join(b.starts[i], " ")
				b.mu.Unlock()
				return p, args
			}
		}
		b.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("no following journalctl process was started")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// CR-2: a cursor journalctl cannot seek to (from a client, another journal, garbage) must not
// stall the shared follower: the follower drops it, starts at the end of the journal, tells
// the subscribers to reload (logs.gap{cursor_behind}) and every subscriber receives lines.
func TestT089_CR2_BadCursorDoesNotStallFollower(t *testing.T) {
	fj := newFakeJournal(t)
	bj := &badCursorJournal{fakeJournal: fj, bad: "garbage-cursor"}
	t.Cleanup(SetStartJournalFollowForTest(bj.start))
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: time.Hour, BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond}, &b)
	t.Cleanup(f.Close)

	f.Acquire("garbage-cursor")
	f.Acquire("") // another subscriber of the same follower
	p, args := bj.lastFollowing(t)
	if !strings.Contains(args, "--lines=0") || strings.Contains(args, "garbage") {
		t.Fatalf("CR-2: the restart after the failed seek = %q, want --lines=0", args)
	}
	p.write(t, entry("s=a;i=1", "live", 6))
	waitFor(t, "a live line after the bad cursor", func() bool { return len(b.lines()) == 1 })
	b.mu.Lock()
	gaps := append([]string(nil), b.gaps...)
	b.mu.Unlock()
	found := false
	for _, g := range gaps {
		if strings.HasSuffix(g, ":"+JournalGapCursorBehind) {
			found = true
		}
	}
	if !found {
		t.Fatalf("CR-2: gaps = %v, want logs.gap{cursor_behind} so the clients reload the snapshot", gaps)
	}
}

// CR-2: a follow process that keeps exiting without a line (even at the end of the journal)
// is reported once through onError (notice logs_unavailable) instead of retrying silently.
func TestT089_CR2_RepeatedEmptyExitsAreReported(t *testing.T) {
	var mu sync.Mutex
	starts := 0
	t.Cleanup(SetStartJournalFollowForTest(func(ctx context.Context, args ...string) (io.ReadCloser, func() error, error) {
		mu.Lock()
		starts++
		mu.Unlock()
		return io.NopCloser(strings.NewReader("")), func() error { return errors.New("exit status 1: No journal files were opened") }, nil
	}))
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: time.Hour, BackoffMin: 5 * time.Millisecond, BackoffMax: 10 * time.Millisecond}, &b)
	f.Acquire("")
	waitFor(t, "several empty exits", func() bool { mu.Lock(); defer mu.Unlock(); return starts >= 8 })
	f.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.errs) != 1 {
		t.Fatalf("CR-2: %d errors reported after repeated empty exits, want exactly 1: %v", len(b.errs), b.errs)
	}
	if !strings.Contains(b.errs[0].Error(), "No journal files") {
		t.Errorf("CR-2: the reported error should carry journalctl's message: %v", b.errs[0])
	}
}
