package auth

import (
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func newCSRFContext(headers map[string]string) *app.RequestContext {
	c := app.NewContext(0)
	c.Request.SetRequestURI("http://app.test/admin/api/v1/apps")
	c.Request.Header.SetMethod("POST")
	for k, v := range headers {
		c.Request.Header.Set(k, v)
	}
	return c
}

func TestCSRFSchemeComparedOnlyWhenForwardedProtoTrusted(t *testing.T) {
	h := map[string]string{"X-Admin-Request": "true", "Origin": "https://app.test", "X-Forwarded-Proto": "http"}
	// Untrusted X-Forwarded-Proto is ignored: host match is enough.
	if v := CheckCSRF(newCSRFContext(h), false); v != CSRFOK {
		t.Errorf("untrusted proto: verdict = %v, want OK", v)
	}
	// A trusted proxy says the request was plain http while Origin claims https: reject.
	if v := CheckCSRF(newCSRFContext(h), true); v != CSRFRejected {
		t.Errorf("trusted proto mismatch: verdict = %v, want Rejected", v)
	}
	h["X-Forwarded-Proto"] = "https"
	if v := CheckCSRF(newCSRFContext(h), true); v != CSRFOK {
		t.Errorf("trusted proto match: verdict = %v, want OK", v)
	}
}

func TestCSRFRejectsOriginWithPathOrNull(t *testing.T) {
	for _, origin := range []string{"null", "http://app.test/evil", "ftp://", "::::"} {
		h := map[string]string{"X-Admin-Request": "true", "Origin": origin}
		if v := CheckCSRF(newCSRFContext(h), false); v != CSRFRejected {
			t.Errorf("Origin %q: verdict = %v, want Rejected", origin, v)
		}
	}
}

func TestDeadCookiesAreClearedOn401(t *testing.T) {
	e := newAPIEnv(t, false)
	w := ut.PerformRequest(e.h.Engine, "GET", apiBase+"/apps", nil,
		ut.Header{Key: "Cookie", Value: e.mgr.CookieName() + "=garbage; admin_token=old"})
	r := w.Result()
	if r.StatusCode() != 401 {
		t.Fatalf("status = %d, want 401", r.StatusCode())
	}
	var cleared []string
	r.Header.VisitAllCookie(func(k, v []byte) { cleared = append(cleared, string(k)+"="+string(v)) })
	joined := strings.Join(cleared, "\n")
	for _, name := range []string{e.mgr.CookieName() + "=", "admin_token="} {
		if !strings.Contains(joined, name) {
			t.Errorf("expected a Set-Cookie expiring %q, got %q", name, joined)
		}
	}
}
