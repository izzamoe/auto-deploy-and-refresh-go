package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

// Inline bootstrap scripts as Next's export writes them (D4): the CSP of each document
// must carry exactly their hashes.
const (
	inlineA = `self.__next_f.push([1,"a"])`
	inlineB = `self.__next_f.push([1,"b"])`
)

func exportFS() fstest.MapFS {
	page := func(title string, scripts ...string) *fstest.MapFile {
		var sb strings.Builder
		sb.WriteString("<!DOCTYPE html><html><head><title>" + title + `</title><script src="/admin/_next/static/chunks/app.js" async=""></script></head><body>` + title)
		for _, s := range scripts {
			sb.WriteString("<script>" + s + "</script>")
		}
		sb.WriteString("</body></html>")
		return &fstest.MapFile{Data: []byte(sb.String())}
	}
	return fstest.MapFS{
		".keep":                            {},
		"index.html":                       page("ROOT", inlineA, inlineB),
		"index.txt":                        {Data: []byte("rsc-root")},
		"404.html":                         page("NOTFOUND", inlineA),
		"404/index.html":                   page("NOTFOUND-DIR", inlineA),
		"apps/index.html":                  page("APPS", inlineB),
		"apps/__next._tree.txt":            {Data: []byte("rsc-tree")},
		"apps/__next.apps.__PAGE__.txt":    {Data: []byte("rsc-page")},
		"apps/edit/index.html":             page("EDIT", inlineA),
		"__next._tree.txt":                 {Data: []byte("rsc-root-tree")},
		"__next.__PAGE__.txt":              {Data: []byte("rsc-root-page")},
		"_next/static/chunks/app.js":       {Data: []byte("console.log(1)")},
		"_next/static/chunks/app.css":      {Data: []byte("body{}")},
		"_next/static/media/dm-sans.woff2": {Data: []byte("font")},
		"logo.svg":                         {Data: []byte("<svg/>")},
	}
}

// passThroughAuth stands for the public mount of cmd/server (Q-17).
func passThroughAuth(ctx context.Context, c *app.RequestContext) { c.Next(ctx) }

func staticEngine(t *testing.T, fsys fstest.MapFS) *server.Hertz {
	t.Helper()
	sh, err := NewStaticHandler(fsys)
	if err != nil {
		t.Fatalf("NewStaticHandler: %v", err)
	}
	h := server.New(server.WithHostPorts("127.0.0.1:0"), server.WithDisablePrintRoute(true))
	SetupMiddleware(h)
	RegisterStaticHandlerHertz(h, passThroughAuth, sh)
	return h
}

func scriptHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func cspDirective(csp, name string) string {
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, name+" ") || d == name {
			return d
		}
	}
	return ""
}

func TestStaticHandler_PathMapping(t *testing.T) {
	t.Parallel()
	h := staticEngine(t, exportFS())

	cases := []struct {
		name         string
		path         string
		wantStatus   int
		wantBody     string // substring
		wantLocation string
		wantType     string
	}{
		{"root without slash redirects", "/admin", 308, "", "/admin/", ""},
		{"root", "/admin/", 200, "ROOT", "", "text/html"},
		{"directory index", "/admin/apps/", 200, "APPS", "", "text/html"},
		{"nested directory index", "/admin/apps/edit/", 200, "EDIT", "", "text/html"},
		{"directory without slash redirects", "/admin/apps", 308, "", "/admin/apps/", ""},
		{"redirect keeps the query", "/admin/apps/edit?id=q1", 308, "", "/admin/apps/edit/?id=q1", ""},
		{"query ignored when serving", "/admin/apps/edit/?id=q1", 200, "EDIT", "", "text/html"},
		{"file served as is", "/admin/logo.svg", 200, "<svg/>", "", "image/svg+xml"},
		{"explicit index.html", "/admin/apps/index.html", 200, "APPS", "", "text/html"},
		// #73427 (spike-report): Next 16.4.6 requests the RSC payload files that the
		// export writes; the plain path lookup serves them, no extra mapping needed.
		{"rsc root tree", "/admin/__next._tree.txt?_rsc=abc", 200, "rsc-root-tree", "", "text/plain; charset=utf-8"},
		{"rsc root page", "/admin/__next.__PAGE__.txt?_rsc=abc", 200, "rsc-root-page", "", "text/plain; charset=utf-8"},
		{"rsc route tree", "/admin/apps/__next._tree.txt?_rsc=abc", 200, "rsc-tree", "", "text/plain; charset=utf-8"},
		{"rsc route page", "/admin/apps/__next.apps.__PAGE__.txt?_rsc=abc", 200, "rsc-page", "", "text/plain; charset=utf-8"},
		{"rsc index", "/admin/index.txt", 200, "rsc-root", "", "text/plain; charset=utf-8"},
		{"/admin.txt is not an export file", "/admin.txt", 404, "", "", ""},
		{"js", "/admin/_next/static/chunks/app.js", 200, "console.log(1)", "", "text/javascript; charset=utf-8"},
		{"css", "/admin/_next/static/chunks/app.css", 200, "body{}", "", "text/css; charset=utf-8"},
		{"font", "/admin/_next/static/media/dm-sans.woff2", 200, "font", "", "font/woff2"},
		{"unknown path -> 404.html", "/admin/nope/", 404, "NOTFOUND", "", "text/html"},
		{"unknown file -> 404.html", "/admin/nope.js", 404, "NOTFOUND", "", "text/html"},
		{"missing hashed asset -> 404.html", "/admin/_next/static/chunks/missing.js", 404, "NOTFOUND", "", "text/html"},
		{"traversal", "/admin/../etc/passwd", 404, "", "", ""},
		// Hertz normalises the path before routing, so this may never reach the handler;
		// the contract is only: 404, and no file outside the export.
		{"encoded traversal", "/admin/%2e%2e/etc/passwd", 404, "", "", ""},
		{"backslash", "/admin/..%5csecret", 404, "NOTFOUND", "", ""},
		{".keep is never served", "/admin/.keep", 404, "NOTFOUND", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := ut.PerformRequest(h.Engine, http.MethodGet, tc.path, nil)
			if w.Code != tc.wantStatus {
				t.Fatalf("GET %s: status %d, want %d (body %q)", tc.path, w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(w.Body.String(), tc.wantBody) {
				t.Errorf("GET %s: body %q lacks %q", tc.path, w.Body.String(), tc.wantBody)
			}
			if got := w.Header().Get("Location"); got != tc.wantLocation {
				t.Errorf("GET %s: Location %q, want %q", tc.path, got, tc.wantLocation)
			}
			if tc.wantType != "" && !strings.HasPrefix(w.Header().Get("Content-Type"), tc.wantType) {
				t.Errorf("GET %s: Content-Type %q, want prefix %q", tc.path, w.Header().Get("Content-Type"), tc.wantType)
			}
		})
	}
}

func TestStaticHandler_CacheControlAndETag(t *testing.T) {
	t.Parallel()
	h := staticEngine(t, exportFS())

	t.Run("hashed assets are immutable", func(t *testing.T) {
		for _, p := range []string{"/admin/_next/static/chunks/app.js", "/admin/_next/static/media/dm-sans.woff2"} {
			w := ut.PerformRequest(h.Engine, http.MethodGet, p, nil)
			if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Errorf("%s: Cache-Control %q", p, got)
			}
		}
	})
	t.Run("html and rsc payloads revalidate with an ETag", func(t *testing.T) {
		for _, p := range []string{"/admin/", "/admin/apps/", "/admin/__next._tree.txt", "/admin/logo.svg"} {
			w := ut.PerformRequest(h.Engine, http.MethodGet, p, nil)
			if got := w.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("%s: Cache-Control %q, want no-cache", p, got)
			}
			if w.Header().Get("ETag") == "" {
				t.Errorf("%s: no ETag", p)
			}
		}
	})
	t.Run("If-None-Match gives 304 without a body", func(t *testing.T) {
		first := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/apps/", nil)
		etag := first.Header().Get("ETag")
		for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
			w := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/apps/", nil, ut.Header{Key: "If-None-Match", Value: inm})
			if w.Code != http.StatusNotModified {
				t.Errorf("If-None-Match %q: status %d, want 304", inm, w.Code)
			}
			if w.Body.Len() != 0 {
				t.Errorf("If-None-Match %q: 304 carried a body", inm)
			}
		}
		stale := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/apps/", nil, ut.Header{Key: "If-None-Match", Value: `"stale"`})
		if stale.Code != http.StatusOK {
			t.Errorf("stale validator: status %d, want 200", stale.Code)
		}
	})
	t.Run("the ETag differs per file and follows the content", func(t *testing.T) {
		a := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/apps/", nil).Header().Get("ETag")
		b := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/", nil).Header().Get("ETag")
		if a == b {
			t.Errorf("different files share ETag %s", a)
		}
	})
	t.Run("redirects and 404s are not cached as content", func(t *testing.T) {
		w := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/apps", nil)
		if got := w.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("redirect Cache-Control %q", got)
		}
		n := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/nope/", nil)
		if got := n.Header().Get("Cache-Control"); got != "no-cache" || n.Header().Get("ETag") != "" {
			t.Errorf("404 Cache-Control %q ETag %q", got, n.Header().Get("ETag"))
		}
	})
}

func TestStaticHandler_HEAD(t *testing.T) {
	t.Parallel()
	h := staticEngine(t, exportFS())
	w := ut.PerformRequest(h.Engine, http.MethodHead, "/admin/apps/", nil)
	if w.Code != http.StatusOK || w.Header().Get("ETag") == "" {
		t.Fatalf("HEAD: status %d etag %q", w.Code, w.Header().Get("ETag"))
	}
	r := ut.PerformRequest(h.Engine, http.MethodHead, "/admin/apps", nil)
	if r.Code != http.StatusPermanentRedirect {
		t.Fatalf("HEAD redirect: status %d", r.Code)
	}
}

// D4: each HTML document carries a CSP whose script-src lists the sha256 of the inline
// scripts of THAT document, and never 'unsafe-inline'/'unsafe-eval'.
func TestCSP_HashesMatchInlineScriptsPerDocument(t *testing.T) {
	t.Parallel()
	h := staticEngine(t, exportFS())

	cases := []struct {
		path       string
		status     int
		wantHashes []string
		notHashes  []string
	}{
		{"/admin/", 200, []string{scriptHash(inlineA), scriptHash(inlineB)}, nil},
		{"/admin/apps/", 200, []string{scriptHash(inlineB)}, []string{scriptHash(inlineA)}},
		{"/admin/apps/edit/", 200, []string{scriptHash(inlineA)}, []string{scriptHash(inlineB)}},
		{"/admin/nope/", 404, []string{scriptHash(inlineA)}, []string{scriptHash(inlineB)}},
	}
	for _, tc := range cases {
		w := ut.PerformRequest(h.Engine, http.MethodGet, tc.path, nil)
		if w.Code != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.path, w.Code, tc.status)
		}
		script := cspDirective(w.Header().Get("Content-Security-Policy"), "script-src")
		if script == "" {
			t.Fatalf("%s: no script-src in %q", tc.path, w.Header().Get("Content-Security-Policy"))
		}
		for _, want := range tc.wantHashes {
			if !strings.Contains(script, want) {
				t.Errorf("%s: script-src %q lacks %s", tc.path, script, want)
			}
		}
		for _, not := range tc.notHashes {
			if strings.Contains(script, not) {
				t.Errorf("%s: script-src %q must not list %s", tc.path, script, not)
			}
		}
		if strings.Contains(script, "'unsafe-inline'") || strings.Contains(script, "'unsafe-eval'") {
			t.Errorf("%s: script-src allows unsafe script execution: %q", tc.path, script)
		}
		for _, d := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'", "base-uri 'none'"} {
			if !strings.Contains(w.Header().Get("Content-Security-Policy"), d) {
				t.Errorf("%s: CSP lacks %q", tc.path, d)
			}
		}
	}
}

func TestCSP_NonHTMLKeepsTheDefaultPolicyWithoutHashes(t *testing.T) {
	t.Parallel()
	h := staticEngine(t, exportFS())
	for _, p := range []string{"/admin/_next/static/chunks/app.js", "/admin/__next._tree.txt", "/admin/logo.svg"} {
		w := ut.PerformRequest(h.Engine, http.MethodGet, p, nil)
		script := cspDirective(w.Header().Get("Content-Security-Policy"), "script-src")
		if script != "script-src 'self'" {
			t.Errorf("%s: script-src %q, want the default 'self' only", p, script)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: nosniff missing", p)
		}
	}
}

func TestStaticHandler_NotBuiltAnswers503(t *testing.T) {
	t.Parallel()
	// Only the tracked .keep exists, as on a clean checkout (Q-12).
	h := staticEngine(t, fstest.MapFS{".keep": {}})
	for _, p := range []string{"/admin", "/admin/", "/admin/apps/", "/admin/_next/static/x.js"} {
		w := ut.PerformRequest(h.Engine, http.MethodGet, p, nil)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503", p, w.Code)
		}
		if !strings.Contains(w.Body.String(), "admin UI not built") {
			t.Errorf("%s: body %q", p, w.Body.String())
		}
	}
}

func TestStaticHandler_NoFallbackWithout404Document(t *testing.T) {
	t.Parallel()
	h := staticEngine(t, fstest.MapFS{"index.html": {Data: []byte("<html>ROOT</html>")}})
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/nope/", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
}

func newTestAdminStaticFullHertzEngine(t *testing.T) *server.Hertz {
	t.Helper()

	db := newTestDB(t)
	appStore, err := store.NewAppStore(db)
	if err != nil {
		t.Fatalf("NewAppStore: %v", err)
	}
	queue, err := store.NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	if err := queue.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	tracker := progress.NewProgressTracker()
	cancelService := cancel.NewCancelService(queue)
	apiHandler := NewAdminAPIHandler(appStore, queue, tracker, cancelService)

	sh, err := NewStaticHandler(exportFS())
	if err != nil {
		t.Fatalf("NewStaticHandler: %v", err)
	}
	h := server.New(server.WithHostPorts("127.0.0.1:0"), server.WithDisablePrintRoute(true))
	auth := testHertzSessionAuth(t)
	RegisterAdminAPIRoutesHertz(h, apiHandler, auth)
	RegisterStaticHandlerHertz(h, auth, sh)
	return h
}

func TestStaticHandler_RouteOrderingKeepsAPIJSON(t *testing.T) {
	t.Parallel()
	engine := newTestAdminStaticFullHertzEngine(t)

	w := ut.PerformRequest(engine.Engine, http.MethodGet, "/admin/api/v1/apps", nil, ut.Header{Key: "Cookie", Value: testAdminSessionCookie(t)})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if contentType := w.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected JSON Content-Type, got %q body=%s", contentType, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "<!DOCTYPE html>") || !strings.Contains(w.Body.String(), `"apps"`) {
		t.Fatalf("API fell through to the UI: %s", w.Body.String())
	}
}

func TestStaticHandler_RoutesKeepAPINotFoundJSON(t *testing.T) {
	t.Parallel()
	engine := newTestAdminStaticFullHertzEngine(t)

	for _, path := range []string{"/admin/api/v1", "/admin/api/v1/does-not-exist", "/admin/api", "/admin/api/does-not-exist"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			w := ut.PerformRequest(engine.Engine, http.MethodGet, path, nil, ut.Header{Key: "Cookie", Value: testAdminSessionCookie(t)})
			if w.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
			}
			if contentType := w.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
				t.Fatalf("expected JSON Content-Type, got %q body=%s", contentType, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "<!DOCTYPE html>") {
				t.Fatalf("API 404 fell through to the UI: %s", w.Body.String())
			}
		})
	}
}

// With the v1 auth middleware applied, an unauthenticated browser gets the JSON error
// contract: no redirect to a retired login and no Basic challenge. (cmd/server mounts
// the static shell publicly, Q-17; this keeps the handlers safe to put behind auth.)
func TestStaticHandler_ComposesBehindAuth(t *testing.T) {
	t.Parallel()
	sh, err := NewStaticHandler(exportFS())
	if err != nil {
		t.Fatal(err)
	}
	h := server.New(server.WithHostPorts("127.0.0.1:0"), server.WithDisablePrintRoute(true))
	RegisterStaticHandlerHertz(h, testHertzSessionAuth(t), sh)

	for _, path := range []string{"/admin", "/admin/", "/admin/_next/static/chunks/app.js"} {
		w := ut.PerformRequest(h.Engine, http.MethodGet, path, nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401, got %d body=%s", path, w.Code, w.Body.String())
		}
		if w.Header().Get("Location") != "" || w.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("%s: must neither redirect nor challenge", path)
		}
		if !strings.Contains(w.Body.String(), `"code":"session_expired"`) {
			t.Fatalf("%s: expected the JSON error contract, got %s", path, w.Body.String())
		}
	}
}
