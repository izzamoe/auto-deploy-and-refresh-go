package events

import (
	"testing"
	"time"
)

// T036 (FR-016, NFR-008): <= 20 ops per 10 s, every 5th invalid JSON frame is a violation,
// the third violation within 60 s closes with 4429.

func TestLimits_TokenBucket20Per10s(t *testing.T) {
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	l := NewLimiter(t0)
	for i := 1; i <= MaxOpsPer10s; i++ {
		if !l.Allow(t0) {
			t.Fatalf("op %d of a burst of %d refused", i, MaxOpsPer10s)
		}
	}
	if l.Allow(t0) {
		t.Fatal("op 21 within the same instant allowed (limit 20 per 10 s)")
	}
	if l.Allow(t0.Add(400 * time.Millisecond)) {
		t.Fatal("a token came back before 500 ms (20 per 10 s = one per 500 ms)")
	}
	if !l.Allow(t0.Add(500 * time.Millisecond)) {
		t.Fatal("no token after 500 ms")
	}
	// Steady state: over 10 s at most 20 more ops pass.
	l = NewLimiter(t0)
	allowed := 0
	for ms := 0; ms < 10_000; ms += 10 {
		if l.Allow(t0.Add(time.Duration(ms) * time.Millisecond)) {
			allowed++
		}
	}
	if allowed > 2*MaxOpsPer10s {
		t.Fatalf("%d ops allowed in 10 s from a 100/s client, want <= 40 (burst 20 + refill 20)", allowed)
	}
	// The bucket never holds more than 20.
	l = NewLimiter(t0)
	n := 0
	for l.Allow(t0.Add(time.Hour)) {
		n++
	}
	if n != MaxOpsPer10s {
		t.Fatalf("after an hour idle the burst is %d, want %d", n, MaxOpsPer10s)
	}
}

func TestLimits_InvalidJSONEveryFifth(t *testing.T) {
	l := NewLimiter(time.Now())
	for i := 1; i <= 15; i++ {
		got := l.InvalidJSON()
		if want := i%MaxInvalidJSON == 0; got != want {
			t.Fatalf("invalid frame %d: violation=%v, want %v", i, got, want)
		}
	}
}

func TestLimits_ThirdViolationWithin60sCloses(t *testing.T) {
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	l := NewLimiter(t0)
	if l.Violation(t0) || l.Violation(t0.Add(10*time.Second)) {
		t.Fatal("the first two violations must not close")
	}
	if !l.Violation(t0.Add(59 * time.Second)) {
		t.Fatal("the third violation within 60 s must close (4429)")
	}

	l = NewLimiter(t0)
	for i, at := range []time.Duration{0, 30 * time.Second, 61 * time.Second, 89 * time.Second} {
		got := l.Violation(t0.Add(at))
		if want := i == 3; got != want {
			t.Fatalf("violation #%d at +%v: close=%v, want %v (sliding 60 s window)", i+1, at, got, want)
		}
	}
}

func TestLimits_OpShape(t *testing.T) {
	topics := `[` + repeatJSON(`{"name":"apps"}`, MaxTopics+1) + `]`
	if _, perr := ParseOp([]byte(`{"op":"sub","ref":"r1","topics":` + topics + `}`)); perr == nil || perr.Code != CodeTooManyTopics || perr.Ref != "r1" {
		t.Fatalf("17 topics: %+v, want too_many_topics with the ref", perr)
	}
	ok := `[` + repeatJSON(`{"name":"apps"}`, MaxTopics) + `]`
	if _, perr := ParseOp([]byte(`{"op":"sub","ref":"r1","topics":` + ok + `}`)); perr != nil {
		t.Fatalf("16 topics refused: %v", perr)
	}
	for _, bad := range []string{
		`not json`, `[]`, `null`, `{"op":"sub"}`, `{"op":"sub","ref":"bad ref!","topics":[{"name":"apps"}]}`,
		`{"op":"sub","ref":"r1234567890123456","topics":[{"name":"apps"}]}`,  // ref of 17
		`{"op":"sub","ref":"r1","topics":[{"name":"apps","progress":true}]}`, // progress only for jobs
		`{"op":"sub","ref":"r1","topics":[{"name":"jobs","after":"c"}]}`,     // after only for logs:system
		`{"op":"sub","ref":"r1","topics":[{"name":"stats","tz":"Mars/Base"}]}`,
		`{"op":"sub","ref":"r1","topics":[]}`,
		`{"op":"unsub","ref":"r1","topics":["apps"],"resume":{"epoch":"e","seq":1}}`,
		`{"op":"sub","ref":"r1","topics":[{"name":"apps"}],"resume":{"epoch":"e","seq":-1}}`,
		`{"op":"sub","ref":"r1","topics":[{"name":"apps"}],"extra":1}`,
	} {
		if _, perr := ParseOp([]byte(bad)); perr == nil || (perr.Code != CodeBadOp) {
			t.Errorf("ParseOp(%s) = %+v, want bad_op", bad, perr)
		}
	}
	if _, perr := ParseOp([]byte(`not json`)); !perr.InvalidJSON {
		t.Error("a non-JSON frame must be marked InvalidJSON")
	}
	op, perr := ParseOp([]byte(`{"op":"sub","ref":"r_1-a","topics":[{"name":"jobs","progress":false},{"name":"logs:system","after":"s=1"},{"name":"stats","tz":"Asia/Jakarta"}],"resume":{"epoch":"abc","seq":41}}`))
	if perr != nil || len(op.Subs) != 3 || op.Subs[0].Opts.Progress || op.Subs[1].Opts.After != "s=1" || op.Subs[2].Opts.TZ != "Asia/Jakarta" || op.Resume == nil || op.Resume.Seq != 41 {
		t.Fatalf("valid op parsed as %+v (%v)", op, perr)
	}
}

func repeatJSON(item string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += item
	}
	return out
}
