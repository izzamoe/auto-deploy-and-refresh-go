package main

import (
	"html/template"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

const adminUIRequestHeader = "X-Admin-Request"
const adminUILocationHeader = "X-Admin-Location"

// adminHTMLRouteContract is the canonical admin HTML response contract used by
// all server-rendered admin handlers:
//   - normal GET: return the full HTML document via base.html
//   - AdminUI GET: return only the page fragment defined by the template's content block
//   - AdminUI POST success that navigates: return 200 with X-Admin-Location instead of raw 3xx
//   - non-AdminUI POST success: keep standard 303 redirects
//   - unauthorized admin HTML and WebSocket upgrade requests: return 401 with WWW-Authenticate
func adminHTMLRouteContract() string {
	return "full GET document, AdminUI GET fragment, AdminUI POST X-Admin-Location, non-AdminUI POST 303, unauthorized 401 + WWW-Authenticate"
}

func isAdminUIRequest(r *http.Request) bool {
	return r.Header.Get(adminUIRequestHeader) == "true"
}

func renderAdminTemplate(w http.ResponseWriter, r *http.Request, tmpl *template.Template, data any) error {
	if isAdminUIRequest(r) {
		if r.Method == http.MethodGet {
			return tmpl.ExecuteTemplate(w, "fragment", data)
		}
		if err := tmpl.ExecuteTemplate(w, "flash", data); err != nil {
			return err
		}
		return tmpl.ExecuteTemplate(w, "content", data)
	}
	return tmpl.ExecuteTemplate(w, "base.html", data)
}

func adminUINavigate(w http.ResponseWriter, r *http.Request, location string) {
	if isAdminUIRequest(r) {
		w.Header().Set(adminUILocationHeader, location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func isAdminUIRequestHertz(c *app.RequestContext) bool {
	return string(c.GetHeader(adminUIRequestHeader)) == "true"
}

func renderAdminTemplateHertz(c *app.RequestContext, tmpl *template.Template, data any) error {
	if isAdminUIRequestHertz(c) {
		if string(c.Request.Method()) == http.MethodGet {
			return tmpl.ExecuteTemplate(c.Response.BodyWriter(), "fragment", data)
		}
		if err := tmpl.ExecuteTemplate(c.Response.BodyWriter(), "flash", data); err != nil {
			return err
		}
		return tmpl.ExecuteTemplate(c.Response.BodyWriter(), "content", data)
	}
	return tmpl.ExecuteTemplate(c.Response.BodyWriter(), "base.html", data)
}

func adminUINavigateHertz(c *app.RequestContext, location string) {
	if isAdminUIRequestHertz(c) {
		c.Header(adminUILocationHeader, location)
		c.SetStatusCode(http.StatusOK)
		return
	}
	c.Redirect(http.StatusSeeOther, []byte(location))
}
