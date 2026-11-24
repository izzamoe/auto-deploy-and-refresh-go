package auth

import (
	"mime"
	"net/url"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

// CSRFVerdict is the outcome of CheckCSRF.
type CSRFVerdict int

// CSRF verdicts.
const (
	CSRFOK CSRFVerdict = iota
	CSRFRejected
	CSRFUnsupportedMedia
)

// isMutation reports whether the method changes state.
func isMutation(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

// CheckCSRF validates a cookie-authenticated mutation (FR-011, D8):
//
//   - header X-Admin-Request: true is required (a cross-site form or simple
//     fetch cannot add it without a CORS preflight, which the server never grants);
//   - and Sec-Fetch-Site must be same-origin, or, when the browser sent no
//     Sec-Fetch-Site, Origin must equal the request host. Any other Sec-Fetch-Site
//     value (cross-site, same-site, none) is rejected even when Origin matches;
//   - a request with a body must be application/json (B16: no CORS-simple
//     text/plain or form bodies).
//
// trustedProto says X-Forwarded-Proto comes from a trusted proxy; only then is the
// Origin scheme compared. Otherwise the scheme is ignored, because behind a
// TLS-terminating proxy the server only ever sees plain http.
func CheckCSRF(c *app.RequestContext, trustedProto bool) CSRFVerdict {
	if string(c.GetHeader("X-Admin-Request")) != "true" {
		return CSRFRejected
	}
	switch site := strings.ToLower(string(c.GetHeader("Sec-Fetch-Site"))); site {
	case "same-origin":
	case "":
		if !originMatches(c, trustedProto) {
			return CSRFRejected
		}
	default:
		return CSRFRejected
	}
	if len(c.Request.Body()) > 0 && !isJSONContentType(string(c.GetHeader("Content-Type"))) {
		return CSRFUnsupportedMedia
	}
	return CSRFOK
}

func originMatches(c *app.RequestContext, trustedProto bool) bool {
	origin := string(c.GetHeader("Origin"))
	if origin == "" || origin == "null" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.Path != "" && u.Path != "/" {
		return false
	}
	if !strings.EqualFold(u.Host, string(c.Host())) {
		return false
	}
	if trustedProto {
		if proto := strings.ToLower(string(c.GetHeader("X-Forwarded-Proto"))); proto != "" && !strings.EqualFold(u.Scheme, proto) {
			return false
		}
	}
	return true
}

func isJSONContentType(v string) bool {
	mt, _, err := mime.ParseMediaType(v)
	return err == nil && mt == "application/json"
}
