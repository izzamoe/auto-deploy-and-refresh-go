package events

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
)

// ClientQueueSize is the per-connection queue of state events (NFR-004).
const ClientQueueSize = 256

// Client is the state of one protocol-3 connection (data-model.md "Client"): its id, its
// principal, its subscriptions and the queue its writer drains.
type Client struct {
	ID        string
	Principal Principal

	queue chan []byte

	mu     sync.Mutex
	topics map[Topic]TopicOpts
}

// NewClient creates the state of a new connection with a fresh id "c-<8 hex>".
func NewClient(p Principal) *Client {
	return &Client{
		ID:        newClientID(),
		Principal: p,
		queue:     make(chan []byte, ClientQueueSize),
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
	for _, s := range subs {
		if _, ok := c.topics[s.Topic]; !ok {
			added = append(added, s.Topic)
		}
		c.topics[s.Topic] = s.Opts
	}
	for _, t := range unsubs {
		delete(c.topics, t)
	}
	return added
}

// Apply records a sub or unsub without a bus (T031: acknowledgement only).
func (c *Client) Apply(op Op) (added []Topic) { return c.setTopics(op.Subs, op.Unsubs) }
