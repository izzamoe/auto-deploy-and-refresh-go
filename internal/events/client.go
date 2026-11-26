package events

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ClientQueueSize is the per-connection queue of state events (NFR-004).
const ClientQueueSize = 256

// Client is the state of one protocol-3 connection (data-model.md "Client"): its id, its
// principal, its subscriptions and the queue its writer drains.
type Client struct {
	ID        string
	Principal Principal

	queue    chan []byte
	overflow atomic.Bool
	wake     chan struct{}

	// topics is written only with both the bus lock and mu held, so the fan-out (bus lock)
	// and the accessors below (mu) can each read it.
	mu            sync.Mutex
	topics        map[Topic]TopicOpts
	overflowTimes []time.Time // the last OverflowLimit overflows

	pmu      sync.Mutex
	progress progressSlots
}

// NewClient creates the state of a new connection with a fresh id "c-<8 hex>".
func NewClient(p Principal) *Client {
	return &Client{
		ID:        newClientID(),
		Principal: p,
		queue:     make(chan []byte, ClientQueueSize),
		wake:      make(chan struct{}, 1),
		topics:    make(map[Topic]TopicOpts),
	}
}

func newClientID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "c-" + hex.EncodeToString(b[:])
}

// Queue is what the connection writer drains, in order.
func (c *Client) Queue() <-chan []byte { return c.queue }

// Subscribed reports whether the client subscribes to t and with which options.
func (c *Client) Subscribed(t Topic) (TopicOpts, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.topics[t]
	return o, ok
}

// TopicList returns the subscribed topics in registry order.
func (c *Client) TopicList() []Topic {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.topicListLocked()
}

func (c *Client) topicListLocked() []Topic {
	out := make([]Topic, 0, len(c.topics))
	for t := range c.topics {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return topicRank(out[i]) < topicRank(out[j]) })
	return out
}

func topicRank(t Topic) int {
	for i, x := range topicOrder {
		if x == t {
			return i
		}
	}
	return len(topicOrder)
}

// setTopics adds subs and removes unsubs; it returns the topics that were not subscribed
// before (newly added).
func (c *Client) setTopics(subs []TopicSub, unsubs []Topic) (added []Topic) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dropProgress := false
	for _, s := range subs {
		if _, ok := c.topics[s.Topic]; !ok {
			added = append(added, s.Topic)
		}
		c.topics[s.Topic] = s.Opts
		if s.Topic == TopicJobs && !s.Opts.Progress {
			dropProgress = true // jobs{progress:false}: a hidden tab stops the stream (FR-018)
		}
	}
	for _, t := range unsubs {
		delete(c.topics, t)
		if t == TopicJobs {
			dropProgress = true
		}
	}
	if dropProgress {
		c.clearProgress()
	}
	return added
}

// enqueue never blocks: when the queue is full the frame is dropped, the client is flagged
// for overflow and its writer is woken (it then resyncs the client, FR-013).
func (c *Client) enqueue(frame []byte) {
	select {
	case c.queue <- frame:
	default:
		c.overflow.Store(true)
		c.signal()
	}
}

func (c *Client) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Wake is signalled when the writer has work besides the queue (overflow; progress, T034).
func (c *Client) Wake() <-chan struct{} { return c.wake }

// Overflowed reports whether frames were dropped since the last TakeOverflow.
func (c *Client) Overflowed() bool { return c.overflow.Load() }

// Overflow policy of events-ws.v3.md section 6: every overflow empties the queue and sends
// one resync{overflow}; the OverflowLimit-th overflow within OverflowWindow closes 4408.
const (
	OverflowLimit  = 3
	OverflowWindow = 60 * time.Second
)

// TakeOverflow is called by the writer. When the client overflowed it clears the flag,
// empties the queue (the frames there are older than the resync about to be sent) and
// reports lagging=true on the OverflowLimit-th overflow within OverflowWindow.
func (c *Client) TakeOverflow(now time.Time) (overflowed, lagging bool) {
	if !c.overflow.CompareAndSwap(true, false) {
		return false, false
	}
	for drained := false; !drained; {
		select {
		case <-c.queue:
		default:
			drained = true
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.overflowTimes = append(c.overflowTimes, now)
	if len(c.overflowTimes) > OverflowLimit {
		c.overflowTimes = c.overflowTimes[len(c.overflowTimes)-OverflowLimit:]
	}
	lagging = len(c.overflowTimes) == OverflowLimit && now.Sub(c.overflowTimes[0]) < OverflowWindow
	return true, lagging
}
