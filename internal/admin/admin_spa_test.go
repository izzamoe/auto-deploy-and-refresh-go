package admin

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func newTestAdminSPAFullHertzEngine(t *testing.T) *server.Hertz {
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

	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	auth := testHertzSessionAuth(t)
	RegisterAdminAPIRoutesHertz(h, apiHandler, auth)
	RegisterAdminSPARoutesHertz(h, auth)
	return h
}

func TestAdminSPAFallbackServesClientRoutes(t *testing.T) {
	TestAdminSPARoutesHertzFallbackServesClientRoutes(t)
}

func TestAdminSPARouteOrderingKeepsAPIJSON(t *testing.T) {
	t.Parallel()
	engine := newTestAdminSPAFullHertzEngine(t)

	w := ut.PerformRequest(engine.Engine, http.MethodGet, "/admin/api/apps", nil, ut.Header{Key: "Cookie", Value: testAdminSessionCookie(t)})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if contentType := w.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected JSON Content-Type, got %q body=%s", contentType, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, `<div id="root"></div>`) || strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("API fell through to SPA HTML: %s", body)
	}
	if !strings.Contains(body, `"apps"`) {
		t.Fatalf("expected apps JSON body, got %s", body)
	}
}

func TestAdminSPARoutesKeepAPINotFoundJSON(t *testing.T) {
	t.Parallel()
	engine := newTestAdminSPAFullHertzEngine(t)

	for _, path := range []string{"/admin/api", "/admin/api/does-not-exist"} {
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
				t.Fatalf("API 404 fell through to SPA HTML: %s", w.Body.String())
			}
		})
	}
}

func TestAdminSPARoutesServeAssetsWithCacheAndNoFallback(t *testing.T) {
	TestAdminSPARoutesHertzServeAssetsWithCache(t)
}

func TestAdminSPARoutesRequireAuth(t *testing.T) {
	TestAdminSPARoutesHertzRequireAuth(t)
}

func newTestAdminSPAHertzEngine(t *testing.T) *server.Hertz {
	t.Helper()
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	auth := testHertzSessionAuth(t)
	RegisterAdminSPARoutesHertz(h, auth)
	return h
}

func TestAdminSPARoutesHertzFallbackServesClientRoutes(t *testing.T) {
	t.Parallel()
	engine := newTestAdminSPAHertzEngine(t)

	for _, path := range []string{"/admin", "/admin/apps"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			w := ut.PerformRequest(engine.Engine, http.MethodGet, path, nil, ut.Header{Key: "Cookie", Value: testAdminSessionCookie(t)})
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
	t.Parallel()
	engine := newTestAdminSPAHertzEngine(t)

	indexW := ut.PerformRequest(engine.Engine, http.MethodGet, "/admin", nil, ut.Header{Key: "Cookie", Value: testAdminSessionCookie(t)})
	assetPath := regexp.MustCompile(`/admin/assets/[^"']+`).FindString(indexW.Body.String())
	if assetPath == "" {
		t.Fatalf("expected index.html to reference a built asset; run npm run admin:build")
	}

	assetW := ut.PerformRequest(engine.Engine, http.MethodGet, assetPath, nil, ut.Header{Key: "Cookie", Value: testAdminSessionCookie(t)})
	if assetW.Code != http.StatusOK {
		t.Fatalf("expected asset 200, got %d body=%s", assetW.Code, assetW.Body.String())
	}
	if got := assetW.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("expected immutable asset cache header, got %q", got)
	}

	missingW := ut.PerformRequest(engine.Engine, http.MethodGet, "/admin/assets/missing.js", nil, ut.Header{Key: "Cookie", Value: testAdminSessionCookie(t)})
	if missingW.Code != http.StatusNotFound {
		t.Fatalf("expected missing asset 404, got %d body=%s", missingW.Code, missingW.Body.String())
	}
}

func TestAdminSPARoutesHertzRequireAuth(t *testing.T) {
	t.Parallel()
	engine := newTestAdminSPAHertzEngine(t)

	for _, path := range []string{"/admin", "/admin/assets/missing.js"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			w := ut.PerformRequest(engine.Engine, http.MethodGet, path, nil)
			if w.Code != http.StatusFound {
				t.Fatalf("expected 401, got %d body=%s", w.Code, w.Body.String())
			}
			if got := w.Header().Get("Location"); got != "/admin/login" {
				t.Fatalf("expected login redirect, got %q", got)
			}
		})
	}
}
