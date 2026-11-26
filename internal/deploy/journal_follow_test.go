package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// T046 (FR-009, NFR-003, NFR-004, NFR-012, Q-5=A): ONE shared `journalctl -f -o json` for
// every subscriber of logs:system, batched every 250 ms (<= 200 lines), lines cut to 4 KiB,
// a rate limit reported as a gap, a grace period after the last subscriber, a restart with
// backoff when the process ends, no panic without journalctl, every process reaped.

// fakeJournal stands in for the journalctl process: each start gets a pipe the test writes
// JSON entries to; the "process" ends when the test ends it or the context is canceled.
type fakeJournal struct {
	mu      sync.Mutex
	starts  [][]string
	procs   []*fakeJournalProc
	reaped  int
	failErr error
	started chan struct{}
}

type fakeJournalProc struct {
	w    *io.PipeWriter
	done chan struct{}
	once sync.Once
}

func (p *fakeJournalProc) end() {
	p.once.Do(func() {
		_ = p.w.Close()
		close(p.done)
	})
}

func newFakeJournal(t *testing.T) *fakeJournal {
	t.Helper()
	f := &fakeJournal{started: make(chan struct{}, 16)}
	restore := SetStartJournalFollowForTest(f.start)
	t.Cleanup(restore)
	return f
}

func (f *fakeJournal) start(ctx context.Context, args ...string) (io.ReadCloser, func() error, error) {
	f.mu.Lock()
	f.starts = append(f.starts, args)
	if f.failErr != nil {
		err := f.failErr
		f.mu.Unlock()
		f.started <- struct{}{}
		return nil, nil, err
	}
	r, w := io.Pipe()
	p := &fakeJournalProc{w: w, done: make(chan struct{})}
	f.procs = append(f.procs, p)
	f.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done(): // exec.CommandContext kills the process
			p.end()
		case <-p.done:
		}
	}()
	f.started <- struct{}{}
	return r, func() error {
		<-p.done
		f.mu.Lock()
		f.reaped++
		f.mu.Unlock()
		return nil
	}, nil
}

func (f *fakeJournal) proc(t *testing.T, i int) *fakeJournalProc {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		if len(f.procs) > i {
			p := f.procs[i]
			f.mu.Unlock()
			return p
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("process %d never started", i)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fakeJournal) counts() (starts, reaped int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts), f.reaped
}

func (f *fakeJournal) args(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.starts[i], " ")
}

func entry(cursor, msg string, prio int) string {
	raw, _ := json.Marshal(map[string]any{
		"__CURSOR": cursor, "__REALTIME_TIMESTAMP": "1795700000000000", "PRIORITY": fmt.Sprint(prio), "MESSAGE": msg,
	})
	return string(raw) + "\n"
}

func (p *fakeJournalProc) write(t *testing.T, s string) {
	t.Helper()
	if _, err := io.WriteString(p.w, s); err != nil {
		t.Fatalf("write to the fake journal: %v", err)
	}
}

type batchLog struct {
	mu      sync.Mutex
	batches [][]JournalLine
	cursors []string
	gaps    []string
	errs    []error
	at      []time.Time
}

func (b *batchLog) onBatch(lines []JournalLine, cursor string) {
	b.mu.Lock()
	b.batches = append(b.batches, append([]JournalLine(nil), lines...))
	b.cursors = append(b.cursors, cursor)
	b.at = append(b.at, time.Now())
	b.mu.Unlock()
}

func (b *batchLog) onGap(dropped int, reason string) {
	b.mu.Lock()
	b.gaps = append(b.gaps, fmt.Sprintf("%d:%s", dropped, reason))
	b.mu.Unlock()
}

func (b *batchLog) onError(err error) {
	b.mu.Lock()
	b.errs = append(b.errs, err)
	b.mu.Unlock()
}

func (b *batchLog) lines() []JournalLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []JournalLine
	for _, batch := range b.batches {
		out = append(out, batch...)
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newFollower(opts JournalFollowerOptions, b *batchLog) *JournalFollower {
	if opts.Unit == "" {
		opts.Unit = "auto-deploy.service"
	}
	return NewJournalFollower(opts, b.onBatch, b.onGap, b.onError)
}

func TestJournalFollower_SharedProcess(t *testing.T) {
	fj := newFakeJournal(t)
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: time.Hour}, &b)
	t.Cleanup(f.Close)

	if join := f.Acquire(""); join.Behind || len(join.Replay) != 0 {
		t.Fatalf("first subscriber join = %+v", join)
	}
	p := fj.proc(t, 0)
	if args := fj.args(0); args != "-u auto-deploy.service -f -o json --no-pager --lines=0" {
		t.Fatalf("journalctl args = %q", args)
	}
	p.write(t, entry("c1", "one", 6)+entry("c2", "two", 3))
	waitFor(t, "two lines", func() bool { return len(b.lines()) == 2 })

	// A second and a third subscriber share the process; the one at the head gets nothing
	// extra, the one at c1 gets the line after c1, the one with an unknown cursor a gap.
	if join := f.Acquire("c2"); join.Behind || len(join.Replay) != 0 {
		t.Fatalf("subscriber at the head: join = %+v", join)
	}
	if join := f.Acquire("c1"); join.Behind || len(join.Replay) != 1 || join.Replay[0].Message != "two" || join.Cursor != "c2" {
		t.Fatalf("subscriber at c1: join = %+v, want [two] up to c2", join)
	}
	if join := f.Acquire("c0-gone"); !join.Behind {
		t.Fatalf("subscriber with a forgotten cursor: join = %+v, want Behind (cursor_behind)", join)
	}
	if starts, _ := fj.counts(); starts != 1 {
		t.Fatalf("%d journalctl processes for 4 subscribers, want 1", starts)
	}
	if got := f.Head(); got != "c2" {
		t.Fatalf("Head = %q, want c2", got)
	}
	lines := b.lines()
	if lines[0].Priority != 6 || lines[1].Priority != 3 || !lines[0].TS.Equal(time.UnixMicro(1795700000000000)) {
		t.Fatalf("parsed lines = %+v", lines)
	}
}

func TestJournalFollower_StopsAfterGrace(t *testing.T) {
	fj := newFakeJournal(t)
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: 100 * time.Millisecond}, &b)
	t.Cleanup(f.Close)

	f.Acquire("")
	fj.proc(t, 0)
	f.Release()
	// Back within the grace period (navigation away and back): the same process.
	time.Sleep(30 * time.Millisecond)
	f.Acquire("")
	time.Sleep(150 * time.Millisecond)
	if !f.Running() {
		t.Fatal("the process stopped although a subscriber came back within the grace")
	}
	if starts, _ := fj.counts(); starts != 1 {
		t.Fatalf("%d processes, want 1", starts)
	}
	f.Release()
	waitFor(t, "the process to stop after the grace", func() bool { return !f.Running() })
	waitFor(t, "the process to be reaped", func() bool { _, reaped := fj.counts(); return reaped == 1 })

	// A new subscriber starts a new process after the last cursor.
	fj.proc(t, 0) // (already ended)
	f.Acquire("")
	fj.proc(t, 1)
	if args := fj.args(1); !strings.Contains(args, "--lines=0") {
		t.Fatalf("restart args = %q", args)
	}
	f.Close()
	if _, reaped := fj.counts(); reaped != 2 {
		t.Fatalf("Close: %d processes reaped, want 2 (no zombie)", reaped)
	}
}

func TestJournalFollower_RateLimitGap(t *testing.T) {
	fj := newFakeJournal(t)
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: time.Hour, RateBytes: 1000}, &b)
	t.Cleanup(f.Close)
	f.Acquire("")
	p := fj.proc(t, 0)
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString(entry(fmt.Sprintf("c%d", i), strings.Repeat("x", 100), 6)) // 5000 B in a burst
	}
	p.write(t, sb.String())
	waitFor(t, "a rate gap", func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.gaps) > 0 })
	waitFor(t, "the head to pass the dropped lines", func() bool { return f.Head() == "c49" })
	kept := len(b.lines())
	b.mu.Lock()
	gap := b.gaps[0]
	b.mu.Unlock()
	var dropped int
	var reason string
	fmt.Sscanf(strings.Replace(gap, ":", " ", 1), "%d %s", &dropped, &reason)
	if reason != JournalGapRate || dropped < 30 || kept+dropped != 50 || kept > 11 {
		t.Fatalf("kept %d, gap %q; want at most ~10 lines kept (1000 B/s) and the rest reported as rate", kept, gap)
	}
}

func TestJournalFollower_LineTruncated(t *testing.T) {
	fj := newFakeJournal(t)
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: time.Hour}, &b)
	t.Cleanup(f.Close)
	f.Acquire("")
	p := fj.proc(t, 0)
	long := strings.Repeat("é", 3000) // 6000 B
	p.write(t, entry("c1", long, 6))
	waitFor(t, "one line", func() bool { return len(b.lines()) == 1 })
	msg := b.lines()[0].Message
	if len(msg) > 4096 || len(msg) < 4094 || !utf8.ValidString(msg) {
		t.Fatalf("message cut to %d bytes (valid UTF-8: %v), want <= 4096 on a rune boundary", len(msg), utf8.ValidString(msg))
	}
}

func TestJournalFollower_LatencyUnder1s(t *testing.T) {
	fj := newFakeJournal(t)
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: time.Hour}, &b)
	t.Cleanup(f.Close)
	f.Acquire("")
	p := fj.proc(t, 0)
	sent := time.Now()
	p.write(t, entry("c1", "fresh", 6))
	waitFor(t, "the line", func() bool { return len(b.lines()) == 1 })
	b.mu.Lock()
	lat := b.at[0].Sub(sent)
	b.mu.Unlock()
	if lat > time.Second {
		t.Fatalf("a line reached the subscribers after %v, want < 1 s (batch 250 ms)", lat)
	}
	// A burst larger than one batch is split into batches of at most 200 lines.
	var sb strings.Builder
	for i := 0; i < 450; i++ {
		sb.WriteString(entry(fmt.Sprintf("b%d", i), "l", 6))
	}
	p.write(t, sb.String())
	waitFor(t, "451 lines", func() bool { return len(b.lines()) == 451 })
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, batch := range b.batches {
		if len(batch) > 200 {
			t.Fatalf("batch %d has %d lines, want <= 200", i, len(batch))
		}
		if b.cursors[i] != batch[len(batch)-1].Cursor {
			t.Fatalf("batch %d cursor %q is not its last line's", i, b.cursors[i])
		}
	}
}

func TestJournalFollower_MissingBinary(t *testing.T) {
	fj := newFakeJournal(t)
	fj.failErr = &exec.Error{Name: "journalctl", Err: exec.ErrNotFound}
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: time.Hour, BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond}, &b)
	f.Acquire("")
	waitFor(t, "a few start attempts", func() bool { starts, _ := fj.counts(); return starts >= 3 })
	f.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.errs) != 1 || !errors.Is(b.errs[0], exec.ErrNotFound) {
		t.Fatalf("start errors reported = %v, want exactly one (no journalctl)", b.errs)
	}
	if len(b.batches) != 0 {
		t.Fatalf("batches without a process: %v", b.batches)
	}
}

func TestJournalFollower_ProcessExitRestartsWithBackoff(t *testing.T) {
	fj := newFakeJournal(t)
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: time.Hour, BackoffMin: 50 * time.Millisecond, BackoffMax: 200 * time.Millisecond}, &b)
	t.Cleanup(f.Close)
	f.Acquire("")
	p := fj.proc(t, 0)
	p.write(t, entry("c7", "before the crash", 6))
	waitFor(t, "the line", func() bool { return len(b.lines()) == 1 })
	ended := time.Now()
	p.end() // journalctl died
	p2 := fj.proc(t, 1)
	if wait := time.Since(ended); wait < 40*time.Millisecond {
		t.Fatalf("restarted after %v, want the backoff first", wait)
	}
	if args := fj.args(1); !strings.Contains(args, "--after-cursor=c7") || strings.Contains(args, "--lines=0") {
		t.Fatalf("restart args = %q, want --after-cursor=c7", args)
	}
	p2.write(t, entry("c8", "after", 6))
	waitFor(t, "the line after the restart", func() bool { return len(b.lines()) == 2 })
	if _, reaped := fj.counts(); reaped != 1 {
		t.Fatalf("the dead process was not reaped (%d)", reaped)
	}
}

func TestJournalFollower_NonUTF8Message(t *testing.T) {
	fj := newFakeJournal(t)
	var b batchLog
	f := newFollower(JournalFollowerOptions{Grace: time.Hour}, &b)
	t.Cleanup(f.Close)
	f.Acquire("")
	p := fj.proc(t, 0)
	// journald sends a MESSAGE that is not valid UTF-8 as an array of bytes, and may send
	// null; a broken JSON line is skipped.
	p.write(t, `{"__CURSOR":"c1","__REALTIME_TIMESTAMP":"1795700000000000","PRIORITY":"4","MESSAGE":[104,105,255,33]}`+"\n")
	p.write(t, `{"__CURSOR":"c2","PRIORITY":"9","MESSAGE":null}`+"\n")
	p.write(t, "{not json\n")
	p.write(t, entry("c3", "ok", 6))
	waitFor(t, "three lines", func() bool { return len(b.lines()) == 3 })
	lines := b.lines()
	if lines[0].Message != "hi�!" || lines[0].Priority != 4 {
		t.Fatalf("byte-array MESSAGE = %q prio %d, want \"hi\\uFFFD!\" prio 4", lines[0].Message, lines[0].Priority)
	}
	if lines[1].Message != "" || lines[1].Priority != 6 {
		t.Fatalf("null MESSAGE / bad PRIORITY = %+v, want empty text, priority 6", lines[1])
	}
	if lines[2].Message != "ok" || f.Head() != "c3" {
		t.Fatalf("line after the broken one = %+v head %q", lines[2], f.Head())
	}
}
