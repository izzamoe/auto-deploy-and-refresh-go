package admin

import (
	"context"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

const (
	apiV1Prefix     = "/admin/api/v1"
	apiLegacyPrefix = "/admin/api"
)

// noLegacyAlias lists endpoints that are new in v1: they never existed under the
// pre-v1 prefix, so there is nothing to keep working.
var noLegacyAlias = []string{"/session", "/tokens", "/stats", "/account/sessions"}

// RegisterLegacyAPIAliases keeps the pre-v1 paths (/admin/api/*) working for ONE
// release (Q-7): every /admin/api/v1/* route registered on h so far is also mounted
// under /admin/api/* with the same handler, behind the same auth middleware, plus
// the headers `Deprecation: true` and `Link: <v1 path>; rel="successor-version"`.
// Call it after every v1 route has been registered. The aliases are removed in the
// next release. The admin password is not accepted as a Basic credential there
// either: the middleware is the same.
func RegisterLegacyAPIAliases(h *server.Hertz, authMW app.HandlerFunc) {
	for _, ri := range h.Routes() {
		if !strings.HasPrefix(ri.Path, apiV1Prefix+"/") || strings.Contains(ri.Path, "*") || ri.HandlerFunc == nil {
			continue
		}
		rest := strings.TrimPrefix(ri.Path, apiV1Prefix)
		if isNewInV1(rest) {
			continue
		}
		h.Handle(ri.Method, apiLegacyPrefix+rest, authMW, deprecatedAlias, ri.HandlerFunc)
	}
}

func isNewInV1(rest string) bool {
	for _, p := range noLegacyAlias {
		if rest == p || strings.HasPrefix(rest, p+"/") {
			return true
		}
	}
	return false
}

// deprecatedAlias announces that the pre-v1 path is going away and where the
// canonical path is (RFC 9745 Deprecation, RFC 8288 Link).
func deprecatedAlias(ctx context.Context, c *app.RequestContext) {
	c.Header("Deprecation", "true")
	path := string(c.Request.URI().Path())
	if strings.HasPrefix(path, apiLegacyPrefix+"/") && !strings.HasPrefix(path, apiV1Prefix+"/") {
		c.Header("Link", "<"+apiV1Prefix+strings.TrimPrefix(path, apiLegacyPrefix)+`>; rel="successor-version"`)
	}
	c.Next(ctx)
}
