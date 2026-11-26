package admin

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cloudwego/hertz/pkg/common/ut"
)

// 002 T074 (FR-030, SC-009, Q-7=A): text assets are gzipped once at index time and sent
// compressed when the browser accepts gzip, without changing CSP, cache headers or the
// meaning of the validators (the gzip representation has its own ETag).

var (
	bigJS   = strings.Repeat("console.log('auto-deploy admin chunk');\n", 200)
	bigCSS  = strings.Repeat(".card{border-radius:1rem;padding:1rem}\n", 100)
	bigText = strings.Repeat("0:[\"$\",\"div\",null,{\"children\":\"rsc\"}]\n", 100)
)

func gzipExportFS() fstest.MapFS {
	fsys := exportFS()
	var page strings.Builder
	page.WriteString(`<!DOCTYPE html><html><head><title>BIG</title><script src="/admin/_next/static/chunks/big.js" async=""></script></head><body>`)
	page.WriteString(strings.Repeat("<p>auto-deploy</p>", 200))
	page.WriteString("<script>" + inlineA + "</script></body></html>")
	fsys["big/index.html"] = &fstest.MapFile{Data: []byte(page.String())}
	fsys["_next/static/chunks/big.js"] = &fstest.MapFile{Data: []byte(bigJS)}
	fsys["_next/static/chunks/big.css"] = &fstest.MapFile{Data: []byte(bigCSS)}
	fsys["big/index.txt"] = &fstest.MapFile{Data: []byte(bigText)}
	fsys["_next/static/media/big.woff2"] = &fstest.MapFile{Data: bytes.Repeat([]byte("font"), 1000)}
	return fsys
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return out
}

func acceptGzip(v string) ut.Header { return ut.Header{Key: "Accept-Encoding", Value: v} }

func TestStatic_Gzip(t *testing.T) {
	t.Parallel()
	fsys := gzipExportFS()
	h := staticEngine(t, fsys)

	for _, tc := range []struct{ path, file, ctype string }{
		{"/admin/_next/static/chunks/big.js", "_next/static/chunks/big.js", "text/javascript; charset=utf-8"},
		{"/admin/_next/static/chunks/big.css", "_next/static/chunks/big.css", "text/css; charset=utf-8"},
		{"/admin/big/", "big/index.html", "text/html; charset=utf-8"},
		{"/admin/big/index.txt", "big/index.txt", contentTypeTextPlain},
	} {
		w := ut.PerformRequest(h.Engine, http.MethodGet, tc.path, nil, acceptGzip("gzip, deflate, br, zstd"))
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("%s: status %d Content-Encoding %q, want 200 gzip", tc.path, w.Code, w.Header().Get("Content-Encoding"))
		}
		if got := w.Header().Get("Content-Type"); got != tc.ctype {
			t.Fatalf("%s: Content-Type %q, want %q (the type of the content, not of the encoding)", tc.path, got, tc.ctype)
		}
		body := w.Body.Bytes()
		if want := fsys[tc.file].Data; !bytes.Equal(gunzip(t, body), want) {
			t.Fatalf("%s: gunzipped body differs from the file", tc.path)
		}
		if len(body) >= len(fsys[tc.file].Data) {
			t.Fatalf("%s: gzip body %d B is not smaller than %d B", tc.path, len(body), len(fsys[tc.file].Data))
		}
	}

	// Cache headers are those of the identity representation (FR-092).
	js := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/_next/static/chunks/big.js", nil, acceptGzip("gzip"))
	if got := js.Header().Get("Cache-Control"); got != cacheImmutable {
		t.Fatalf("gzip chunk Cache-Control %q, want %q", got, cacheImmutable)
	}

	// No gzip without (or with a refused) Accept-Encoding; small files and binary formats stay identity.
	for _, tc := range []struct {
		name, path string
		hdr        []ut.Header
	}{
		{"no Accept-Encoding", "/admin/_next/static/chunks/big.js", nil},
		{"gzip;q=0", "/admin/_next/static/chunks/big.js", []ut.Header{acceptGzip("gzip;q=0, br")}},
		{"identity only", "/admin/_next/static/chunks/big.js", []ut.Header{acceptGzip("identity")}},
		{"small file", "/admin/_next/static/chunks/app.js", []ut.Header{acceptGzip("gzip")}},
		{"binary font", "/admin/_next/static/media/big.woff2", []ut.Header{acceptGzip("gzip")}},
	} {
		w := ut.PerformRequest(h.Engine, http.MethodGet, tc.path, nil, tc.hdr...)
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s: status %d Content-Encoding %q, want 200 identity", tc.name, w.Code, w.Header().Get("Content-Encoding"))
		}
		rel := strings.TrimPrefix(tc.path, "/admin/")
		if !bytes.Equal(w.Body.Bytes(), fsys[rel].Data) {
			t.Fatalf("%s: identity body differs from the file", tc.name)
		}
	}

	// `*` accepts gzip; `x-gzip` is gzip.
	for _, ae := range []string{"*", "x-gzip", "GZIP"} {
		w := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/_next/static/chunks/big.js", nil, acceptGzip(ae))
		if w.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("Accept-Encoding %q: Content-Encoding %q, want gzip", ae, w.Header().Get("Content-Encoding"))
		}
	}
}

func TestStatic_Vary(t *testing.T) {
	t.Parallel()
	h := staticEngine(t, gzipExportFS())
	// Both representations of a compressible file say that they depend on Accept-Encoding.
	for _, hdr := range [][]ut.Header{nil, {acceptGzip("gzip")}} {
		for _, p := range []string{"/admin/_next/static/chunks/big.js", "/admin/big/"} {
			w := ut.PerformRequest(h.Engine, http.MethodGet, p, nil, hdr...)
			if !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
				t.Fatalf("%s (%v): Vary %q, want Accept-Encoding", p, hdr, w.Header().Get("Vary"))
			}
		}
	}
	// A file that has only one representation does not vary.
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/_next/static/media/big.woff2", nil, acceptGzip("gzip"))
	if strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("woff2: Vary %q, want none", w.Header().Get("Vary"))
	}
}

func TestStatic_ETagVariant(t *testing.T) {
	t.Parallel()
	h := staticEngine(t, gzipExportFS())
	plain := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/big/", nil)
	gz := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/big/", nil, acceptGzip("gzip"))
	pe, ge := plain.Header().Get("ETag"), gz.Header().Get("ETag")
	if pe == "" || ge == "" || pe == ge {
		t.Fatalf("ETag identity %q gzip %q: want two different validators", pe, ge)
	}
	if want := strings.TrimSuffix(pe, `"`) + `-gz"`; ge != want {
		t.Fatalf("gzip ETag %q, want %q", ge, want)
	}

	// 304 per representation, with the validator and Vary of that representation.
	nm := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/big/", nil, acceptGzip("gzip"), ut.Header{Key: "If-None-Match", Value: ge})
	if nm.Code != http.StatusNotModified || nm.Header().Get("ETag") != ge || !strings.Contains(nm.Header().Get("Vary"), "Accept-Encoding") || len(nm.Body.Bytes()) != 0 {
		t.Fatalf("If-None-Match gzip etag: status %d etag %q vary %q body %d B", nm.Code, nm.Header().Get("ETag"), nm.Header().Get("Vary"), len(nm.Body.Bytes()))
	}
	nmPlain := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/big/", nil, ut.Header{Key: "If-None-Match", Value: pe})
	if nmPlain.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match identity etag without gzip: status %d, want 304", nmPlain.Code)
	}
	// The identity validator does not validate the gzip representation (and vice versa).
	cross := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/big/", nil, acceptGzip("gzip"), ut.Header{Key: "If-None-Match", Value: pe})
	if cross.Code != http.StatusOK || cross.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("identity etag with gzip accepted: status %d enc %q, want 200 gzip", cross.Code, cross.Header().Get("Content-Encoding"))
	}
	cross2 := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/big/", nil, ut.Header{Key: "If-None-Match", Value: ge})
	if cross2.Code != http.StatusOK || cross2.Header().Get("Content-Encoding") != "" {
		t.Fatalf("gzip etag without gzip accepted: status %d enc %q, want 200 identity", cross2.Code, cross2.Header().Get("Content-Encoding"))
	}
}

func TestStatic_CSPUnchanged(t *testing.T) {
	t.Parallel()
	h := staticEngine(t, gzipExportFS())
	plain := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/big/", nil)
	gz := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/big/", nil, acceptGzip("gzip"))
	pc, gc := plain.Header().Get("Content-Security-Policy"), gz.Header().Get("Content-Security-Policy")
	if pc == "" || pc != gc {
		t.Fatalf("CSP identity %q vs gzip %q: want identical", pc, gc)
	}
	if !strings.Contains(cspDirective(gc, "script-src"), scriptHash(inlineA)) {
		t.Fatalf("gzip CSP lacks the inline script hash: %q", gc)
	}
	if gz.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("HTML not gzipped: %q", gz.Header().Get("Content-Encoding"))
	}
	// Same for the 404 document (served with status 404).
	nf := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/nope/", nil, acceptGzip("gzip"))
	nfPlain := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/nope/", nil)
	if nf.Code != http.StatusNotFound || nf.Header().Get("Content-Security-Policy") != nfPlain.Header().Get("Content-Security-Policy") {
		t.Fatalf("404: status %d CSP differs with gzip", nf.Code)
	}
}

func TestStatic_HeadGzip(t *testing.T) {
	t.Parallel()
	h := staticEngine(t, gzipExportFS())
	get := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/big/", nil, acceptGzip("gzip"))
	head := ut.PerformRequest(h.Engine, http.MethodHead, "/admin/big/", nil, acceptGzip("gzip"))
	if head.Code != http.StatusOK || head.Header().Get("Content-Encoding") != "gzip" || head.Header().Get("ETag") != get.Header().Get("ETag") {
		t.Fatalf("HEAD gzip: status %d enc %q etag %q (GET etag %q)", head.Code, head.Header().Get("Content-Encoding"), head.Header().Get("ETag"), get.Header().Get("ETag"))
	}
	if len(head.Body.Bytes()) != 0 {
		t.Fatalf("HEAD has a body of %d B", len(head.Body.Bytes()))
	}
	if cl := head.Header().Get("Content-Length"); cl != "" && cl != strconv.Itoa(len(get.Body.Bytes())) {
		t.Fatalf("HEAD Content-Length %s, want the gzip length %d", cl, len(get.Body.Bytes()))
	}
	nm := ut.PerformRequest(h.Engine, http.MethodHead, "/admin/big/", nil, acceptGzip("gzip"), ut.Header{Key: "If-None-Match", Value: get.Header().Get("ETag")})
	if nm.Code != http.StatusNotModified {
		t.Fatalf("HEAD If-None-Match gzip etag: status %d, want 304", nm.Code)
	}
}
