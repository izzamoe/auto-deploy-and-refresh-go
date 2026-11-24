package regression

import (
	"strings"
	"testing"
)

// B6 (proof A1): the session cookie must carry Secure with the default config.
func TestRegression_B6_CookieNotSecureByDefault(t *testing.T) {
	e := newEnv(t)
	st, _, raw := e.login(t, "admin", "11")
	t.Logf("login admin/11 -> %d Set-Cookie=%q", st, raw)
	if !strings.Contains(strings.ToLower(raw), "secure") {
		t.Error("B6: session cookie issued WITHOUT the Secure flag by default")
	}
}

// K1 (proof A2): a 401 for a request without Authorization must not carry
// WWW-Authenticate (it makes browsers pop up the native Basic dialog).
func TestRegression_K1_WWWAuthenticateOnBrowser401(t *testing.T) {
	e := newEnv(t)
	st, _, wa, _ := e.do("GET", "/admin/api/apps", "", "", "")
	t.Logf("no-cookie GET /admin/api/apps -> %d WWW-Authenticate=%q", st, wa)
	if wa != "" {
		t.Errorf("K1: unauthenticated API response carries WWW-Authenticate=%q (browser popup)", wa)
	}
}

// B5 (proof A3): repeated wrong logins must be rate limited (429) within 30 attempts.
func TestRegression_B5_NoRateLimitDefaultAdmin(t *testing.T) {
	e := newEnv(t)
	codes := map[int]int{}
	for i := 0; i < 30; i++ {
		c, _, _ := e.login(t, "admin", "wrong")
		codes[c]++
	}
	t.Logf("30 wrong logins -> status histogram %v", codes)
	if codes[429] == 0 {
		t.Errorf("B5: no 429 in 30 wrong logins against the default admin (histogram %v)", codes)
	}
}

// B3 (proof A4): renaming the account under the must-change gate must not let the
// pre-rename token bypass the gate while the default password is still in use.
func TestRegression_B3_RenameBypassesMustChange(t *testing.T) {
	e := newEnv(t)
	st, cookie1, raw := e.login(t, "admin", "11")
	t.Logf("login -> %d Set-Cookie=%q", st, raw)
	st, body, _, _ := e.do("GET", "/admin/api/apps", cookie1, "", "")
	t.Logf("A4a gated GET /admin/api/apps with cookie1 -> %d %s", st, body)
	st, body, _, cookie2 := e.do("POST", "/admin/api/account/username", cookie1, "application/json", `{"currentPassword":"11","newUsername":"renamed"}`)
	t.Logf("A4b rename -> %d %s", st, body)
	st, body, _, _ = e.do("GET", "/admin/api/apps", cookie1, "", "")
	t.Logf("A4c OLD cookie after rename GET /admin/api/apps -> %d %s", st, body)
	if st == 200 {
		t.Error("B3: must-change-password gate bypassed with the pre-rename token while default password 11 is still in use")
	}
	st, _, _, _ = e.do("GET", "/admin/api/apps", cookie2, "", "")
	t.Logf("A4d NEW cookie GET /admin/api/apps -> %d (still gated)", st)
}

// B4 (proof A5): a password change must revoke sessions issued before it.
func TestRegression_B4_NoSessionRevocation(t *testing.T) {
	e := newEnv(t)
	_, cookie2, _ := e.ungatedSession(t)
	st, body, _, _ := e.do("GET", "/admin/api/apps", cookie2, "", "")
	t.Logf("token issued BEFORE password change -> %d %s", st, body)
	if st == 200 {
		t.Error("B4: session issued before password change is still accepted (no revocation)")
	}
}

// B16 (proof A8): mutation endpoints must reject a text/plain body (CORS-simple
// request) with 415 or 403. The body is otherwise a VALID password change, so the
// only legitimate reason to reject it is the Content-Type.
func TestRegression_B16_TextPlainAccepted(t *testing.T) {
	e := newEnv(t)
	_, _, cookie3 := e.ungatedSession(t)
	st, body, _, _ := e.do("POST", "/admin/api/account/password", cookie3, "text/plain", `{"currentPassword":"newpass123","newPassword":"zzzzzz123"}`)
	t.Logf("text/plain POST /admin/api/account/password -> %d %s", st, body)
	if st != 415 && st != 403 {
		t.Errorf("B16: text/plain mutation request not rejected (got %d, want 415 or 403)", st)
	}
}

// B15 (proof A6): an invalid tag on manual deploy is a client error (400), never a 500
// that leaks the internal error string.
func TestRegression_B15_InvalidTagLeaks500(t *testing.T) {
	e := newEnv(t)
	_, _, cookie3 := e.ungatedSession(t)
	app, err := e.apps.Create("a", "sec", "/tmp/x/a", "a.service", "o/r", "a")
	if err != nil {
		t.Fatal(err)
	}
	st, body, _, _ := e.do("POST", "/admin/api/apps/"+app.ID+"/deploy", cookie3, "application/json", `{"tag":"v1 bad"}`)
	t.Logf("manual deploy invalid tag -> %d %s", st, body)
	if st == 500 {
		t.Error("B15: invalid tag maps to HTTP 500 and leaks internal error string")
	}
}
