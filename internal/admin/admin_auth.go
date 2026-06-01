package admin

import (
	"embed"
	"html/template"
	"strings"

	"github.com/izzamoe/auto-deploy/internal/config"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

//go:embed templates/*.html
var templateFS embed.FS

type AdminHandler struct {
	cfg       *config.ServiceConfig
	templates map[string]*template.Template
}

func NewAdminHandler(cfg *config.ServiceConfig) (*AdminHandler, error) {
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

func hertzRequireAuth(c *app.RequestContext) {
	c.Header("WWW-Authenticate", `Basic realm="auto-deploy admin"`)
	path := string(c.Request.URI().Path())
	if path == "/admin/api" || strings.HasPrefix(path, "/admin/api/") {
		c.JSON(consts.StatusUnauthorized, adminAPIErrorResponse{Status: "error", Error: "unauthorized"})
		return
	}
	c.String(consts.StatusUnauthorized, "Unauthorized")
}

func (h *AdminHandler) Templates() map[string]*template.Template { return h.templates }
