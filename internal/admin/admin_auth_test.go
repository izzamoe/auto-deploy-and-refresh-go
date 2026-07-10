package admin

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/config"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestHertzAdminAuthRequiresCredentials(t *testing.T) {
	t.Parallel()
	middleware := testHertzSessionAuth(t)
	c := newHertzTestContext(http.MethodGet, "/admin/api/apps", nil, nil)

	middleware(context.Background(), c)

	if c.Response.StatusCode() != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized, got %d", c.Response.StatusCode())
	}
	if got := string(c.Response.Header.Peek("WWW-Authenticate")); got != `Basic realm="auto-deploy admin"` {
		t.Errorf("Expected WWW-Authenticate header %q, got %q", `Basic realm="auto-deploy admin"`, got)
	}
}

func TestHertzAdminAuthRejectsWrongPassword(t *testing.T) {
	t.Parallel()
	middleware := testHertzSessionAuth(t)
	c := newHertzTestContext(http.MethodGet, "/admin/api/apps", nil, nil)
	c.Request.SetBasicAuth("admin", "wrong")

	middleware(context.Background(), c)

	if c.Response.StatusCode() != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for wrong password, got %d", c.Response.StatusCode())
	}
}

func TestHertzAdminAuthAcceptsCorrectCredentials(t *testing.T) {
	t.Parallel()
	middleware := testHertzSessionAuth(t)
	c := newHertzTestContext(http.MethodGet, "/admin/apps", nil, nil)
	setHertzSessionCookie(t, c)
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
	t.Parallel()
	cfg := &config.ServiceConfig{}
	adminHandler, err := NewAdminHandler(cfg)
	if err != nil {
		t.Fatalf("Failed to create admin handler: %v", err)
	}

	var buf bytes.Buffer
	data := map[string]any{}
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
	t.Parallel()
	cfg := &config.ServiceConfig{}
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

func newHertzTestContext(method, path string, body []byte, headers map[string]string) *app.RequestContext {
	c := app.NewContext(0)
	c.Request.SetRequestURI(path)
	c.Request.SetMethod(method)
	if body != nil {
		c.Request.SetBody(body)
	}
	for k, v := range headers {
		c.Request.Header.Set(k, v)
	}
	return c
}
