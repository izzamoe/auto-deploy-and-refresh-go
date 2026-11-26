package events

import (
	"testing"
	"time"
)

// T033 (FR-013, SC-010): a slow client is never dropped silently. A full queue drops the
// frame, flags the client and wakes its writer without blocking the publisher; the writer
// empties the queue and resyncs; the third overflow within 60 s means 4408.

func TestClient_OverflowNeverBlocksAndFlags(t *testing.T) {
	b := NewBus()
	c := NewClient(session("u1"))
	b.Register(c)
	b.Subscribe(c, sub(TopicApps), nil)

	done := make(chan struct{})
	go func() {
		for i := 0; i < ClientQueueSize+50; i++ {
			publish(t, b, TopicApps, "a")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a client that does not read")
	}
	if !c.Overflowed() {
		t.Fatal("a full queue must flag the client for overflow")
	}
	select {
	case <-c.Wake():
	default:
		t.Fatal("overflow must wake the writer")
	}
	if n := len(c.queue); n != ClientQueueSize {
		t.Fatalf("queue holds %d, want %d (the cap of NFR-004)", n, ClientQueueSize)
	}
}

func TestClient_OverflowEmptiesQueueThenLaggingOnThird(t *testing.T) {
	c := NewClient(session("u1"))
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	fill := func() {
		for i := 0; i < ClientQueueSize+1; i++ {
			c.enqueue([]byte("x"))
		}
	}

	if over, _ := c.TakeOverflow(t0); over {
		t.Fatal("TakeOverflow without an overflow must report nothing")
	}
	fill()
	over, lagging := c.TakeOverflow(t0)
	if !over || lagging {
		t.Fatalf("first overflow: overflowed=%v lagging=%v, want true/false (resync)", over, lagging)
	}
	if len(c.queue) != 0 || c.Overflowed() {
		t.Fatalf("after TakeOverflow: queue %d, flag %v; want empty and cleared", len(c.queue), c.Overflowed())
	}
	fill()
	if over, lagging = c.TakeOverflow(t0.Add(20 * time.Second)); !over || lagging {
		t.Fatalf("second overflow: %v/%v, want resync", over, lagging)
	}
	fill()
	if over, lagging = c.TakeOverflow(t0.Add(59 * time.Second)); !over || !lagging {
		t.Fatalf("third overflow within 60 s: %v/%v, want lagging (4408)", over, lagging)
	}
}

func TestClient_OverflowWindowSlides(t *testing.T) {
	c := NewClient(session("u1"))
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	for i, at := range []time.Duration{0, 30 * time.Second, 61 * time.Second, 89 * time.Second} {
		for j := 0; j < ClientQueueSize+1; j++ {
			c.enqueue([]byte("x"))
		}
		_, lagging := c.TakeOverflow(t0.Add(at))
		want := i == 3 // 30 s, 61 s, 89 s are three within 60 s; 0, 30, 61 are not
		if lagging != want {
			t.Fatalf("overflow #%d at +%v: lagging=%v, want %v", i+1, at, lagging, want)
		}
	}
}

// After a resync the client gets the live events again.
func TestClient_OverflowThenLiveAgain(t *testing.T) {
	b := NewBus()
	c := NewClient(session("u1"))
	b.Register(c)
	b.Subscribe(c, sub(TopicApps), nil)
	for i := 0; i < ClientQueueSize+10; i++ {
		publish(t, b, TopicApps, "a")
	}
	if over, _ := c.TakeOverflow(time.Now()); !over {
		t.Fatal("no overflow reported")
	}
	next := publish(t, b, TopicApps, "after")
	if got := seqs(t, drainQueue(c)); len(got) != 1 || got[0] != next {
		t.Fatalf("after the resync the queue holds %v, want only the new event %d", got, next)
	}
}
