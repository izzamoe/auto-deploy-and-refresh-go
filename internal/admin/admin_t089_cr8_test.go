package admin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/events"
)

// T089 CR-8 (independent code review): RED before the fix, GREEN after
// (reference/executor/T089-red.log, T089.log).

// t089CountingFollower counts the references a socket holds on logs:system.
type t089CountingFollower struct {
	mu                 sync.Mutex
	acquires, releases int
}

func (f *t089CountingFollower) Acquire(string) events.LogsJoin {
	f.mu.Lock()
	f.acquires++
	f.mu.Unlock()
	return events.LogsJoin{}
}
func (f *t089CountingFollower) Release() { f.mu.Lock(); f.releases++; f.mu.Unlock() }
func (f *t089CountingFollower) Close()   {}
func (f *t089CountingFollower) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquires, f.releases
}

// CR-8: a peer that stopped answering (half-open TCP) is detected by a ping on the heartbeat:
// the socket is closed and the shared watchers it held are released.
func TestT089_CR8_DeadPeerIsClosedByPing(t *testing.T) {
	oldTick, oldTimeout := adminEventHeartbeatTick, adminEventPingTimeout
	adminEventHeartbeatTick, adminEventPingTimeout = 100*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { adminEventHeartbeatTick, adminEventPingTimeout = oldTick, oldTimeout })

	e := newWSSessionEnv(t, "t089_cr8", time.Hour)
	f := &t089CountingFollower{}
	e.hub.SetJournalFollower(f)
	c := e.connectV3(t)
	defer c.CloseNow()
	sendOp(t, c, map[string]any{"op": "sub", "ref": "r1", "topics": []any{map[string]any{"name": "logs:system"}}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err := c.Read(ctx) // subscribed; from now on the client never reads (no pong)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if acq, _ := f.counts(); acq != 1 {
		t.Fatalf("acquires = %d, want 1", acq)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, rel := f.counts(); rel == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("CR-8: a peer that never answers a ping still holds the journal follower after 3 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
