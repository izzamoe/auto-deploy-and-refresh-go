package progress

import (
	"bytes"
	"io"
	"math"
	"testing"
	"time"
)

type scriptedRead struct {
	steps []scriptStep
	idx   int
}

type scriptStep struct {
	data []byte
	err  error
}

func (r *scriptedRead) Read(p []byte) (int, error) {
	if r.idx >= len(r.steps) {
		return 0, io.EOF
	}
	step := r.steps[r.idx]
	r.idx++
	if len(step.data) > 0 {
		n := copy(p, step.data)
		return n, step.err
	}
	return 0, step.err
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func TestCountingReaderBasic(t *testing.T) {
	cr := NewCountingReader(bytes.NewReader(bytes.Repeat([]byte{'a'}, 1000)), 1000, nil)
	_, err := io.Copy(io.Discard, cr)
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if cr.downloaded != 1000 {
		t.Fatalf("downloaded = %d, want 1000", cr.downloaded)
	}
}

func TestCountingReaderSpeedCalculation(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	var got []float64
	cr := newCountingReaderWithClock(&scriptedRead{steps: []scriptStep{
		{data: bytes.Repeat([]byte{'a'}, 250)},
		{data: bytes.Repeat([]byte{'b'}, 250)},
		{data: bytes.Repeat([]byte{'c'}, 250)},
		{data: bytes.Repeat([]byte{'d'}, 250)},
		{err: io.EOF},
	}}, 1000, func(downloaded, total int64, speedBPS float64) {
		if downloaded > 0 && total == 1000 {
			got = append(got, speedBPS)
		}
	}, clock.Now)
	cr.interval = 500 * time.Millisecond

	buf := make([]byte, 256)
	clock.Advance(500 * time.Millisecond)
	if _, err := cr.Read(buf); err != nil {
		t.Fatalf("read 1: %v", err)
	}
	clock.Advance(500 * time.Millisecond)
	if _, err := cr.Read(buf); err != nil {
		t.Fatalf("read 2: %v", err)
	}
	clock.Advance(500 * time.Millisecond)
	if _, err := cr.Read(buf); err != nil {
		t.Fatalf("read 3: %v", err)
	}
	clock.Advance(500 * time.Millisecond)
	if _, err := cr.Read(buf); err != nil {
		t.Fatalf("read 4: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one speed sample")
	}
	if got[len(got)-1] < 450 || got[len(got)-1] > 550 {
		t.Fatalf("speed = %.2f, want around 500", got[len(got)-1])
	}
}

func TestCountingReaderUnknownTotal(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	var seenTotal int64
	cr := newCountingReaderWithClock(&scriptedRead{steps: []scriptStep{
		{data: bytes.Repeat([]byte{'a'}, 10)},
		{err: io.EOF},
	}}, -1, func(downloaded, total int64, speedBPS float64) {
		seenTotal = total
	}, clock.Now)
	cr.interval = time.Nanosecond
	clock.Advance(time.Millisecond)
	if _, err := cr.Read(make([]byte, 16)); err != nil {
		t.Fatalf("read: %v", err)
	}
	if seenTotal != -1 {
		t.Fatalf("total = %d, want -1", seenTotal)
	}
}

func TestCountingReaderFinalCallback(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	var calls int
	var downloaded int64
	var eofCalls int
	cr := newCountingReaderWithClock(&scriptedRead{steps: []scriptStep{
		{data: []byte("abc")},
		{err: io.EOF},
	}}, 3, func(d, total int64, speedBPS float64) {
		calls++
		downloaded = d
		if d == 3 {
			eofCalls++
		}
	}, clock.Now)
	cr.interval = time.Hour
	buf := make([]byte, 8)
	if _, err := cr.Read(buf); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if _, err := cr.Read(buf); err != io.EOF {
		t.Fatalf("second read err = %v, want EOF", err)
	}
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
	if eofCalls != 1 {
		t.Fatalf("EOF callback calls = %d, want 1", eofCalls)
	}
	if downloaded != 3 {
		t.Fatalf("downloaded = %d, want 3", downloaded)
	}
}

func TestCountingReaderThrottling(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	var calls int
	cr := newCountingReaderWithClock(&scriptedRead{steps: []scriptStep{
		{data: []byte("a")},
		{data: []byte("b")},
		{data: []byte("c")},
		{data: []byte("d")},
		{err: io.EOF},
	}}, 5, func(downloaded, total int64, speedBPS float64) {
		calls++
	}, clock.Now)
	cr.interval = 250 * time.Millisecond
	buf := make([]byte, 1)
	for i := 0; i < 2; i++ {
		clock.Advance(100 * time.Millisecond)
		if _, err := cr.Read(buf); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	if calls != 0 {
		t.Fatalf("callback calls after throttled reads = %d, want 0", calls)
	}
	clock.Advance(100 * time.Millisecond)
	if _, err := cr.Read(buf); err != nil {
		t.Fatalf("read 3: %v", err)
	}
	if calls != 1 {
		t.Fatalf("callback calls after interval elapsed = %d, want 1", calls)
	}
}

func TestUnknownContentLengthFallback(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	var total int64
	var speed float64
	var downloaded int64
	cr := newCountingReaderWithClock(&scriptedRead{steps: []scriptStep{
		{data: bytes.Repeat([]byte{'x'}, 256)},
		{err: io.EOF},
	}}, 0, func(d, tBytes int64, s float64) {
		downloaded, total, speed = d, tBytes, s
	}, clock.Now)
	cr.interval = time.Nanosecond
	clock.Advance(time.Second)
	if _, err := cr.Read(make([]byte, 256)); err != nil {
		t.Fatalf("read: %v", err)
	}
	if total != -1 {
		t.Fatalf("total = %d, want -1", total)
	}
	if downloaded != 256 {
		t.Fatalf("downloaded = %d, want 256", downloaded)
	}
	if math.IsNaN(speed) || math.IsInf(speed, 0) {
		t.Fatalf("speed invalid: %v", speed)
	}
}
