package admin

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

// SecurityHeadersConfig selects the optional headers.
type SecurityHeadersConfig struct {
	// HSTS adds Strict-Transport-Security; set it when the session cookie is Secure,
	// i.e. when the admin is served over HTTPS (directly or behind a TLS proxy).
	HSTS bool
}

// SecurityHeaders sets the headers of FR-054 on every response, including errors and
// unknown routes: CSP, X-Content-Type-Options, Referrer-Policy, X-Frame-Options and,
// optionally, HSTS. They are set before the handlers run, so a handler (the SPA shell)
// may replace the CSP with one that carries script hashes.
func SecurityHeaders(cfg SecurityHeadersConfig) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Response.Header.Set("Content-Security-Policy", defaultCSP)
		c.Response.Header.Set("X-Content-Type-Options", "nosniff")
		c.Response.Header.Set("X-Frame-Options", "DENY")
		c.Response.Header.Set("Referrer-Policy", "same-origin")
		if cfg.HSTS {
			c.Response.Header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next(ctx)
	}
}
