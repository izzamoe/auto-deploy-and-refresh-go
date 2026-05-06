package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestAdminAuthRequiresCredentials(t *testing.T) {
	middleware := BasicAuthMiddleware("admin", "secret")
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/admin/apps", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized, got %d", rr.Code)
	}

	authHeader := rr.Header().Get("WWW-Authenticate")
	expectedHeader := `Basic realm="auto-deploy admin"`
	if authHeader != expectedHeader {
		t.Errorf("Expected WWW-Authenticate header %q, got %q", expectedHeader, authHeader)
	}
}

func TestAdminAuthRejectsWrongPassword(t *testing.T) {
	middleware := BasicAuthMiddleware("admin", "secret")
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/admin/apps", nil)
	req.SetBasicAuth("admin", "wrong")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for wrong password, got %d", rr.Code)
	}
}

func TestAdminAuthAdminUIRequiresCredentialsWithWWWAuthenticate(t *testing.T) {
	middleware := BasicAuthMiddleware("admin", "secret")
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/admin/apps", nil)
	req.Header.Set(adminUIRequestHeader, "true")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for AdminUI request, got %d", rr.Code)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got != `Basic realm="auto-deploy admin"` {
		t.Errorf("Expected WWW-Authenticate header on AdminUI request, got %q", got)
	}
}

func TestAdminAuthAcceptsCorrectCredentials(t *testing.T) {
	middleware := BasicAuthMiddleware("admin", "secret")
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))

	req := httptest.NewRequest("GET", "/admin/apps", nil)
	req.SetBasicAuth("admin", "secret")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for correct credentials, got %d", rr.Code)
	}

	if rr.Body.String() != "success" {
		t.Errorf("Expected body 'success', got %q", rr.Body.String())
	}
}

func TestHertzAdminAuthRequiresCredentials(t *testing.T) {
	middleware := HertzBasicAuthMiddleware("admin", "secret")
	c := newHertzTestContext("/admin/apps")

	middleware(context.Background(), c)

	if c.Response.StatusCode() != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized, got %d", c.Response.StatusCode())
	}
	if got := string(c.Response.Header.Peek("WWW-Authenticate")); got != `Basic realm="auto-deploy admin"` {
		t.Errorf("Expected WWW-Authenticate header %q, got %q", `Basic realm="auto-deploy admin"`, got)
	}
}

func TestHertzAdminAuthRejectsWrongPassword(t *testing.T) {
	middleware := HertzBasicAuthMiddleware("admin", "secret")
	c := newHertzTestContext("/admin/apps")
	c.Request.SetBasicAuth("admin", "wrong")

	middleware(context.Background(), c)

	if c.Response.StatusCode() != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for wrong password, got %d", c.Response.StatusCode())
	}
}

func TestHertzAdminAuthAcceptsCorrectCredentials(t *testing.T) {
	middleware := HertzBasicAuthMiddleware("admin", "secret")
	c := newHertzTestContext("/admin/apps")
	c.Request.SetBasicAuth("admin", "secret")
	c.SetHandlers(app.HandlersChain{func(ctx context.Context, c *app.RequestContext) {
		c.SetStatusCode(http.StatusOK)
		c.Response.SetBodyString("success")
	}})

	middleware(context.Background(), c)

	if c.Response.StatusCode() != http.StatusOK {
		t.Errorf("Expected 200 OK for correct credentials, got %d", c.Response.StatusCode())
	}
	if body := string(c.Response.Body()); body != "success" {
		t.Errorf("Expected body 'success', got %q", body)
	}
}

func TestAdminLayoutRendersAppsTableSelector(t *testing.T) {
	cfg := &ServiceConfig{}
	adminHandler, err := NewAdminHandler(cfg)
	if err != nil {
		t.Fatalf("Failed to create admin handler: %v", err)
	}

	var buf bytes.Buffer
	data := map[string]interface{}{}
	err = adminHandler.templates["apps_list.html"].ExecuteTemplate(&buf, "base.html", data)
	if err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `id="apps-table"`) {
		t.Errorf("Expected template output to contain 'id=\"apps-table\"', got %s", output)
	}
}

func TestAdminLayoutRendersNavigationLinks(t *testing.T) {
	cfg := &ServiceConfig{}
	adminHandler, err := NewAdminHandler(cfg)
	if err != nil {
		t.Fatalf("Failed to create admin handler: %v", err)
	}

	var buf bytes.Buffer
	err = adminHandler.templates["apps_list.html"].ExecuteTemplate(&buf, "base.html", nil)
	if err != nil {
		t.Fatalf("Failed to execute base template: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `href="/admin/apps"`) {
		t.Errorf("Expected template output to contain navigation link 'href=\"/admin/apps\"', got %s", output)
	}
}

func newHertzTestContext(path string) *app.RequestContext {
	c := app.NewContext(0)
	c.Request.SetRequestURI(path)
	return c
}
