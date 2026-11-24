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
	if got := string(c.Response.Header.Peek("WWW-Authenticate")); got != "" {
		t.Errorf("K1: a request without Authorization must not get a WWW-Authenticate challenge, got %q", got)
	}
}

// A script client that talks Basic still gets the standard challenge, but the admin
// password is never a valid Basic credential (only API tokens are, FR-014).
func TestHertzAdminAuthRejectsAdminPasswordViaBasic(t *testing.T) {
	t.Parallel()
	middleware := testHertzSessionAuth(t)
	c := newHertzTestContext(http.MethodGet, "/admin/api/apps", nil, nil)
	c.Request.SetBasicAuth("admin", "secret")

	middleware(context.Background(), c)

	if c.Response.StatusCode() != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for the admin password via Basic, got %d", c.Response.StatusCode())
	}
	if got := string(c.Response.Header.Peek("WWW-Authenticate")); got != `Basic realm="auto-deploy api"` {
		t.Errorf("Expected the Basic challenge for a Basic client, got %q", got)
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
