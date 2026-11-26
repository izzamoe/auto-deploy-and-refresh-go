package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// T089 CR-3 (independent code review): RED before the fix, GREEN after
// (reference/executor/T089-red.log, T089.log).

// CR-3 (Q-2=A, FR-031): a WebSocket handshake (a reconnect of an idle tab) must not extend
// the session: no Set-Cookie, and the hello reports the unchanged expiry.
func TestT089_CR3_WSHandshakeDoesNotRenewSession(t *testing.T) {
	e := newWSSessionEnv(t, "t089_cr3_ws", time.Hour)
	e.clock.Advance(7 * time.Hour) // 5 h of the 12 h idle window left: a normal request would renew
	for _, path := range []string{"/admin/events/v1/ws?protocol=3", "/admin/events/v1/ws?protocol=3&bg=1"} {
		conn, resp, err := dialEvents(t, e.addr, path, e.header())
		if err != nil {
			t.Fatalf("dial %s: %v", path, err)
		}
		if resp != nil {
			for _, c := range resp.Header.Values("Set-Cookie") {
				if strings.HasPrefix(c, e.mgr.CookieName()+"=") {
					t.Errorf("CR-3 %s: the handshake renewed the session (Set-Cookie %q)", path, c)
				}
			}
		}
		closeBody(resp)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, raw, err := conn.Read(ctx)
		cancel()
		_ = conn.CloseNow()
		if err != nil {
			t.Fatalf("hello: %v", err)
		}
		var hello struct {
			Payload struct {
				SessionExpiresAt time.Time `json:"sessionExpiresAt"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(raw, &hello); err != nil {
			t.Fatal(err)
		}
		if !hello.Payload.SessionExpiresAt.Equal(e.iss.ExpiresAt.UTC().Truncate(time.Second)) && !hello.Payload.SessionExpiresAt.Equal(e.iss.ExpiresAt.UTC()) {
			t.Errorf("CR-3 %s: hello.sessionExpiresAt = %v, want the unchanged %v", path, hello.Payload.SessionExpiresAt, e.iss.ExpiresAt.UTC())
		}
	}
}
