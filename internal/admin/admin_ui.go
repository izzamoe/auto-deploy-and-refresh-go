package admin

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

const adminUIRequestHeader = "X-Admin-Request"
const adminUILocationHeader = "X-Admin-Location"

func isAdminUIRequestHertz(c *app.RequestContext) bool {
	return string(c.GetHeader(adminUIRequestHeader)) == "true"
}

func renderAdminTemplateHertz(c *app.RequestContext, tmpl *template.Template, data any) error {
	var buf bytes.Buffer
	if isAdminUIRequestHertz(c) {
		if string(c.Request.Method()) == http.MethodGet {
			if err := tmpl.ExecuteTemplate(&buf, "fragment", data); err != nil {
				return err
			}
		} else {
			if err := tmpl.ExecuteTemplate(&buf, "flash", data); err != nil {
				return err
			}
			if err := tmpl.ExecuteTemplate(&buf, "content", data); err != nil {
				return err
			}
		}
	} else {
		if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
			return err
		}
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", buf.Bytes())
	return nil
}

func adminUINavigateHertz(c *app.RequestContext, location string) {
	if isAdminUIRequestHertz(c) {
		c.Header(adminUILocationHeader, location)
		c.SetStatusCode(http.StatusOK)
		return
	}
	c.Redirect(http.StatusSeeOther, []byte(location))
}
