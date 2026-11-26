package admin

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/izzamoe/auto-deploy/internal/deploy"
	"github.com/izzamoe/auto-deploy/internal/events"
)

// T089 CR-6 (independent code review): RED before the fix, GREEN after
// (reference/executor/T089-red.log, T089.log).

// t089Journal is a fake journalctl for the real deploy.JournalFollower behind the hub.
type t089Journal struct {
	mu    sync.Mutex
	procs []*io.PipeWriter
}

func (j *t089Journal) start(ctx context.Context, args ...string) (io.ReadCloser, func() error, error) {
	r, w := io.Pipe()
	j.mu.Lock()
	j.procs = append(j.procs, w)
	j.mu.Unlock()
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		_ = w.Close()
		close(done)
	}()
	return r, func() error { <-done; return nil }, nil
}

func (j *t089Journal) write(t *testing.T, cursor, msg string) {
	t.Helper()
	j.mu.Lock()
	w := j.procs[len(j.procs)-1]
	j.mu.Unlock()
	raw, _ := json.Marshal(map[string]string{"__CURSOR": cursor, "__REALTIME_TIMESTAMP": "1795700000000000", "PRIORITY": "6", "MESSAGE": msg})
	if _, err := w.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
}

// collector reads a socket in the background (a Read whose context expires closes the
// connection, so tests never read with a deadline).
type collector struct {
	mu     sync.Mutex
	frames []map[string]any
}

func collect(conn *websocket.Conn) *collector {
	c := &collector{}
	go func() {
		for {
			_, raw, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				c.mu.Lock()
				c.frames = append(c.frames, m)
				c.mu.Unlock()
			}
		}
	}()
	return c
}

func (c *collector) after(d time.Duration) []map[string]any {
	time.Sleep(d)
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.frames...)
}

func countLogMessage(frames []map[string]any, msg string) int {
	n := 0
	for _, f := range frames {
		if f["type"] != "logs.lines" {
			continue
		}
		p, _ := f["payload"].(map[string]any)
		lines, _ := p["lines"].([]any)
		for _, l := range lines {
			if lm, _ := l.(map[string]any); lm != nil && lm["message"] == msg {
				n++
			}
		}
	}
	return n
}

func sendOp(t *testing.T, conn *websocket.Conn, op any) {
	t.Helper()
	raw, _ := json.Marshal(op)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatal(err)
	}
}

// CR-6: a socket joining logs:system while a batch is between the follower's recent lines and
// the bus receives each line ONCE (not in the replay and again live).
func TestT089_CR6_LogsJoinDoesNotDuplicateLines(t *testing.T) {
	e := newWSSessionEnv(t, "t089_cr6", time.Hour)
	j := &t089Journal{}
	t.Cleanup(deploy.SetStartJournalFollowForTest(j.start))
	gate := make(chan struct{})
	var batches sync.WaitGroup
	var n int
	var nmu sync.Mutex
	f := deploy.NewJournalFollower(deploy.JournalFollowerOptions{Unit: "auto-deploy.service", Batch: 10 * time.Millisecond, Grace: time.Hour},
		func(lines []deploy.JournalLine, cursor string) {
			nmu.Lock()
			n++
			second := n == 2
			nmu.Unlock()
			if second {
				<-gate // the batch is in the recent lines already; its publication is late
			}
			_, _ = e.hub.bus.Publish(events.LogsLines(logLines(lines), cursor))
			batches.Done()
		}, nil, nil)
	t.Cleanup(f.Close)
	e.hub.SetJournalFollower(journalAdapter{f})

	a := e.connectV3(t)
	defer a.CloseNow()
	sendOp(t, a, map[string]any{"op": "sub", "ref": "a1", "topics": []any{map[string]any{"name": "logs:system"}}})
	collect(a).after(200 * time.Millisecond)
	batches.Add(1)
	j.write(t, "s=a;i=1", "one")
	batches.Wait()
	batches.Add(1)
	j.write(t, "s=a;i=2", "two")
	deadline := time.Now().Add(3 * time.Second)
	for f.Head() != "s=a;i=2" {
		if time.Now().After(deadline) {
			t.Fatal("the second line never reached the follower")
		}
		time.Sleep(5 * time.Millisecond)
	}

	b := e.connectV3(t)
	defer b.CloseNow()
	sendOp(t, b, map[string]any{"op": "sub", "ref": "b1", "topics": []any{map[string]any{"name": "logs:system", "after": "s=a;i=1"}}})
	cb := collect(b)
	cb.after(300 * time.Millisecond)
	close(gate)
	batches.Wait()
	got := cb.after(500 * time.Millisecond)
	if c := countLogMessage(got, "two"); c != 1 {
		t.Fatalf("CR-6: the joining socket received the line %d times, want once: %v", c, got)
	}
}
