package events

import (
	"sync"
	"time"
)

// StatsTimer is the part of *time.Timer the StatsClock uses (tests inject a fake).
type StatsTimer interface {
	Stop() bool
}

// StatsClockOptions are the injected clock and data source of a StatsClock.
type StatsClockOptions struct {
	// Now is the clock (default time.Now).
	Now func() time.Time
	// WindowExit returns when the next job leaves the rolling 24-hour window of GET /stats
	// (its event time + 24 h); false when no job is in the window. Nil: no window boundary.
	WindowExit func(now time.Time) (time.Time, bool)
	// AfterFunc arms the timer (default time.AfterFunc).
	AfterFunc func(d time.Duration, f func()) StatsTimer
}

// StatsClock publishes stats.changed{reasons:["window"]} when the dashboard statistics
// change by the passing of time alone (data-model.md "StatsClock", FR-003): a job leaves
// the 24-hour window, or a day ends at midnight of a time zone a subscriber uses (default
// UTC). One timer, armed for the earliest boundary, only while topic stats has subscribers.
type StatsClock struct {
	publish func(Event)
	now     func() time.Time
	exit    func(now time.Time) (time.Time, bool)
	after   func(d time.Duration, f func()) StatsTimer

	mu    sync.Mutex
	zones map[string]int // tz -> subscribers
	locs  map[string]*time.Location
	timer StatsTimer
	next  time.Time
	gen   uint64 // a timer that fires after a reschedule is stale
}

// NewStatsClock creates an idle clock (no timer until the first Acquire).
func NewStatsClock(publish func(Event), opts StatsClockOptions) *StatsClock {
	c := &StatsClock{
		publish: publish,
		now:     opts.Now,
		exit:    opts.WindowExit,
		after:   opts.AfterFunc,
		zones:   make(map[string]int),
		locs:    make(map[string]*time.Location),
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.after == nil {
		c.after = func(d time.Duration, f func()) StatsTimer { return time.AfterFunc(d, f) }
	}
	return c
}

// windowMargin keeps the timer just past a boundary: GET /stats still counts a job whose
// event time is exactly now-24h, and the database stores whole seconds.
const windowMargin = time.Second

// Acquire counts one subscriber of topic stats in time zone tz ("" = UTC) and re-arms the
// timer.
func (c *StatsClock) Acquire(tz string) {
	tz = zoneName(tz)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.locs[tz]; !ok {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			loc = time.UTC
		}
		c.locs[tz] = loc
	}
	c.zones[tz]++
	c.scheduleLocked()
}

// Release drops one subscriber of tz; without subscribers the timer stops.
func (c *StatsClock) Release(tz string) {
	tz = zoneName(tz)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.zones[tz] <= 1 {
		delete(c.zones, tz)
	} else {
		c.zones[tz]--
	}
	c.scheduleLocked()
}

// Reschedule re-arms the timer after a job transition (the oldest job of the window may
// have changed). It does nothing without subscribers.
func (c *StatsClock) Reschedule() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.zones) == 0 {
		return
	}
	c.scheduleLocked()
}

// Next reports the armed boundary (false when the clock is idle).
func (c *StatsClock) Next() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.next, c.timer != nil
}

func (c *StatsClock) scheduleLocked() {
	c.gen++
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.next = time.Time{}
	if len(c.zones) == 0 {
		return
	}
	now := c.now()
	var next time.Time
	for tz := range c.zones {
		l := now.In(c.locs[tz])
		midnight := time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, c.locs[tz])
		if next.IsZero() || midnight.Before(next) {
			next = midnight
		}
	}
	if c.exit != nil {
		if exit, ok := c.exit(now); ok {
			exit = exit.Add(windowMargin)
			if exit.After(now) && exit.Before(next) {
				next = exit
			}
		}
	}
	c.next = next
	gen := c.gen
	c.timer = c.after(next.Sub(now), func() { c.fire(gen) })
}

func (c *StatsClock) fire(gen uint64) {
	c.mu.Lock()
	if gen != c.gen || len(c.zones) == 0 {
		c.mu.Unlock()
		return
	}
	c.timer = nil
	c.mu.Unlock()
	if c.publish != nil {
		c.publish(StatsChanged(ReasonWindow))
	}
	c.mu.Lock()
	if gen == c.gen {
		c.scheduleLocked()
	}
	c.mu.Unlock()
}

func zoneName(tz string) string {
	if tz == "" {
		return "UTC"
	}
	return tz
}
