package events

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// T032 (FR-011, FR-012, NFR-004): seq/epoch, ring buffer, resume and resync.

type wireEvent struct {
	Seq   uint64 `json:"seq"`
	Topic Topic  `json:"topic"`
	Type  string `json:"type"`
	AppID string `json:"appId"`
	ID    string `json:"id"`
}

func decode(t *testing.T, frame []byte) wireEvent {
	t.Helper()
	var w wireEvent
	if err := json.Unmarshal(frame, &w); err != nil {
		t.Fatalf("frame is not JSON: %s", frame)
	}
	return w
}

func seqs(t *testing.T, frames [][]byte) []uint64 {
	t.Helper()
	out := make([]uint64, 0, len(frames))
	for _, f := range frames {
		out = append(out, decode(t, f).Seq)
	}
	return out
}

func session(user string) Principal {
	return Principal{Kind: PrincipalSession, UserID: user, SessionID: "s-" + user}
}

func sub(topics ...Topic) []TopicSub {
	out := make([]TopicSub, 0, len(topics))
	for _, t := range topics {
		out = append(out, TopicSub{Topic: t, Opts: TopicOpts{Progress: t == TopicJobs}})
	}
	return out
}

func publish(t *testing.T, b *Bus, topic Topic, appID string) uint64 {
	t.Helper()
	seq, err := b.Publish(Event{Topic: topic, Type: "fixture", AppID: appID, Payload: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// drainQueue returns what is queued right now.
func drainQueue(c *Client) [][]byte {
	var out [][]byte
	for {
		select {
		case f := <-c.queue:
			out = append(out, f)
		default:
			return out
		}
	}
}

func TestBus_ResumeReplaysMissedInOrder(t *testing.T) {
	b := NewBus()
	publish(t, b, TopicApps, "before")
	from := b.Seq()
	want := []uint64{
		publish(t, b, TopicApps, "a1"),
		publish(t, b, TopicApps, "a2"),
	}
	publish(t, b, TopicStats, "") // another topic: not replayed
	want = append(want, publish(t, b, TopicApps, "a3"))

	c := NewClient(session("u1"))
	b.Register(c)
	res := b.Subscribe(c, sub(TopicApps), &Resume{Epoch: b.Epoch(), Seq: from})
	if !res.Resumed || res.Resync != "" {
		t.Fatalf("resume within the ring: Resumed=%v Resync=%q, want a replay", res.Resumed, res.Resync)
	}
	if got := seqs(t, res.Replay); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("replayed seqs = %v, want %v (apps only, in order)", got, want)
	}
	if res.FromSeq != from || res.ToSeq != b.Seq() {
		t.Errorf("resumed fromSeq/toSeq = %d/%d, want %d/%d", res.FromSeq, res.ToSeq, from, b.Seq())
	}
	// After the replay the live events follow on the queue.
	live := publish(t, b, TopicApps, "a4")
	if got := seqs(t, drainQueue(c)); fmt.Sprint(got) != fmt.Sprint([]uint64{live}) {
		t.Errorf("live after resume = %v, want [%d]", got, live)
	}
}

// RV-9: a client of the previous process (old epoch) gets resync{epoch}; the new process
// counts seq from 1 again, so the hello.seq a new socket sees is the new bus's Seq.
func TestBus_ResyncOnEpochChange(t *testing.T) {
	old := NewBus()
	for i := 0; i < 10; i++ {
		publish(t, old, TopicApps, "a")
	}
	oldEpoch, oldSeq := old.Epoch(), old.Seq()

	restarted := NewBus() // self-deploy / restart
	if restarted.Epoch() == oldEpoch || len(restarted.Epoch()) != 32 {
		t.Fatalf("epoch after restart = %q (old %q), want a new 32-hex id", restarted.Epoch(), oldEpoch)
	}
	publish(t, restarted, TopicApps, "b")
	publish(t, restarted, TopicApps, "b")
	if got := restarted.Seq(); got != 2 {
		t.Fatalf("hello.seq after restart = %d, want 2 (seq restarts at 1 per epoch)", got)
	}
	c := NewClient(session("u1"))
	restarted.Register(c)
	res := restarted.Subscribe(c, sub(TopicApps, TopicSummary), &Resume{Epoch: oldEpoch, Seq: oldSeq})
	if res.Resync != ResyncEpoch || res.Resumed || len(res.Replay) != 0 {
		t.Fatalf("resume with the old epoch: Resync=%q Resumed=%v replay=%d, want resync epoch", res.Resync, res.Resumed, len(res.Replay))
	}
	if fmt.Sprint(res.ResyncTopics) != "[apps summary]" {
		t.Errorf("resync topics = %v, want every topic of the sub", res.ResyncTopics)
	}
	// Events after the restart reach the client although their seq (3) is below the old 10.
	next := publish(t, restarted, TopicApps, "c")
	if got := seqs(t, drainQueue(c)); fmt.Sprint(got) != fmt.Sprint([]uint64{next}) || next != 3 {
		t.Errorf("live after resync = %v, want [3]", got)
	}
}

func TestBus_ResyncOnGap(t *testing.T) {
	b := NewBus()
	for i := 0; i < RingCapacity+100; i++ {
		publish(t, b, TopicStats, "")
	}
	c := NewClient(session("u1"))
	b.Register(c)
	res := b.Subscribe(c, sub(TopicStats), &Resume{Epoch: b.Epoch(), Seq: 0})
	if res.Resync != ResyncGap || len(res.Replay) != 0 {
		t.Fatalf("resume older than the ring: Resync=%q replay=%d, want gap", res.Resync, len(res.Replay))
	}
	// The oldest event still held is replayable from oldest-1.
	c2 := NewClient(session("u1"))
	b.Register(c2)
	res = b.Subscribe(c2, sub(TopicStats), &Resume{Epoch: b.Epoch(), Seq: b.ring.OldestSeq() - 1})
	if !res.Resumed || len(res.Replay) != RingCapacity {
		t.Fatalf("resume at oldest-1: Resumed=%v replay=%d, want %d", res.Resumed, len(res.Replay), RingCapacity)
	}
	// A seq this epoch never produced is a gap too.
	c3 := NewClient(session("u1"))
	b.Register(c3)
	if res = b.Subscribe(c3, sub(TopicStats), &Resume{Epoch: b.Epoch(), Seq: b.Seq() + 5}); res.Resync != ResyncGap {
		t.Fatalf("resume from the future: Resync=%q, want gap", res.Resync)
	}
}

// Race: events are published concurrently while the client resumes; the replay plus the
// queue must hold every seq after resume.seq exactly once and in order.
func TestBus_NoDuplicateBetweenReplayAndLive(t *testing.T) {
	for round := 0; round < 20; round++ {
		b := NewBus()
		const total = 400
		for i := 0; i < 50; i++ {
			publish(t, b, TopicApps, "pre")
		}
		from := b.Seq()

		c := NewClient(session("u1"))
		b.Register(c)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < total; i++ {
				if _, err := b.Publish(Event{Topic: TopicApps, Type: "fixture", AppID: "x"}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		var live [][]byte
		var lmu sync.Mutex
		done := make(chan struct{})
		go func() { // the writer: drains the queue concurrently
			for {
				select {
				case f := <-c.queue:
					lmu.Lock()
					live = append(live, f)
					lmu.Unlock()
				case <-done:
					return
				}
			}
		}()
		res := b.Subscribe(c, sub(TopicApps), &Resume{Epoch: b.Epoch(), Seq: from})
		wg.Wait()
		deadline := time.Now().Add(2 * time.Second)
		for {
			lmu.Lock()
			n := len(live)
			lmu.Unlock()
			if uint64(len(res.Replay)+n) >= b.Seq()-from || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		close(done)
		if c.overflow.Load() {
			t.Fatal("the client queue overflowed; the test cannot judge ordering")
		}
		lmu.Lock()
		got := append(seqs(t, res.Replay), seqs(t, live)...)
		lmu.Unlock()
		want := b.Seq() - from
		if uint64(len(got)) != want {
			t.Fatalf("round %d: replay %d + live %d = %d events, want %d (seq %d..%d)", round, len(res.Replay), len(got)-len(res.Replay), len(got), want, from+1, b.Seq())
		}
		for i, s := range got {
			if s != from+uint64(i)+1 {
				t.Fatalf("round %d: position %d has seq %d, want %d (duplicate or gap at the replay/live seam %d)", round, i, s, from+uint64(i)+1, res.ToSeq)
			}
		}
	}
}

func TestBus_ReplayRespectsAccountFilter(t *testing.T) {
	b := NewBus()
	from := b.Seq()
	mine, _ := b.Publish(Event{Topic: TopicAccount, Type: "account.changed", UserID: "u1", Payload: map[string]string{"section": "tokens"}})
	_, _ = b.Publish(Event{Topic: TopicAccount, Type: "account.changed", UserID: "u2", Payload: map[string]string{"section": "tokens"}})
	_, _ = b.Publish(Event{Topic: TopicSettings, Type: "settings.changed", Payload: map[string]string{"section": "github"}})

	c := NewClient(session("u1"))
	b.Register(c)
	res := b.Subscribe(c, sub(TopicAccount), &Resume{Epoch: b.Epoch(), Seq: from})
	if got := seqs(t, res.Replay); fmt.Sprint(got) != fmt.Sprint([]uint64{mine}) {
		t.Fatalf("FR-017: replay of account for u1 = %v, want only u1's event %d", got, mine)
	}
	for _, f := range res.Replay {
		if strings.Contains(string(f), "u1") || strings.Contains(string(f), "u2") {
			t.Errorf("the internal userId was serialized: %s", f)
		}
	}

	// An API token may not subscribe to account or settings: nothing is replayed, both refused.
	tok := NewClient(Principal{Kind: PrincipalToken, UserID: "u1"})
	b.Register(tok)
	res = b.Subscribe(tok, sub(TopicAccount, TopicSettings, TopicApps), &Resume{Epoch: b.Epoch(), Seq: from})
	if fmt.Sprint(res.Refused) != "[account settings]" {
		t.Errorf("token refused topics = %v, want [account settings]", res.Refused)
	}
	if len(res.Replay) != 0 {
		t.Errorf("token replay = %d frames, want 0 (no apps event, forbidden topics never replayed)", len(res.Replay))
	}
	_, _ = b.Publish(Event{Topic: TopicAccount, Type: "account.changed", UserID: "u1", Payload: map[string]string{"section": "profile"}})
	if q := drainQueue(tok); len(q) != 0 {
		t.Errorf("token client received %d live account frames, want 0", len(q))
	}
	// The live fan-out filters per user too.
	if q := drainQueue(c); len(q) != 1 {
		t.Errorf("u1 live account frames = %d, want 1", len(q))
	}
}

// RV2-13: re-subscribing to a topic left earlier on the SAME connection resumes it.
func TestBus_ResumeOnResubscribeSameConnection(t *testing.T) {
	b := NewBus()
	c := NewClient(session("u1"))
	b.Register(c)
	b.Subscribe(c, sub(TopicApps, TopicSummary), nil)
	last := publish(t, b, TopicApps, "a1")
	drainQueue(c)

	b.Unsubscribe(c, []Topic{TopicApps})
	missed := []uint64{publish(t, b, TopicApps, "a2"), publish(t, b, TopicApps, "a3")}
	if q := drainQueue(c); len(q) != 0 {
		t.Fatalf("an unsubscribed topic still delivered %d frames", len(q))
	}
	res := b.Subscribe(c, sub(TopicApps), &Resume{Epoch: b.Epoch(), Seq: last})
	if !res.Resumed || fmt.Sprint(seqs(t, res.Replay)) != fmt.Sprint(missed) {
		t.Fatalf("re-sub with resume: Resumed=%v replay=%v, want %v", res.Resumed, seqs(t, res.Replay), missed)
	}

	// The same re-sub when the gap left the ring: resync for that topic only.
	b.Unsubscribe(c, []Topic{TopicApps})
	for i := 0; i < RingCapacity+1; i++ {
		publish(t, b, TopicStats, "")
	}
	res = b.Subscribe(c, sub(TopicApps), &Resume{Epoch: b.Epoch(), Seq: last})
	if res.Resync != ResyncGap || fmt.Sprint(res.ResyncTopics) != "[apps]" {
		t.Fatalf("re-sub after the ring moved on: Resync=%q topics=%v, want gap [apps]", res.Resync, res.ResyncTopics)
	}
}

func TestRing_BoundedBytes(t *testing.T) {
	r := NewRingBuffer(RingCapacity, RingMaxBytes)
	big := make([]byte, 2048)
	for i := 1; i <= 2000; i++ {
		r.push(entry{seq: uint64(i), topic: TopicApps, frame: big})
		if r.Bytes() > RingMaxBytes {
			t.Fatalf("after seq %d the ring holds %d bytes > %d", i, r.Bytes(), RingMaxBytes)
		}
	}
	if r.Len() != RingMaxBytes/2048 {
		t.Errorf("2 KiB entries held = %d, want %d (byte bound first)", r.Len(), RingMaxBytes/2048)
	}
	var prev uint64
	n := 0
	r.since(0, func(e *entry) {
		if prev != 0 && e.seq != prev+1 {
			t.Fatalf("hole in the ring: %d after %d", e.seq, prev)
		}
		prev = e.seq
		n++
	})
	if prev != 2000 || r.OldestSeq() != 2000-uint64(n)+1 {
		t.Errorf("newest/oldest = %d/%d over %d entries", prev, r.OldestSeq(), n)
	}

	small := NewRingBuffer(RingCapacity, RingMaxBytes)
	for i := 1; i <= 3000; i++ {
		small.push(entry{seq: uint64(i), frame: []byte("x")})
	}
	if small.Len() != RingCapacity || small.OldestSeq() != 3000-RingCapacity+1 {
		t.Errorf("count bound: len %d oldest %d", small.Len(), small.OldestSeq())
	}

	// One frame larger than the whole bound is still kept alone (never an empty ring after a push).
	huge := NewRingBuffer(4, 10)
	huge.push(entry{seq: 1, frame: []byte("12345")})
	huge.push(entry{seq: 2, frame: make([]byte, 20)})
	if huge.Len() != 1 || huge.OldestSeq() != 2 {
		t.Errorf("oversized frame: len %d oldest %d, want the new frame alone", huge.Len(), huge.OldestSeq())
	}
}
