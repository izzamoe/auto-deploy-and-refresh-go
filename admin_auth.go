package main

import (
	"crypto/subtle"
	"embed"
	"html/template"
	"net/http"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

type AdminHandler struct {
	cfg       *ServiceConfig
	templates map[string]*template.Template
}

func NewAdminHandler(cfg *ServiceConfig) (*AdminHandler, error) {
	tmpls := make(map[string]*template.Template)
	pages := []string{"apps_list.html", "app_form.html", "history.html"}

	for _, page := range pages {
		tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/"+page)
		if err != nil {
			return nil, err
		}
		tmpls[page] = tmpl
	}

	return &AdminHandler{
		cfg:       cfg,
		templates: tmpls,
	}, nil
}

func BasicAuthMiddleware(expectedUsername, expectedPassword string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			username, password, ok := r.BasicAuth()

			if !ok {
				requireAuth(w, r)
				return
			}

			// Constant-time comparison to prevent timing attacks.
			usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(expectedUsername)) == 1
			passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(expectedPassword)) == 1

			if !usernameMatch || !passwordMatch {
				requireAuth(w, r)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func requireAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Basic realm="auto-deploy admin"`)
	if r.URL.Path == "/admin/api" || strings.HasPrefix(r.URL.Path, "/admin/api/") {
		writeJSON(w, http.StatusUnauthorized, response{Status: "error", Error: "unauthorized"})
		return
	}
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}
