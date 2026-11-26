package events

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// T043 (FR-003): stats.changed{reasons:["window"]} when time alone changes GET /stats: a
// job leaving the 24-hour window or midnight in a subscriber's zone. One timer, armed only
// while topic stats has subscribers; the clock is injected.

type fakeStatsTimers struct {
	mu     sync.Mutex
	armed  []*fakeStatsTimer
	active int
}

type fakeStatsTimer struct {
	owner   *fakeStatsTimers
	d       time.Duration
	f       func()
	stopped bool
}

func (t *fakeStatsTimer) Stop() bool {
	t.owner.mu.Lock()
	defer t.owner.mu.Unlock()
	if t.stopped {
		return false
	}
	t.stopped = true
	t.owner.active--
	return true
}

func (ts *fakeStatsTimers) after(d time.Duration, f func()) StatsTimer {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := &fakeStatsTimer{owner: ts, d: d, f: f}
	ts.armed = append(ts.armed, t)
	ts.active++
	return t
}

// last returns the newest timer that was not stopped.
func (ts *fakeStatsTimers) last(t *testing.T) *fakeStatsTimer {
	t.Helper()
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for i := len(ts.armed) - 1; i >= 0; i-- {
		if !ts.armed[i].stopped {
			return ts.armed[i]
		}
	}
	t.Fatal("no armed timer")
	return nil
}

func (ts *fakeStatsTimers) count() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.active
}

// fireLast runs the newest armed timer as if its time had come.
func (ts *fakeStatsTimers) fireLast(t *testing.T) {
	t.Helper()
	tm := ts.last(t)
	ts.mu.Lock()
	tm.stopped = true
	ts.active--
	ts.mu.Unlock()
	tm.f()
}

type statsClockEnv struct {
	mu        sync.Mutex
	now       time.Time
	exit      time.Time
	hasExit   bool
	timers    *fakeStatsTimers
	published []Event
	clock     *StatsClock
}

func newStatsClockEnv(now time.Time) *statsClockEnv {
	e := &statsClockEnv{now: now, timers: &fakeStatsTimers{}}
	e.clock = NewStatsClock(func(ev Event) {
		e.mu.Lock()
		e.published = append(e.published, ev)
		e.mu.Unlock()
	}, StatsClockOptions{
		Now: func() time.Time {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.now
		},
		WindowExit: func(time.Time) (time.Time, bool) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.exit, e.hasExit
		},
		AfterFunc: e.timers.after,
	})
	return e
}

func (e *statsClockEnv) set(now time.Time) {
	e.mu.Lock()
	e.now = now
	e.mu.Unlock()
}

func (e *statsClockEnv) setExit(exit time.Time, ok bool) {
	e.mu.Lock()
	e.exit, e.hasExit = exit, ok
	e.mu.Unlock()
}

func (e *statsClockEnv) events() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.published...)
}

func wantWindowEvent(t *testing.T, evs []Event, n int) {
	t.Helper()
	if len(evs) != n {
		t.Fatalf("published %d events, want %d: %+v", len(evs), n, evs)
	}
	for _, ev := range evs {
		raw, _ := json.Marshal(ev.Payload)
		if ev.Topic != TopicStats || ev.Type != TypeStatsChanged || string(raw) != `{"reasons":["window"]}` {
			t.Fatalf("event = %s/%s %s, want stats/stats.changed {\"reasons\":[\"window\"]}", ev.Topic, ev.Type, raw)
		}
	}
}

func TestStatsClock_FiresWhenOldestJobLeaves24h(t *testing.T) {
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	e := newStatsClockEnv(now)
	// The oldest job of the window was created at 15:00 yesterday: it leaves at 15:00 today,
	// before midnight UTC.
	e.setExit(now.Add(5*time.Hour), true)
	e.clock.Acquire("")
	next, armed := e.clock.Next()
	if !armed || !next.Equal(now.Add(5*time.Hour+windowMargin)) {
		t.Fatalf("Next = %v %v, want %v (window exit + margin)", next, armed, now.Add(5*time.Hour+windowMargin))
	}
	if d := e.timers.last(t).d; d != 5*time.Hour+windowMargin {
		t.Fatalf("timer armed for %v, want 5h1s", d)
	}

	// The time comes: one stats.changed{window}; the next job leaves at 18:00.
	e.set(now.Add(5*time.Hour + windowMargin))
	e.setExit(now.Add(8*time.Hour), true)
	e.timers.fireLast(t)
	wantWindowEvent(t, e.events(), 1)
	if next, _ := e.clock.Next(); !next.Equal(now.Add(8*time.Hour + windowMargin)) {
		t.Fatalf("after firing Next = %v, want the next exit %v", next, now.Add(8*time.Hour+windowMargin))
	}

	// A job transition moves the boundary: Reschedule re-arms, the old timer is stale.
	stale := e.timers.last(t)
	e.setExit(now.Add(6*time.Hour), true)
	e.clock.Reschedule()
	if !stale.stopped {
		t.Fatal("Reschedule did not stop the previous timer")
	}
	stale.f() // a timer that fired while being stopped must not publish
	wantWindowEvent(t, e.events(), 1)
	if e.timers.count() != 1 {
		t.Fatalf("%d timers armed, want exactly one", e.timers.count())
	}
}

func TestStatsClock_FiresAtMidnightTZ(t *testing.T) {
	// 20:00 UTC = 03:00 next day in Jakarta (UTC+7): Jakarta's midnight is 17:00 UTC
	// tomorrow, UTC's midnight is in 4 h.
	now := time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC)
	e := newStatsClockEnv(now)
	e.clock.Acquire("Asia/Jakarta")
	jakarta, _ := time.LoadLocation("Asia/Jakarta")
	wantJKT := time.Date(2026, 11, 28, 0, 0, 0, 0, jakarta)
	if next, armed := e.clock.Next(); !armed || !next.Equal(wantJKT) {
		t.Fatalf("Next = %v %v, want Jakarta midnight %v", next, armed, wantJKT)
	}
	// A second subscriber in UTC: the earliest midnight wins.
	e.clock.Acquire("UTC")
	wantUTC := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	if next, _ := e.clock.Next(); !next.Equal(wantUTC) {
		t.Fatalf("Next with a UTC subscriber = %v, want %v", next, wantUTC)
	}
	e.set(wantUTC)
	e.timers.fireLast(t)
	wantWindowEvent(t, e.events(), 1)
	if next, _ := e.clock.Next(); !next.Equal(wantJKT) {
		t.Fatalf("after UTC midnight Next = %v, want Jakarta midnight %v", next, wantJKT)
	}
	// The UTC subscriber leaves: Jakarta's midnight stays armed.
	e.clock.Release("UTC")
	if next, armed := e.clock.Next(); !armed || !next.Equal(wantJKT) {
		t.Fatalf("after Release(UTC) Next = %v %v, want %v", next, armed, wantJKT)
	}
	e.set(wantJKT)
	e.timers.fireLast(t)
	wantWindowEvent(t, e.events(), 2)
	if next, _ := e.clock.Next(); !next.Equal(wantJKT.AddDate(0, 0, 1)) {
		t.Fatalf("after Jakarta midnight Next = %v, want the next one", next)
	}
}

func TestStatsClock_StopsWithoutSubscribers(t *testing.T) {
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	e := newStatsClockEnv(now)
	e.setExit(now.Add(time.Hour), true)

	// Idle: no timer, Reschedule (a job transition) arms nothing.
	e.clock.Reschedule()
	if _, armed := e.clock.Next(); armed || e.timers.count() != 0 {
		t.Fatalf("an idle clock armed a timer (%d)", e.timers.count())
	}

	e.clock.Acquire("UTC")
	e.clock.Acquire("UTC")
	if e.timers.count() != 1 {
		t.Fatalf("%d timers for two subscribers, want 1", e.timers.count())
	}
	e.clock.Release("UTC")
	if _, armed := e.clock.Next(); !armed {
		t.Fatal("the timer stopped while a subscriber is left")
	}
	held := e.timers.last(t)
	e.clock.Release("UTC")
	if _, armed := e.clock.Next(); armed || e.timers.count() != 0 {
		t.Fatalf("the clock still runs without subscribers (%d timers)", e.timers.count())
	}
	held.f() // a timer that raced the last Release publishes nothing
	if evs := e.events(); len(evs) != 0 {
		t.Fatalf("published %+v without subscribers", evs)
	}
	e.clock.Release("UTC") // an extra Release is harmless
	if _, armed := e.clock.Next(); armed {
		t.Fatal("an extra Release armed a timer")
	}
}

func TestStatsClock_RealTimerFires(t *testing.T) {
	// With the default timer: a boundary 50 ms away publishes once.
	var mu sync.Mutex
	var got []Event
	done := make(chan struct{}, 1)
	start := time.Now()
	c := NewStatsClock(func(ev Event) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	}, StatsClockOptions{WindowExit: func(time.Time) (time.Time, bool) {
		return start.Add(50 * time.Millisecond).Add(-windowMargin), true
	}})
	c.Acquire("UTC")
	defer c.Release("UTC")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the armed timer never fired")
	}
	mu.Lock()
	defer mu.Unlock()
	wantWindowEvent(t, got[:1], 1)
}
