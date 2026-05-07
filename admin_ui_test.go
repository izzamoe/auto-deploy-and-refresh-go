package main

import (
	"html/template"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestIsAdminUIRequestHertz(t *testing.T) {
	c := app.NewContext(0)
	if isAdminUIRequestHertz(c) {
		t.Fatal("expected false without header")
	}
	c.Request.Header.Set(adminUIRequestHeader, "true")
	if !isAdminUIRequestHertz(c) {
		t.Fatal("expected true with header")
	}
}

func TestRenderAdminTemplateHertz_NormalGET(t *testing.T) {
	tmpl := template.Must(template.New("base.html").Parse(`{{define "base.html"}}BASE{{end}}`))
	c := app.NewContext(0)
	c.Request.SetMethod(http.MethodGet)
	if err := renderAdminTemplateHertz(c, tmpl, nil); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(c.Response.Body()), "BASE") {
		t.Fatalf("expected base.html output, got %q", string(c.Response.Body()))
	}
}

func TestRenderAdminTemplateHertz_AdminUIFragment(t *testing.T) {
	tmpl := template.Must(template.New("base.html").Parse(`
{{define "fragment"}}FRAGMENT{{end}}
{{define "flash"}}FLASH{{end}}
{{define "content"}}CONTENT{{end}}
`))
	c := app.NewContext(0)
	c.Request.SetMethod(http.MethodGet)
	c.Request.Header.Set(adminUIRequestHeader, "true")
	if err := renderAdminTemplateHertz(c, tmpl, nil); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(c.Response.Body()), "FRAGMENT") {
		t.Fatalf("expected fragment output, got %q", string(c.Response.Body()))
	}
}

func TestRenderAdminTemplateHertz_AdminUIPOST(t *testing.T) {
	tmpl := template.Must(template.New("base.html").Parse(`
{{define "fragment"}}FRAGMENT{{end}}
{{define "flash"}}FLASH{{end}}
{{define "content"}}CONTENT{{end}}
`))
	c := app.NewContext(0)
	c.Request.SetMethod(http.MethodPost)
	c.Request.Header.Set(adminUIRequestHeader, "true")
	if err := renderAdminTemplateHertz(c, tmpl, nil); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := string(c.Response.Body())
	if !strings.Contains(out, "FLASH") || !strings.Contains(out, "CONTENT") {
		t.Fatalf("expected flash+content output, got %q", out)
	}
}

func TestAdminUINavigateHertz_NormalRedirect(t *testing.T) {
	c := app.NewContext(0)
	adminUINavigateHertz(c, "/admin/apps")
	if c.Response.StatusCode() != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", c.Response.StatusCode())
	}
	loc := string(c.Response.Header.Peek("Location"))
	if loc != "/admin/apps" {
		t.Fatalf("expected Location /admin/apps, got %q", loc)
	}
}

func TestAdminUINavigateHertz_AdminUI(t *testing.T) {
	c := app.NewContext(0)
	c.Request.Header.Set(adminUIRequestHeader, "true")
	adminUINavigateHertz(c, "/admin/apps")
	if c.Response.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d", c.Response.StatusCode())
	}
	loc := string(c.Response.Header.Peek(adminUILocationHeader))
	if loc != "/admin/apps" {
		t.Fatalf("expected X-Admin-Location /admin/apps, got %q", loc)
	}
}
