package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// T014 (RED, FR-013, SC-010): a client that cannot keep up must be TOLD, never dropped
// silently. On protocol 2 the hub closes it with 4408 ("lagging", events-ws.v3.md
// section 6); before 002 Broadcast deletes the client and the handler returns, so the
// TCP connection ends without a close frame (CloseStatus -1 at the client, 1006 in a
// browser) and the tab cannot tell lagging from a network error.
func TestAdminEvents_SlowClientClosedWith4408(t *testing.T) {
	addr, hub := startEventsServer(t)
	conn, resp, err := dialEvents(t, addr, "/admin/events/v1/ws?protocol=2", sessionHeader(t))
	if resp != nil {
		closeBody(resp)
	}
	if err != nil {
		t.Fatalf("dial protocol 2: %v", err)
	}
	defer conn.CloseNow()
	// The frames below are 64 KiB each; the client must accept them so that the only
	// way this read loop can end is the server closing the socket.
	conn.SetReadLimit(1 << 20)

	// Do NOT read: wait until the hub registered this client, then flood it.
	deadline := time.Now().Add(3 * time.Second)
	for {
		hub.mu.Lock()
		n := len(hub.clients)
		hub.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the hub never registered the socket (clients = %d)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// RV-17: 64 KiB per event, so the backlog cannot hide in the loopback TCP buffers and
	// the per-client queue (adminEventClientBuffer) overflows deterministically.
	big := strings.Repeat("x", 64*1024)
	for i := 0; i < 1000; i++ {
		hub.Broadcast(AdminEvent{Type: adminEventTypeNotice, Payload: map[string]string{
			"level": "info", "code": "flood", "message": big,
		}})
	}

	// Now drain whatever the server still sends until the connection ends.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	frames := 0
	for {
		_, _, err = conn.Read(ctx)
		if err != nil {
			break
		}
		frames++
	}
	if ctx.Err() != nil {
		t.Fatalf("the slow client was neither closed nor resynced within 20 s (read %d frames)", frames)
	}
	if got := websocket.CloseStatus(err); got != 4408 {
		t.Fatalf("slow client: CloseStatus = %d, want 4408 (lagging) after %d frames; read error: %v", got, frames, err)
	}
}
