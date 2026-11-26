package deploy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/izzamoe/auto-deploy/internal/events"
)

// JournalLine is one entry of the followed journal, rendered as text (only the stream of
// the logs:system topic uses `-o json`; the REST snapshot keeps the default format).
type JournalLine struct {
	TS       time.Time
	Priority int
	Message  string
	Cursor   string
}

// JournalFollowerOptions tune a JournalFollower (data-model.md "JournalFollower",
// research.md section 8). Zero values take the defaults.
type JournalFollowerOptions struct {
	Unit         string        // the unit to follow (auto-deploy itself)
	Batch        time.Duration // flush interval (250 ms)
	MaxBatch     int           // lines per batch (200)
	MaxLineBytes int           // a message is cut to this many bytes (4 KiB)
	RateBytes    int           // message bytes per second; beyond -> gap{rate} (64 KiB)
	MaxBuffer    int           // bytes waiting for a flush; beyond -> gap{rate} (1 MiB)
	Grace        time.Duration // the process outlives the last subscriber this long (10 s)
	Recent       int           // lines kept for late joiners (1000)
	BackoffMin   time.Duration // restart delay after the process ended (1 s, doubling)
	BackoffMax   time.Duration // (30 s)
}

// Journal gap reasons (events-ws.v3.md 2.2).
const (
	JournalGapRate         = "rate"
	JournalGapCursorBehind = "cursor_behind"
)

// JournalJoin is what a new subscriber gets besides the live stream: the recent lines after
// its cursor, or a gap when its cursor is older than what the shared process kept.
type JournalJoin struct {
	Replay []JournalLine
	Cursor string // cursor of the last replayed line
	Behind bool   // logs.gap{cursor_behind}: reload the REST snapshot
}

// JournalFollower runs at most ONE `journalctl -u <unit> -f -o json` for every subscriber
// of logs:system (Q-5=A, FR-009): started by the first Acquire, stopped Grace after the last
// Release, restarted with backoff when it ends, always waited for (no zombie). Lines are
// batched (Batch, MaxBatch), cut to MaxLineBytes and rate limited; what is dropped is
// reported as a gap.
type JournalFollower struct {
	opts    JournalFollowerOptions
	onBatch func(lines []JournalLine, cursor string)
	onGap   func(dropped int, reason string)
	onError func(err error)

	mu       sync.Mutex
	refs     int
	running  bool
	cancel   context.CancelFunc
	done     chan struct{} // closed when the run goroutine (and its process) ended
	grace    *time.Timer
	head     string
	recent   []JournalLine
	closed   bool
	startGen uint64
}

// startJournalFollow starts the follow process: its stdout and a wait func that reaps it.
// Tests replace it (SetStartJournalFollowForTest).
var startJournalFollow = func(ctx context.Context, args ...string) (io.ReadCloser, func() error, error) {
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	// T089 CR-2: what journalctl says when it exits (e.g. "Failed to seek to cursor") goes
	// with its exit error, bounded.
	stderr := &tailBuffer{max: journalStderrMax}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return out, func() error {
		err := cmd.Wait()
		if msg := strings.TrimSpace(string(stderr.Bytes())); err != nil && msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}, nil
}

const (
	// journalStderrMax bounds the stderr of journalctl kept for the log.
	journalStderrMax = 1 << 10
	// journalEmptyExitLimit consecutive exits without a single line are reported once
	// through onError (notice logs_unavailable) instead of being retried silently.
	journalEmptyExitLimit = 3
)

// SetStartJournalFollowForTest replaces the process starter of JournalFollower and returns
// a restore func.
func SetStartJournalFollowForTest(fn func(ctx context.Context, args ...string) (io.ReadCloser, func() error, error)) (restore func()) {
	original := startJournalFollow
	startJournalFollow = fn
	return func() { startJournalFollow = original }
}

// NewJournalFollower builds a stopped follower. onBatch receives each batch with the cursor
// of its last line, onGap each loss (rate) and onError a start failure (no journalctl).
func NewJournalFollower(opts JournalFollowerOptions, onBatch func([]JournalLine, string), onGap func(int, string), onError func(error)) *JournalFollower {
	if opts.Batch <= 0 {
		opts.Batch = 250 * time.Millisecond
	}
	if opts.MaxBatch <= 0 {
		opts.MaxBatch = 200
	}
	if opts.MaxLineBytes <= 0 {
		opts.MaxLineBytes = 4 << 10
	}
	if opts.RateBytes <= 0 {
		opts.RateBytes = 64 << 10
	}
	if opts.MaxBuffer <= 0 {
		opts.MaxBuffer = 1 << 20
	}
	if opts.Grace <= 0 {
		opts.Grace = 10 * time.Second
	}
	if opts.Recent <= 0 {
		opts.Recent = 1000
	}
	if opts.BackoffMin <= 0 {
		opts.BackoffMin = time.Second
	}
	if opts.BackoffMax <= 0 {
		opts.BackoffMax = 30 * time.Second
	}
	return &JournalFollower{opts: opts, onBatch: onBatch, onGap: onGap, onError: onError}
}

// Acquire adds a subscriber with the cursor it already has (after, "" = none). The first
// subscriber starts the process right after that cursor; a later one joins the running
// process and gets the recent lines after its cursor, or Behind when they are gone.
func (f *JournalFollower) Acquire(after string) JournalJoin {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs++
	if f.grace != nil {
		f.grace.Stop()
		f.grace = nil
	}
	if f.closed {
		return JournalJoin{}
	}
	if !f.running {
		f.startLocked(after)
		return JournalJoin{}
	}
	if after == "" || after == f.head {
		return JournalJoin{}
	}
	for i := range f.recent {
		if f.recent[i].Cursor == after {
			replay := append([]JournalLine(nil), f.recent[i+1:]...)
			return JournalJoin{Replay: replay, Cursor: f.head}
		}
	}
	return JournalJoin{Behind: true}
}

// Release drops a subscriber; the last one stops the process after Grace (a navigation away
// and back keeps it).
func (f *JournalFollower) Release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refs == 0 {
		return
	}
	f.refs--
	if f.refs > 0 || !f.running {
		return
	}
	gen := f.startGen
	f.grace = time.AfterFunc(f.opts.Grace, func() {
		f.mu.Lock()
		if f.refs > 0 || gen != f.startGen {
			f.mu.Unlock()
			return
		}
		f.grace = nil
		f.stopLocked()
		f.mu.Unlock()
	})
}

// Running reports whether the follow process (or its restart loop) is active.
func (f *JournalFollower) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

// Head is the cursor of the last line read.
func (f *JournalFollower) Head() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head
}

// Close stops the process now and waits for it (server shutdown).
func (f *JournalFollower) Close() {
	f.mu.Lock()
	f.closed = true
	if f.grace != nil {
		f.grace.Stop()
		f.grace = nil
	}
	done := f.done
	f.stopLocked()
	f.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (f *JournalFollower) stopLocked() {
	if !f.running {
		return
	}
	f.running = false
	f.startGen++
	f.cancel()
	events.M().LogsFollowActive.Set(0)
}

func (f *JournalFollower) startLocked(after string) {
	ctx, cancel := context.WithCancel(context.Background())
	f.running, f.cancel = true, cancel
	f.startGen++
	done := make(chan struct{})
	f.done = done
	// T089 CR-7: the new process starts after the first subscriber's cursor, or at the end of
	// the journal when it has none (never at the head of the previous process: hours of old
	// lines and a false logs.gap{rate}).
	f.head = after
	// T089 CR-6: the lines of the previous process are not continuous with the new one (the
	// lines between the stop and this start were never read): a late joiner whose cursor is
	// in them gets logs.gap{cursor_behind}, not a replay with a hole.
	f.recent = nil
	events.M().LogsFollowActive.Set(1)
	gen := f.startGen
	go func() {
		defer close(done)
		f.run(ctx, gen)
	}()
}

// run keeps one follow process alive until ctx ends, restarting it with backoff. gen is the
// start generation it belongs to: once a stop or a new start moved it, this run no longer
// writes the head or the recent lines (T089: its last flush can come after the next start).
func (f *JournalFollower) run(ctx context.Context, gen uint64) {
	backoff := f.opts.BackoffMin
	reported := false
	emptyExits := 0
	for ctx.Err() == nil {
		args := []string{"-u", f.opts.Unit, "-f", "-o", "json", "--no-pager"}
		head := f.Head()
		if head != "" {
			args = append(args, "--after-cursor="+head)
		} else {
			args = append(args, "--lines=0")
		}
		out, wait, err := startJournalFollow(ctx, args...)
		if err != nil {
			if !reported && f.onError != nil {
				f.onError(err) // e.g. journalctl not installed: tell the subscribers once
			}
			reported = true
		} else {
			readAny := f.consume(ctx, out, gen)
			_ = out.Close()
			werr := wait() // reap the process: never a zombie
			if ctx.Err() != nil {
				return
			}
			if readAny {
				backoff, reported, emptyExits = f.opts.BackoffMin, false, 0
			} else {
				emptyExits++
				// T089 CR-2: a process that ends without a single line after seeking to the
				// head cannot use that cursor (another journal, garbage): forget it, follow
				// from the end, and tell the subscribers to reload the REST snapshot.
				if head != "" {
					f.dropHead(head, gen)
				}
				if emptyExits >= journalEmptyExitLimit && !reported && f.onError != nil {
					if werr == nil {
						werr = errors.New("journalctl exited without any output")
					}
					f.onError(werr)
					reported = true
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > f.opts.BackoffMax {
			backoff = f.opts.BackoffMax
		}
	}
}

// dropHead forgets a head cursor journalctl could not continue after (T089 CR-2) and
// reports logs.gap{cursor_behind}: the subscribers reload the REST snapshot.
func (f *JournalFollower) dropHead(head string, gen uint64) {
	f.mu.Lock()
	current := f.startGen == gen
	if current && f.head == head {
		f.head = ""
	}
	f.mu.Unlock()
	if current && f.onGap != nil {
		f.onGap(0, JournalGapCursorBehind)
	}
}

// consume reads JSON entries until EOF, flushing batches every Batch; it reports whether
// any line was read.
func (f *JournalFollower) consume(ctx context.Context, out io.Reader, gen uint64) bool {
	lines := make(chan JournalLine, 64)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(lines)
		r := bufio.NewReaderSize(out, 64<<10)
		for {
			raw, err := readJournalRecord(r, f.opts.MaxBuffer)
			if len(raw) > 0 {
				if line, ok := parseJournalJSON(raw, f.opts.MaxLineBytes); ok {
					select {
					case lines <- line:
					case <-ctx.Done():
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(f.opts.Batch)
	defer ticker.Stop()
	var batch []JournalLine
	var batchBytes, dropped int
	var latest string // cursor of the last line read, kept or dropped: the restart point
	budget := f.opts.RateBytes
	lastRefill := time.Now()
	readAny := false
	flush := func() {
		if dropped > 0 {
			events.M().LogsLinesDropped.Add(int64(dropped))
			if f.onGap != nil {
				f.onGap(dropped, JournalGapRate)
			}
			dropped = 0
		}
		if len(batch) == 0 {
			if latest != "" {
				f.mu.Lock()
				if f.startGen == gen {
					f.head = latest
				}
				f.mu.Unlock()
			}
			return
		}
		cursor := batch[len(batch)-1].Cursor
		f.mu.Lock()
		current := f.startGen == gen // a stopped process's last batch is not published
		if current {
			if latest != "" {
				f.head = latest
			}
			f.recent = append(f.recent, batch...)
			if over := len(f.recent) - f.opts.Recent; over > 0 {
				f.recent = append([]JournalLine(nil), f.recent[over:]...)
			}
		}
		f.mu.Unlock()
		if current && f.onBatch != nil {
			f.onBatch(batch, cursor)
		}
		batch, batchBytes = nil, 0
	}
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				flush()
				<-readDone
				return readAny
			}
			readAny = true
			if line.Cursor != "" {
				latest = line.Cursor
			}
			now := time.Now()
			if elapsed := now.Sub(lastRefill); elapsed > 0 {
				budget += int(float64(f.opts.RateBytes) * elapsed.Seconds())
				if budget > f.opts.RateBytes {
					budget = f.opts.RateBytes
				}
				lastRefill = now
			}
			n := len(line.Message)
			if n > budget || batchBytes+n > f.opts.MaxBuffer {
				dropped++ // the line is past: a restart continues after it (latest)
				continue
			}
			budget -= n
			batch = append(batch, line)
			batchBytes += n
			if len(batch) >= f.opts.MaxBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			flush()
			<-readDone
			return readAny
		}
	}
}

// readJournalRecord reads one newline-terminated record, keeping at most limit bytes of it.
func readJournalRecord(r *bufio.Reader, limit int) ([]byte, error) {
	var rec []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(rec)+len(chunk) <= limit {
			rec = append(rec, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return rec, err
	}
}

// parseJournalJSON renders one `journalctl -o json` entry. MESSAGE is a string, or an array
// of bytes when it is not valid UTF-8 (journald), or null; the text is made valid UTF-8 and
// cut to max bytes.
func parseJournalJSON(raw []byte, max int) (JournalLine, bool) {
	var e struct {
		Cursor   string          `json:"__CURSOR"`
		Realtime string          `json:"__REALTIME_TIMESTAMP"`
		Priority string          `json:"PRIORITY"`
		Message  json.RawMessage `json:"MESSAGE"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return JournalLine{}, false
	}
	line := JournalLine{Cursor: e.Cursor, Priority: 6}
	if us, err := strconv.ParseInt(e.Realtime, 10, 64); err == nil {
		line.TS = time.UnixMicro(us).UTC()
	} else {
		line.TS = time.Now().UTC()
	}
	if p, err := strconv.Atoi(e.Priority); err == nil && p >= 0 && p <= 7 {
		line.Priority = p
	}
	var msg string
	if err := json.Unmarshal(e.Message, &msg); err != nil {
		var bytes []byte
		var nums []int
		if json.Unmarshal(e.Message, &nums) == nil {
			bytes = make([]byte, 0, len(nums))
			for _, n := range nums {
				bytes = append(bytes, byte(n))
			}
		}
		msg = string(bytes)
	}
	msg = strings.ToValidUTF8(msg, "�")
	if len(msg) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut]
	}
	line.Message = msg
	return line, true
}
