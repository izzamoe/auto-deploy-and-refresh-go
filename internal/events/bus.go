package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion is the protocol string of every frame the bus encodes.
const ProtocolVersion = "3"

// Event is one state change to publish (data-model.md "Event"). Payload must be the public
// shape of events-ws.v3.md 2.2: it is serialized once and never holds a secret (FR-005).
type Event struct {
	Topic   Topic
	Type    string
	AppID   string
	JobID   string
	UserID  string // internal: account events only reach this user; never serialized
	Origin  string // clientId of the tab that caused it (X-Admin-Client), optional
	Payload any
	TS      time.Time // zero = now
}

// envelope is the wire shape of a protocol-3 topic event (events-ws.v3.md section 1).
type envelope struct {
	Protocol string    `json:"protocol"`
	ID       string    `json:"id"`
	Seq      uint64    `json:"seq,omitempty"`
	Topic    Topic     `json:"topic"`
	Type     string    `json:"type"`
	TS       time.Time `json:"ts"`
	AppID    string    `json:"appId,omitempty"`
	JobID    string    `json:"jobId,omitempty"`
	Origin   string    `json:"origin,omitempty"`
	Payload  any       `json:"payload,omitempty"`
}

// Bus numbers state events (seq, per epoch), keeps the newest of them for resume and fans
// every event out to the clients subscribed to its topic. Publish never blocks: a client
// whose queue is full is flagged for overflow (FR-013).
type Bus struct {
	epoch string
	now   func() time.Time
	ids   atomic.Uint64 // ids of stream frames (no seq)

	mu      sync.Mutex
	seq     uint64
	ring    *RingBuffer
	clients map[*Client]struct{}
}

// NewBus starts a new epoch: a random 128-bit id that changes on every process start.
func NewBus() *Bus {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return &Bus{
		epoch:   hex.EncodeToString(b[:]),
		now:     time.Now,
		ring:    NewRingBuffer(RingCapacity, RingMaxBytes),
		clients: make(map[*Client]struct{}),
	}
}

// Epoch identifies this process (hello.epoch).
func (b *Bus) Epoch() string { return b.epoch }

// Seq is the seq of the newest state event (hello.seq; 0 before the first one).
func (b *Bus) Seq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// Register makes the client part of the fan-out (it receives nothing until it subscribes).
func (b *Bus) Register(c *Client) {
	b.mu.Lock()
	b.clients[c] = struct{}{}
	b.mu.Unlock()
}

// Unregister removes the client from the fan-out.
func (b *Bus) Unregister(c *Client) {
	b.mu.Lock()
	delete(b.clients, c)
	b.mu.Unlock()
}

// Publish encodes the event once and queues it to every client subscribed to its topic
// that may see it. A state topic gets the next seq and enters the ring buffer; a stream
// topic (logs:system) gets neither. It returns the seq (0 for a stream frame).
func (b *Bus) Publish(e Event) (uint64, error) {
	if _, ok := LookupTopic(string(e.Topic)); !ok {
		return 0, ErrUnknownTopic
	}
	ts := e.TS
	if ts.IsZero() {
		ts = b.now()
	}
	env := envelope{Protocol: ProtocolVersion, Topic: e.Topic, Type: e.Type, TS: ts.UTC(), AppID: e.AppID, JobID: e.JobID, Origin: e.Origin, Payload: e.Payload}

	b.mu.Lock()
	defer b.mu.Unlock()
	var seq uint64
	if e.Topic.Stream() {
		env.ID = "evt-" + strconv.FormatUint(b.ids.Add(1), 10)
	} else {
		seq = b.seq + 1
		env.ID = "evt-" + strconv.FormatUint(seq, 10)
		env.Seq = seq
	}
	frame, err := json.Marshal(env)
	if err != nil {
		return 0, err
	}
	if seq != 0 {
		b.seq = seq
		b.ring.push(entry{seq: seq, topic: e.Topic, userID: e.UserID, frame: frame})
	}
	for c := range b.clients {
		if _, ok := c.topics[e.Topic]; !ok || !Visible(c.Principal, e.Topic, e.UserID) {
			continue
		}
		c.enqueue(frame)
	}
	return seq, nil
}

// PublishProgress queues a compact progress frame to every client subscribed to jobs with
// progress on. encode is called at most once, only when such a client exists.
func (b *Bus) PublishProgress(jobID string, encode func() []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var frame []byte
	for c := range b.clients {
		if o, ok := c.topics[TopicJobs]; !ok || !o.Progress {
			continue
		}
		if frame == nil {
			if frame = encode(); frame == nil {
				return
			}
		}
		c.enqueue(frame)
	}
}

// Resync reasons (events-ws.v3.md 2.1).
const (
	ResyncEpoch    = "epoch"
	ResyncGap      = "gap"
	ResyncOverflow = "overflow"
)

// SubscribeResult is what a sub op produced, for the acknowledgement frames.
type SubscribeResult struct {
	// Accepted are the subscriptions now in place, in the order of the op.
	Accepted []TopicSub
	// Refused are topics the principal may not subscribe to (forbidden_topic).
	Refused []Topic
	// Added are the accepted topics that were not subscribed before.
	Added []Topic
	// With a resume: either Resumed (Replay holds the missed events, oldest first,
	// FromSeq = resume.seq, ToSeq = the bus seq) or Resync names the reason.
	Resumed      bool
	Replay       [][]byte
	FromSeq      uint64
	ToSeq        uint64
	Resync       string
	ResyncTopics []Topic
}

// Subscribe applies a sub op under the bus lock, so the replay and the live fan-out cannot
// overlap: every event with seq <= ToSeq of a newly added topic is in Replay, every later
// one goes to the client's queue (no duplicate, no gap; plan.md section 7).
func (b *Bus) Subscribe(c *Client, subs []TopicSub, resume *Resume) SubscribeResult {
	var res SubscribeResult
	for _, s := range subs {
		if Authorize(c.Principal, s.Topic) != nil {
			res.Refused = append(res.Refused, s.Topic)
			continue
		}
		res.Accepted = append(res.Accepted, s)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(res.Accepted) == 0 {
		return res
	}
	res.Added = c.setTopics(res.Accepted, nil)
	if resume == nil {
		return res
	}
	all := make([]Topic, 0, len(res.Accepted))
	for _, s := range res.Accepted {
		all = append(all, s.Topic)
	}
	switch {
	case resume.Epoch != b.epoch:
		res.Resync, res.ResyncTopics = ResyncEpoch, all
		return res
	case !b.canReplayLocked(resume.Seq):
		res.Resync, res.ResyncTopics = ResyncGap, all
		return res
	}
	added := make(map[Topic]bool, len(res.Added))
	for _, t := range res.Added {
		added[t] = true
	}
	b.ring.since(resume.Seq, func(e *entry) {
		if added[e.topic] && Visible(c.Principal, e.topic, e.userID) {
			res.Replay = append(res.Replay, e.frame)
		}
	})
	res.Resumed, res.FromSeq, res.ToSeq = true, resume.Seq, b.seq
	return res
}

// canReplayLocked reports whether every state event after `after` is still in the ring.
func (b *Bus) canReplayLocked(after uint64) bool {
	switch {
	case after > b.seq:
		return false // a seq this epoch never produced
	case after == b.seq:
		return true
	default:
		return b.ring.Len() > 0 && b.ring.OldestSeq() <= after+1
	}
}

// Unsubscribe removes topics from the client.
func (b *Bus) Unsubscribe(c *Client, topics []Topic) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c.setTopics(nil, topics)
}
