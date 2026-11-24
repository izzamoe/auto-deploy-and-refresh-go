package admin

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSession_LoginGetLogoutRevokesOnServer(t *testing.T) {
	e := newFullEnv(t, false)
	cookie := e.loginAs(t, "admin", sessionTestPassword)

	r := e.call("GET", "/admin/api/v1/session", cookie, "")
	if r.status != 200 {
		t.Fatalf("GET /session: %d %s", r.status, r.body)
	}
	var s struct {
		Username           string
		MustChangePassword bool
		ExpiresAt          string
		AbsoluteExpiresAt  string
	}
	if err := json.Unmarshal([]byte(r.body), &s); err != nil || s.Username != "admin" || s.MustChangePassword || s.ExpiresAt == "" || s.AbsoluteExpiresAt == "" {
		t.Errorf("GET /session body = %s (%v)", r.body, err)
	}

	r = e.call("DELETE", "/admin/api/v1/session", cookie, "")
	if r.status != 204 {
		t.Fatalf("logout: %d %s, want 204", r.status, r.body)
	}
	if !r.set || r.cookie != "" {
		t.Errorf("logout must expire the cookie (set=%v value=%q)", r.set, r.cookie)
	}
	// B4/FR-007: the copied cookie is useless now, not just deleted in the browser.
	if r := e.call("GET", "/admin/api/v1/probe", cookie, ""); r.status != 401 || errCode(t, r) != "session_revoked" {
		t.Errorf("replaying a logged-out cookie: %d %s, want 401 session_revoked", r.status, r.body)
	}
	// Logout is DELETE only: GET /session is a read, never a logout.
	if r := e.call("GET", "/admin/api/v1/session", cookie, ""); r.status != 401 {
		t.Errorf("GET /session with a dead cookie: %d, want 401", r.status)
	}
}

// RV2-14: POST /session ignores Authorization, so credentials cached by an old browser
// dialog neither break nor override the login.
func TestSession_LoginIgnoresStaleAuthorizationHeader(t *testing.T) {
	e := newFullEnv(t, false)
	h := e.browser("")
	h["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:11"))
	r := e.send("POST", "/admin/api/v1/session", h, `{"username":"admin","password":"`+sessionTestPassword+`"}`)
	if r.status != 200 || r.cookie == "" {
		t.Fatalf("login with a stale Authorization header: %d %s", r.status, r.body)
	}
	if r.hdr("WWW-Authenticate") != "" {
		t.Error("login must never send a challenge")
	}
	// And a wrong password with a "correct" Authorization header must still fail.
	h["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:"+sessionTestPassword))
	if r := e.send("POST", "/admin/api/v1/session", h, `{"username":"admin","password":"wrong"}`); r.status != 401 {
		t.Errorf("Authorization header must not authenticate a login: %d", r.status)
	}
}

func TestSession_LoginGuards(t *testing.T) {
	e := newFullEnv(t, false)
	good := `{"username":"admin","password":"` + sessionTestPassword + `"}`
	with := func(mut func(h map[string]string)) map[string]string {
		h := e.browser("")
		mut(h)
		return h
	}
	cases := []struct {
		name string
		hdr  map[string]string
		body string
		want int
		code string
	}{
		{"missing X-Admin-Request", with(func(h map[string]string) { delete(h, "X-Admin-Request") }), good, 403, "csrf_rejected"},
		{"cross-site", with(func(h map[string]string) { h["Sec-Fetch-Site"] = "cross-site" }), good, 403, "csrf_rejected"},
		{"foreign Origin", with(func(h map[string]string) { delete(h, "Sec-Fetch-Site"); h["Origin"] = "https://evil.example" }), good, 403, "csrf_rejected"},
		{"text/plain", with(func(h map[string]string) { h["Content-Type"] = "text/plain" }), good, 415, "unsupported_media_type"},
		{"broken JSON", e.browser(""), `{nope`, 400, "invalid_json"},
		{"empty username", e.browser(""), `{"username":"","password":"x"}`, 400, "validation_failed"},
		{"empty password", e.browser(""), `{"username":"admin","password":""}`, 400, "validation_failed"},
		{"overlong password", e.browser(""), `{"username":"admin","password":"` + strings.Repeat("p", 1025) + `"}`, 400, "validation_failed"},
		{"curl-style client (no Origin, no Sec-Fetch-Site)", with(func(h map[string]string) { delete(h, "Sec-Fetch-Site") }), good, 200, ""},
	}
	for _, tc := range cases {
		r := e.send("POST", "/admin/api/v1/session", tc.hdr, tc.body)
		if r.status != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", tc.name, r.status, tc.want, r.body)
			continue
		}
		if tc.code != "" && errCode(t, r) != tc.code {
			t.Errorf("%s: code = %q, want %q", tc.name, errCode(t, r), tc.code)
		}
		if tc.want != 200 && r.cookie != "" {
			t.Errorf("%s: a rejected login must not set a session cookie", tc.name)
		}
	}
}

func TestSession_LoginFailureIsGenericAndMustChangeIsReported(t *testing.T) {
	e := newFullEnv(t, true)
	wrongPass := e.send("POST", "/admin/api/v1/session", e.browser(""), `{"username":"admin","password":"wrong"}`)
	noUser := e.send("POST", "/admin/api/v1/session", e.browser(""), `{"username":"ghost","password":"wrong"}`)
	if wrongPass.status != 401 || noUser.status != 401 {
		t.Fatalf("statuses %d / %d, want 401 / 401", wrongPass.status, noUser.status)
	}
	if errCode(t, wrongPass) != "unauthorized" || wrongPass.body == "" {
		t.Errorf("wrong password body: %s", wrongPass.body)
	}
	strip := func(s string) string { // request ids differ per call
		var m map[string]any
		_ = json.Unmarshal([]byte(s), &m)
		delete(m, "requestId")
		b, _ := json.Marshal(m)
		return string(b)
	}
	if strip(wrongPass.body) != strip(noUser.body) {
		t.Errorf("unknown user and wrong password must be indistinguishable:\n%s\n%s", wrongPass.body, noUser.body)
	}

	r := e.send("POST", "/admin/api/v1/session", e.browser(""), `{"username":"admin","password":"`+sessionTestPassword+`"}`)
	if r.status != 200 || !strings.Contains(r.body, `"mustChangePassword":true`) {
		t.Errorf("login must report mustChangePassword: %d %s", r.status, r.body)
	}
}
