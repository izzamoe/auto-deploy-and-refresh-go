package main

import (
	"html/template"
	"net/http"
)

const htmxRequestHeader = "HX-Request"

// adminHTMLRouteContract is the canonical admin HTML response contract used by
// all server-rendered admin handlers:
//   - normal GET: return the full HTML document via base.html
//   - HTMX GET: return only the page fragment defined by the template's content block
//   - HTMX POST success that navigates: return 200 with HX-Location instead of raw 3xx
//   - non-HTMX POST success: keep standard 303 redirects
//   - unauthorized HTML, HTMX, and SSE: return 401 with WWW-Authenticate
func adminHTMLRouteContract() string {
	return "full GET document, HTMX GET fragment, HTMX POST HX-Location, non-HTMX POST 303, unauthorized 401 + WWW-Authenticate"
}

func isHTMXRequest(r *http.Request) bool {
	return r.Header.Get(htmxRequestHeader) == "true"
}

func renderAdminTemplate(w http.ResponseWriter, r *http.Request, tmpl *template.Template, data any) error {
	if isHTMXRequest(r) {
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

func adminNavigate(w http.ResponseWriter, r *http.Request, location string) {
	if isHTMXRequest(r) {
		w.Header().Set("HX-Location", location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}
