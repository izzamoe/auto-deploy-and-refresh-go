package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func newTestAdminSPAMux(t *testing.T) *http.ServeMux {
	t.Helper()

	db := newTestDB(t)
	store, err := NewAppStore(db)
	if err != nil {
		t.Fatalf("NewAppStore: %v", err)
	}
	queue, err := NewDeployQueue(db, 10)
	if err != nil {
		t.Fatalf("NewDeployQueue: %v", err)
	}
	tracker := NewProgressTracker()
	cancelService := NewCancelService(queue)
	apiHandler := NewAdminAPIHandler(store, queue, tracker, cancelService)
	eventHub := NewAdminEventHub(tracker)

	mux := http.NewServeMux()
	authMiddleware := BasicAuthMiddleware("admin", "secret")
	RegisterAdminEventRoutes(mux, eventHub, authMiddleware)
	RegisterAdminAPIRoutes(mux, apiHandler, authMiddleware)
	RegisterAdminSPARoutes(mux, authMiddleware)
	return mux
}

func authenticatedAdminSPARequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.SetBasicAuth("admin", "secret")
	return req
}

func serveAdminSPA(t *testing.T, mux *http.ServeMux, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func assertAdminSPAIndex(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if contentType := rr.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("expected HTML Content-Type, got %q", contentType)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `<div id="root"></div>`) || !strings.Contains(body, "/admin/assets/") {
		t.Fatalf("expected embedded React index.html, got %s", body)
	}
}

func TestAdminSPAFallbackServesClientRoutes(t *testing.T) {
	mux := newTestAdminSPAMux(t)

	for _, path := range []string{"/admin", "/admin/", "/admin/apps"} {
		t.Run(path, func(t *testing.T) {
			rr := serveAdminSPA(t, mux, authenticatedAdminSPARequest(http.MethodGet, path))
			assertAdminSPAIndex(t, rr)
		})
	}
}

func TestAdminSPARouteOrderingKeepsAPIJSON(t *testing.T) {
	mux := newTestAdminSPAMux(t)

	rr := serveAdminSPA(t, mux, authenticatedAdminSPARequest(http.MethodGet, "/admin/api/apps"))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if contentType := rr.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected JSON Content-Type, got %q body=%s", contentType, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, `<div id="root"></div>`) || strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("API fell through to SPA HTML: %s", body)
	}
	if !strings.Contains(body, `"apps"`) {
		t.Fatalf("expected apps JSON body, got %s", body)
	}
}

func TestAdminSPARoutesKeepAPINotFoundJSON(t *testing.T) {
	mux := newTestAdminSPAMux(t)

	for _, path := range []string{"/admin/api", "/admin/api/does-not-exist"} {
		t.Run(path, func(t *testing.T) {
			rr := serveAdminSPA(t, mux, authenticatedAdminSPARequest(http.MethodGet, path))
			if rr.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d body=%s", rr.Code, rr.Body.String())
			}
			if contentType := rr.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
				t.Fatalf("expected JSON Content-Type, got %q body=%s", contentType, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "<!DOCTYPE html>") {
				t.Fatalf("API 404 fell through to SPA HTML: %s", rr.Body.String())
			}
		})
	}
}

func TestAdminSPARoutesKeepWebSocketOutOfFallback(t *testing.T) {
	mux := newTestAdminSPAMux(t)

	rr := serveAdminSPA(t, mux, authenticatedAdminSPARequest(http.MethodGet, "/admin/events/ws"))

	if rr.Code == http.StatusOK {
		t.Fatalf("expected non-200 websocket protocol response without upgrade, got body=%s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `<div id="root"></div>`) || strings.Contains(rr.Body.String(), "<!DOCTYPE html>") {
		t.Fatalf("websocket route fell through to SPA HTML: %s", rr.Body.String())
	}
}

func TestAdminSPARoutesServeAssetsWithCacheAndNoFallback(t *testing.T) {
	mux := newTestAdminSPAMux(t)

	indexRR := serveAdminSPA(t, mux, authenticatedAdminSPARequest(http.MethodGet, "/admin"))
	assertAdminSPAIndex(t, indexRR)
	assetPath := regexp.MustCompile(`/admin/assets/[^"']+`).FindString(indexRR.Body.String())
	if assetPath == "" {
		t.Fatalf("expected index.html to reference a built asset; run npm run admin:build")
	}

	assetRR := serveAdminSPA(t, mux, authenticatedAdminSPARequest(http.MethodGet, assetPath))
	if assetRR.Code != http.StatusOK {
		t.Fatalf("expected asset 200, got %d body=%s", assetRR.Code, assetRR.Body.String())
	}
	if got := assetRR.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("expected immutable asset cache header, got %q", got)
	}
	if contentType := assetRR.Header().Get("Content-Type"); strings.HasPrefix(contentType, "text/html") || contentType == "" {
		t.Fatalf("expected static asset Content-Type, got %q", contentType)
	}

	missingRR := serveAdminSPA(t, mux, authenticatedAdminSPARequest(http.MethodGet, "/admin/assets/missing.js"))
	if missingRR.Code != http.StatusNotFound {
		t.Fatalf("expected missing asset 404, got %d body=%s", missingRR.Code, missingRR.Body.String())
	}
	if strings.Contains(missingRR.Body.String(), `<div id="root"></div>`) || strings.Contains(missingRR.Body.String(), "<!DOCTYPE html>") {
		t.Fatalf("missing asset fell through to SPA HTML: %s", missingRR.Body.String())
	}
}

func TestAdminSPARoutesRequireAuth(t *testing.T) {
	mux := newTestAdminSPAMux(t)

	for _, path := range []string{"/admin", "/admin/assets/missing.js"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rr := serveAdminSPA(t, mux, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("WWW-Authenticate"); got != `Basic realm="auto-deploy admin"` {
				t.Fatalf("expected WWW-Authenticate header, got %q", got)
			}
		})
	}
}

func newTestAdminSPAHertzEngine(t *testing.T) *server.Hertz {
	t.Helper()
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	auth := HertzBasicAuthMiddleware("admin", "secret")
	RegisterAdminSPARoutesHertz(h, auth)
	return h
}

func TestAdminSPARoutesHertzFallbackServesClientRoutes(t *testing.T) {
	engine := newTestAdminSPAHertzEngine(t)

	for _, path := range []string{"/admin", "/admin/apps"} {
		t.Run(path, func(t *testing.T) {
			w := ut.PerformRequest(engine.Engine, http.MethodGet, path, nil, ut.Header{Key: "Authorization", Value: "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:secret"))})
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
			}
			if contentType := w.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
				t.Fatalf("expected HTML Content-Type, got %q", contentType)
			}
			body := w.Body.String()
			if !strings.Contains(body, `<div id="root"></div>`) || !strings.Contains(body, "/admin/assets/") {
				t.Fatalf("expected embedded React index.html, got %s", body)
			}
		})
	}
}

func TestAdminSPARoutesHertzServeAssetsWithCache(t *testing.T) {
	engine := newTestAdminSPAHertzEngine(t)

	indexW := ut.PerformRequest(engine.Engine, http.MethodGet, "/admin", nil, ut.Header{Key: "Authorization", Value: "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:secret"))})
	assetPath := regexp.MustCompile(`/admin/assets/[^"']+`).FindString(indexW.Body.String())
	if assetPath == "" {
		t.Fatalf("expected index.html to reference a built asset; run npm run admin:build")
	}

	assetW := ut.PerformRequest(engine.Engine, http.MethodGet, assetPath, nil, ut.Header{Key: "Authorization", Value: "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:secret"))})
	if assetW.Code != http.StatusOK {
		t.Fatalf("expected asset 200, got %d body=%s", assetW.Code, assetW.Body.String())
	}
	if got := assetW.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("expected immutable asset cache header, got %q", got)
	}

	missingW := ut.PerformRequest(engine.Engine, http.MethodGet, "/admin/assets/missing.js", nil, ut.Header{Key: "Authorization", Value: "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:secret"))})
	if missingW.Code != http.StatusNotFound {
		t.Fatalf("expected missing asset 404, got %d body=%s", missingW.Code, missingW.Body.String())
	}
}

func TestAdminSPARoutesHertzRequireAuth(t *testing.T) {
	engine := newTestAdminSPAHertzEngine(t)

	for _, path := range []string{"/admin", "/admin/assets/missing.js"} {
		t.Run(path, func(t *testing.T) {
			w := ut.PerformRequest(engine.Engine, http.MethodGet, path, nil)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d body=%s", w.Code, w.Body.String())
			}
			if got := w.Header().Get("WWW-Authenticate"); got != `Basic realm="auto-deploy admin"` {
				t.Fatalf("expected WWW-Authenticate header, got %q", got)
			}
		})
	}
}
