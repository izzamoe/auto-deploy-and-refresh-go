package events

// Ring buffer bounds of data-model.md "RingBuffer" (NFR-004).
const (
	RingCapacity = 1024
	RingMaxBytes = 1 << 20
)

// entry is one encoded state event kept for resume.
type entry struct {
	seq    uint64
	topic  Topic
	userID string
	frame  []byte
}

// RingBuffer keeps the newest state events in seq order, bounded by entry count AND by the
// total size of the encoded frames: when either bound would be exceeded the oldest entries
// are dropped first. Invariant: entries are sorted by seq with no hole between OldestSeq
// and the newest seq. Not safe for concurrent use (the bus holds its lock).
type RingBuffer struct {
	buf      []entry
	head     int // index of the oldest entry
	n        int
	bytes    int
	maxBytes int
}

// NewRingBuffer returns a ring of capacity entries and at most maxBytes encoded bytes.
func NewRingBuffer(capacity, maxBytes int) *RingBuffer {
	return &RingBuffer{buf: make([]entry, capacity), maxBytes: maxBytes}
}

func (r *RingBuffer) push(e entry) {
	for r.n > 0 && (r.n == len(r.buf) || r.bytes+len(e.frame) > r.maxBytes) {
		r.bytes -= len(r.buf[r.head].frame)
		r.buf[r.head] = entry{}
		r.head = (r.head + 1) % len(r.buf)
		r.n--
	}
	r.buf[(r.head+r.n)%len(r.buf)] = e
	r.n++
	r.bytes += len(e.frame)
}

// OldestSeq is the seq of the oldest entry still held (0 when empty).
func (r *RingBuffer) OldestSeq() uint64 {
	if r.n == 0 {
		return 0
	}
	return r.buf[r.head].seq
}

// Len is the number of entries held.
func (r *RingBuffer) Len() int { return r.n }

// Bytes is the total size of the encoded frames held.
func (r *RingBuffer) Bytes() int { return r.bytes }

// since calls fn, oldest first, for every entry with seq > after.
func (r *RingBuffer) since(after uint64, fn func(*entry)) {
	for i := 0; i < r.n; i++ {
		e := &r.buf[(r.head+i)%len(r.buf)]
		if e.seq > after {
			fn(e)
		}
	}
}
