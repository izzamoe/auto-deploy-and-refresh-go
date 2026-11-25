package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/route"
)

// B30 / FR-054: every response carries the security headers: a CSP without
// 'unsafe-inline'/'unsafe-eval' for scripts, nosniff, a referrer policy, X-Frame-Options.

func newSecHeadersEngine(t *testing.T) *route.Engine {
	t.Helper()
	h := server.New(server.WithHostPorts("127.0.0.1:0"), server.WithDisablePrintRoute(true))
	SetupMiddleware(h)
	h.GET("/admin/api/v1/session", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(401, map[string]string{"status": "error", "code": "session_expired"})
	})
	return h.Engine
}

func assertSecurityHeaders(t *testing.T, what string, get func(string) string) {
	t.Helper()
	csp := get("Content-Security-Policy")
	if csp == "" {
		t.Errorf("B30: %s has no Content-Security-Policy", what)
	}
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'", "base-uri 'none'", "script-src 'self'"} {
		if csp != "" && !strings.Contains(csp, want) {
			t.Errorf("%s: CSP %q lacks %q", what, csp, want)
		}
	}
	scriptSrc := ""
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src") {
			scriptSrc = d
		}
	}
	if strings.Contains(scriptSrc, "'unsafe-inline'") || strings.Contains(scriptSrc, "'unsafe-eval'") {
		t.Errorf("%s: script-src must not allow unsafe-inline/unsafe-eval: %q", what, scriptSrc)
	}
	if got := get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("B30: %s X-Content-Type-Options = %q, want nosniff", what, got)
	}
	if got := get("X-Frame-Options"); got != "DENY" {
		t.Errorf("B30: %s X-Frame-Options = %q, want DENY", what, got)
	}
	if got := get("Referrer-Policy"); got == "" {
		t.Errorf("B30: %s has no Referrer-Policy", what)
	}
}

func TestSecurityHeaders_PresentOnAPIResponses(t *testing.T) {
	engine := newSecHeadersEngine(t)
	resp := ut.PerformRequest(engine, "GET", "/admin/api/v1/session", nil).Result()
	assertSecurityHeaders(t, "GET /admin/api/v1/session", func(k string) string { return string(resp.Header.Peek(k)) })
}

func TestSecurityHeaders_PresentOnErrorsAndUnknownRoutes(t *testing.T) {
	engine := newSecHeadersEngine(t)
	resp := ut.PerformRequest(engine, "GET", "/admin/api/v1/does-not-exist", nil).Result()
	if resp.StatusCode() != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode())
	}
	assertSecurityHeaders(t, "a 404", func(k string) string { return string(resp.Header.Peek(k)) })
}

func TestSecurityHeaders_HSTSOnlyWhenRequested(t *testing.T) {
	plain := ut.PerformRequest(newSecHeadersEngine(t), "GET", "/admin/api/v1/session", nil).Result()
	if got := string(plain.Header.Peek("Strict-Transport-Security")); got != "" {
		t.Errorf("HSTS without the option: %q", got)
	}
	h := server.New(server.WithHostPorts("127.0.0.1:0"), server.WithDisablePrintRoute(true))
	SetupMiddlewareWithOptions(h, SecurityHeadersConfig{HSTS: true})
	h.GET("/x", func(ctx context.Context, c *app.RequestContext) { c.String(200, "ok") })
	resp := ut.PerformRequest(h.Engine, "GET", "/x", nil).Result()
	if got := string(resp.Header.Peek("Strict-Transport-Security")); !strings.HasPrefix(got, "max-age=") {
		t.Errorf("HSTS header = %q", got)
	}
}

func TestInlineScriptHashes(t *testing.T) {
	html := []byte(`<html><head>
<script src="/admin/_next/a.js"></script>
<script>self.__next_f.push([1,"a"])</script>
<script type="module">import("/x")</script>
<script>self.__next_f.push([1,"a"])</script>
<SCRIPT nonce="x">alert(1)</SCRIPT>
<script></script>
</head></html>`)
	got := InlineScriptHashes(html)
	want := []string{sriHash(`self.__next_f.push([1,"a"])`), sriHash(`import("/x")`), sriHash(`alert(1)`)}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("hashes = %v, want %v (external, empty and duplicate scripts excluded)", got, want)
	}
}

func sriHash(src string) string {
	sum := sha256.Sum256([]byte(src))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func TestBuildCSP_NeverAllowsUnsafeScript(t *testing.T) {
	csp := BuildCSP([]string{"'sha256-AAA='", "'sha256-BBB='"})
	if !strings.Contains(csp, "script-src 'self' 'sha256-AAA=' 'sha256-BBB='") {
		t.Errorf("CSP = %q", csp)
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe") || strings.Contains(csp, "'unsafe-eval'") {
		t.Errorf("CSP must not allow unsafe script execution: %q", csp)
	}
}

// The SPA shell: its inline scripts are allowed by hash, the CSP is the document's own.
func TestCSP_SPAShellListsTheHashesOfItsInlineScripts(t *testing.T) {
	const inline = `window.__BOOT__=1`
	page := `<!doctype html><html><head><script>` + inline + `</script><script src="/admin/assets/app.js"></script></head><body></body></html>`
	handler := &adminSPAHandler{fs: http.FS(fstest.MapFS{"index.html": {Data: []byte(page)}})}
	h := server.New(server.WithHostPorts("127.0.0.1:0"), server.WithDisablePrintRoute(true))
	SetupMiddleware(h)
	h.GET("/admin", handler.serveIndexHertz)
	resp := ut.PerformRequest(h.Engine, "GET", "/admin", nil).Result()
	csp := string(resp.Header.Peek("Content-Security-Policy"))
	if !strings.Contains(csp, sriHash(inline)) {
		t.Fatalf("CSP %q lacks the hash of the inline script", csp)
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || strings.Contains(csp, "'unsafe-eval'") {
		t.Errorf("CSP allows unsafe script execution: %q", csp)
	}
	assertSecurityHeaders(t, "the SPA shell", func(k string) string { return string(resp.Header.Peek(k)) })
}
