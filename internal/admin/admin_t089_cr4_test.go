package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// T089 CR-4 (independent code review): RED before the fix, GREEN after
// (reference/executor/T089-red.log, T089.log).

// dialWith opens a protocol-3 socket with the given headers (an API token: no cookie).
func (e *mutevEnv) dialWith(t *testing.T, hdr http.Header) *mutevSocket {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://"+e.addr+"/admin/events/v1/ws?protocol=3", &websocket.DialOptions{HTTPHeader: hdr})
	closeBody(resp)
	if err != nil {
		t.Fatalf("dial ?protocol=3: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("no hello: %v", err)
	}
	s := &mutevSocket{conn: conn, done: make(chan struct{})}
	_ = json.Unmarshal(raw, &s.Hello)
	s.Hello.Raw = raw
	go s.readLoop()
	return s
}

func (e *mutevEnv) createToken(t *testing.T) (id, token string) {
	t.Helper()
	_, raw, _ := e.mustDo(t, "POST", "/tokens", e.cookie, map[string]string{"name": "cr4"}, http.StatusCreated)
	var out struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" || out.Token == "" {
		t.Fatalf("create token: %s (%v)", raw, err)
	}
	return out.ID, out.Token
}

// closedWithin waits at most d for the socket to end and returns its close status.
func (s *mutevSocket) closedWithin(d time.Duration) (websocket.StatusCode, bool) {
	select {
	case <-s.done:
	case <-time.After(d):
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return websocket.CloseStatus(s.closeErr), true
}

// CR-4: a socket opened with an API token is closed 4401 within 1 s of the token's
// revocation (in-process signal), like a revoked session.
func TestT089_CR4_RevokedTokenClosesSocket(t *testing.T) {
	e := newMutevEnv(t)
	tokenID, token := e.createToken(t)
	s := e.dialWith(t, http.Header{"Authorization": {"Bearer " + token}})
	s.sub(t, "r1", nil, mutevTopic("apps"))

	e.mustDo(t, "DELETE", "/tokens/"+tokenID, e.cookie, nil, http.StatusNoContent)
	code, closed := s.closedWithin(time.Second)
	if !closed || code != 4401 {
		t.Fatalf("CR-4: after DELETE /tokens/:id the token's socket: closed=%v status=%d, want 4401 within 1 s", closed, code)
	}
}

// CR-4: the token row is re-checked periodically too (revoked outside the handler, or gone).
func TestT089_CR4_TokenRecheckClosesSocket(t *testing.T) {
	old := adminEventSessionCheckInterval
	adminEventSessionCheckInterval = 50 * time.Millisecond
	t.Cleanup(func() { adminEventSessionCheckInterval = old })

	e := newMutevEnv(t)
	tokenID, token := e.createToken(t)
	s := e.dialWith(t, http.Header{"Authorization": {"Bearer " + token}})
	s.sub(t, "r1", nil, mutevTopic("apps"))
	if code, closed := s.closedWithin(300 * time.Millisecond); closed {
		t.Fatalf("a valid token's socket was closed (%d)", code)
	}
	if _, err := e.db.Exec(`UPDATE api_tokens SET revoked_at = ? WHERE id = ?`, time.Now().UTC(), tokenID); err != nil {
		t.Fatal(err)
	}
	code, closed := s.closedWithin(time.Second)
	if !closed || code != 4401 {
		t.Fatalf("CR-4: token revoked in the store: closed=%v status=%d, want 4401 at the next check", closed, code)
	}
}
