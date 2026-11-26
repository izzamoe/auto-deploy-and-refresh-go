package admin

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

// The Next.js static export (basePath /admin, trailingSlash) is copied here by
// scripts/embed-admin-ui.sh. The build output is not committed (Q-12): only .keep is
// tracked so this directive compiles on a clean checkout.
//
//go:embed all:web/admin/dist
var adminDist embed.FS

const (
	adminBasePath          = "/admin"
	cacheImmutable         = "public, max-age=31536000, immutable"
	cacheRevalidate        = "no-cache"
	msgAdminUIUnavailable  = "admin UI not built: run `make admin-ui` and rebuild the binary"
	immutableAssetsPrefix  = "_next/static/"
	notFoundDocument       = "404.html"
	indexDocument          = "index.html"
	contentTypeTextPlain   = "text/plain; charset=utf-8"
	contentTypeOctetStream = "application/octet-stream"
	// gzipMinSize: smaller text files are sent as is (the gzip header and a second
	// representation are not worth it).
	gzipMinSize = 1024
)

// staticEntry is one embedded file, indexed once at start (D1, D4).
type staticEntry struct {
	body        []byte
	etag        string
	contentType string
	// csp is set for HTML documents only: script-src lists the sha256 of every inline
	// script of THAT document (D4), never 'unsafe-inline'. Computed from the identity body,
	// so it is the same for both representations.
	csp string
	// gz is the gzip representation (002 T074, D9, FR-030), computed once at index time for
	// text files larger than gzipMinSize when it is smaller; nil otherwise. It has its own
	// validator gzEtag (same value + "-gz"): another representation, another ETag.
	gz     []byte
	gzEtag string
}

// StaticHandler serves the embedded Next.js export under /admin/ (replaces the Vite SPA
// handler): path mapping per D1, per-document CSP per D4, cache headers per FR-092.
type StaticHandler struct {
	files map[string]*staticEntry
}

// NewStaticHandler indexes every file of fsys (the export root). It reads and hashes
// everything at start, so request handling does no I/O.
func NewStaticHandler(fsys fs.FS) (*StaticHandler, error) {
	h := &StaticHandler{files: map[string]*staticEntry{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == ".keep" {
			return nil
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		e := &staticEntry{
			body:        body,
			etag:        `"` + hex.EncodeToString(sum[:16]) + `"`,
			contentType: staticContentType(p),
		}
		if strings.HasSuffix(p, ".html") {
			e.csp = BuildCSP(InlineScriptHashes(body))
		}
		if compressible(p) && len(body) > gzipMinSize {
			if gz, err := gzipBytes(body); err == nil && len(gz) < len(body) {
				e.gz = gz
				e.gzEtag = strings.TrimSuffix(e.etag, `"`) + `-gz"`
			}
		}
		h.files[p] = e
		return nil
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

// compressible reports whether a file is text that gzip shrinks (fonts and images are
// already compressed; .svg is XML text).
func compressible(name string) bool {
	switch path.Ext(name) {
	case ".html", ".js", ".mjs", ".css", ".txt", ".json", ".svg", ".map", ".xml", ".webmanifest":
		return true
	}
	return false
}

func gzipBytes(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(body); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// acceptsGzip reads Accept-Encoding (RFC 9110 §12.5.3): gzip (or x-gzip, or `*`) with a
// q-value above 0. An explicit gzip;q=0 refuses it even when `*` is accepted.
func acceptsGzip(header string) bool {
	star := false
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		coding := strings.ToLower(strings.TrimSpace(fields[0]))
		q := 1.0
		for _, param := range fields[1:] {
			k, v, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		switch coding {
		case "gzip", "x-gzip":
			return q > 0
		case "*":
			star = q > 0
		}
	}
	return star
}

// stats reports how many files were indexed and their total and gzip sizes (startup log).
func (h *StaticHandler) stats() (files, gzipped, bytesTotal, gzBytes int) {
	for _, e := range h.files {
		files++
		bytesTotal += len(e.body)
		if e.gz != nil {
			gzipped++
			gzBytes += len(e.gz)
		}
	}
	return
}

// Built reports whether a UI export is present (an index.html exists).
func (h *StaticHandler) Built() bool {
	_, ok := h.files[indexDocument]
	return ok
}

func staticContentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".txt":
		// Next RSC payloads (`index.txt`, `__next.*.txt`); Next accepts text/plain.
		return contentTypeTextPlain
	case ".woff2":
		return "font/woff2"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return contentTypeOctetStream
}

// RegisterAdminStaticRoutesHertz mounts the embedded export. It must be registered
// AFTER every more specific /admin route (API, events), like the handler it replaces.
func RegisterAdminStaticRoutesHertz(h *server.Hertz, auth app.HandlerFunc) {
	sub, err := fs.Sub(adminDist, "web/admin/dist")
	if err != nil {
		slog.Error("failed to open the embedded admin UI", "err", err)
		sub = nil
	}
	var sh *StaticHandler
	if sub != nil {
		start := time.Now()
		if sh, err = NewStaticHandler(sub); err != nil {
			slog.Error("failed to index the embedded admin UI", "err", err)
			sh = nil
		} else {
			// T074: the gzip representations are computed here, once; log what it cost.
			files, gzipped, total, gz := sh.stats()
			slog.Info("admin UI indexed", "files", files, "gzipped", gzipped, "bytes", total, "gzip_bytes", gz, "duration", time.Since(start).String())
		}
	}
	if sh == nil {
		sh = &StaticHandler{files: map[string]*staticEntry{}}
	}
	if !sh.Built() {
		slog.Warn("admin UI is not built into this binary; /admin answers 503 (run make admin-ui)")
	}
	RegisterStaticHandlerHertz(h, auth, sh)
}

// RegisterStaticHandlerHertz mounts sh on /admin and /admin/*path (GET and HEAD).
func RegisterStaticHandlerHertz(h *server.Hertz, auth app.HandlerFunc, sh *StaticHandler) {
	h.GET(adminBasePath, auth, sh.serve)
	h.HEAD(adminBasePath, auth, sh.serve)
	h.GET(adminBasePath+"/*path", auth, sh.serve)
	h.HEAD(adminBasePath+"/*path", auth, sh.serve)
}

func (h *StaticHandler) serve(ctx context.Context, c *app.RequestContext) {
	if !h.Built() {
		c.Response.Header.Set("Cache-Control", "no-store")
		c.Data(http.StatusServiceUnavailable, contentTypeTextPlain, []byte(msgAdminUIUnavailable))
		return
	}

	p := string(c.Request.URI().Path())
	if p == adminBasePath {
		h.redirect(c, adminBasePath+"/")
		return
	}
	rel := strings.TrimPrefix(p, adminBasePath+"/")
	if rel == p || strings.Contains(rel, "\\") || strings.ContainsRune(rel, 0) {
		h.notFound(c)
		return
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." || seg == "." {
			h.notFound(c)
			return
		}
	}

	// D1: a directory path serves its index.html; a file serves as is; a directory
	// path without the trailing slash redirects to it (query kept).
	if rel == "" || strings.HasSuffix(rel, "/") {
		if e, ok := h.files[rel+indexDocument]; ok {
			h.respond(c, rel, e, http.StatusOK)
			return
		}
		h.notFound(c)
		return
	}
	if e, ok := h.files[rel]; ok {
		h.respond(c, rel, e, http.StatusOK)
		return
	}
	if _, ok := h.files[rel+"/"+indexDocument]; ok {
		h.redirect(c, adminBasePath+"/"+rel+"/")
		return
	}
	h.notFound(c)
}

func (h *StaticHandler) redirect(c *app.RequestContext, target string) {
	if q := c.Request.URI().QueryString(); len(q) > 0 {
		target += "?" + string(q)
	}
	c.Response.Header.Set("Cache-Control", cacheRevalidate)
	c.Redirect(http.StatusPermanentRedirect, []byte(target))
}

func (h *StaticHandler) notFound(c *app.RequestContext) {
	if e, ok := h.files[notFoundDocument]; ok {
		h.respond(c, notFoundDocument, e, http.StatusNotFound)
		return
	}
	c.Response.Header.Set("Cache-Control", cacheRevalidate)
	c.Data(http.StatusNotFound, contentTypeTextPlain, []byte("not found"))
}

func (h *StaticHandler) respond(c *app.RequestContext, rel string, e *staticEntry, status int) {
	hdr := &c.Response.Header
	hdr.Set("Content-Type", e.contentType)
	immutable := status == http.StatusOK && strings.HasPrefix(rel, immutableAssetsPrefix)
	if immutable {
		// FR-092: files under _next/static are content-hashed.
		hdr.Set("Cache-Control", cacheImmutable)
	} else {
		hdr.Set("Cache-Control", cacheRevalidate)
	}
	if e.csp != "" {
		// D4: replaces the default policy set by SecurityHeaders with one that lists
		// the hashes of this document's inline scripts.
		hdr.Set("Content-Security-Policy", e.csp)
	}
	// T074 (FR-030): the gzip representation when the client accepts it. Both
	// representations of such a file vary on Accept-Encoding (shared caches, Cloudflare).
	body, etag := e.body, e.etag
	if e.gz != nil {
		hdr.Set("Vary", "Accept-Encoding")
		if acceptsGzip(string(c.GetHeader("Accept-Encoding"))) {
			body, etag = e.gz, e.gzEtag
			hdr.Set("Content-Encoding", "gzip")
		}
	}
	if status == http.StatusOK && !immutable {
		hdr.Set("ETag", etag)
		if etagMatches(string(c.GetHeader("If-None-Match")), etag) {
			c.SetStatusCode(http.StatusNotModified)
			return
		}
	}
	if c.Request.Header.IsHead() {
		// HEAD: the headers of the selected representation (its length), no body.
		c.SetStatusCode(status)
		hdr.SetContentLength(len(body))
		c.Response.SkipBody = true
		return
	}
	c.Data(status, e.contentType, body)
}

// etagMatches implements If-None-Match weak comparison for one validator.
func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
