package auth

import (
	"strings"
	"testing"
	"time"
)

// T089 CR-3 (Q-2=A, FR-031): an idle tab must not keep its session alive. Requests the
// panel makes on its own because of an event (X-Admin-Background: 1) and WebSocket
// handshakes are authenticated as usual but never renew the session; a request without the
// mark (a user action, a page load) renews it like before. The mark grants nothing: an
// expired session stays 401.

func (e *apiEnv) rowExpiry(t *testing.T, sid string) time.Time {
	t.Helper()
	row, err := e.be.Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	return row.ExpiresAt
}

func TestT089_CR3_BackgroundRequestDoesNotRenewSession(t *testing.T) {
	e := newAPIEnv(t, false)
	iss := e.issue(t)
	before := e.rowExpiry(t, iss.SessionID)
	e.clk.Advance(7 * time.Hour) // 5 h of the 12 h idle window left: renewal is due

	for _, hdrs := range []map[string]string{
		{"Cookie": e.cookie(iss), "X-Admin-Background": "1"},
		{"Cookie": e.cookie(iss), "Connection": "Upgrade", "Upgrade": "websocket"},
	} {
		r := e.do("GET", "/apps", hdrs, "")
		if r.status != 200 {
			t.Fatalf("background request %v: status %d %s", hdrs, r.status, r.body)
		}
		if sc := r.hdr("Set-Cookie"); strings.Contains(sc, e.mgr.CookieName()+"=") {
			t.Errorf("CR-3: background request %v renewed the cookie: %q", hdrs, sc)
		}
		if got := e.rowExpiry(t, iss.SessionID); !got.Equal(before) {
			t.Errorf("CR-3: background request %v moved expires_at %v -> %v", hdrs, before, got)
		}
	}
	r := e.do("GET", "/apps?bg=1", map[string]string{"Cookie": e.cookie(iss)}, "")
	if r.status != 200 || strings.Contains(r.hdr("Set-Cookie"), e.mgr.CookieName()+"=") {
		t.Errorf("CR-3: ?bg=1 request: status %d Set-Cookie %q (want 200 without renewal)", r.status, r.hdr("Set-Cookie"))
	}
	if got := e.rowExpiry(t, iss.SessionID); !got.Equal(before) {
		t.Errorf("CR-3: ?bg=1 moved expires_at %v -> %v", before, got)
	}

	// A user's own request (no mark) renews as before.
	r = e.do("GET", "/apps", map[string]string{"Cookie": e.cookie(iss)}, "")
	if r.status != 200 || !strings.Contains(r.hdr("Set-Cookie"), e.mgr.CookieName()+"=") {
		t.Fatalf("a request without the mark must renew: status %d Set-Cookie %q", r.status, r.hdr("Set-Cookie"))
	}
	if got := e.rowExpiry(t, iss.SessionID); !got.After(before) {
		t.Errorf("a request without the mark did not extend expires_at (%v)", got)
	}
}

func TestT089_CR3_BackgroundMarkGrantsNothing(t *testing.T) {
	e := newAPIEnv(t, false)
	iss := e.issue(t)
	e.clk.Advance(13 * time.Hour) // past the idle window
	r := e.do("GET", "/apps", map[string]string{"Cookie": e.cookie(iss), "X-Admin-Background": "1"}, "")
	if r.status != 401 || r.errBody(t).Code != "session_expired" {
		t.Fatalf("expired session with the background mark: %d %s, want 401 session_expired", r.status, r.body)
	}
	r = e.do("GET", "/apps", map[string]string{"X-Admin-Background": "1"}, "")
	if r.status != 401 {
		t.Fatalf("no credentials with the background mark: %d, want 401", r.status)
	}
	// A mutation with the mark is still CSRF-checked.
	iss2 := e.issue(t)
	r = e.do("POST", "/apps", map[string]string{"Cookie": e.cookie(iss2), "X-Admin-Background": "1", "Content-Type": "application/json"}, "{}")
	if r.status != 403 {
		t.Fatalf("mutation without X-Admin-Request but with the background mark: %d, want 403", r.status)
	}
}
