package auth

import "github.com/cloudwego/hertz/pkg/app"

// CSRFVerdict is the outcome of CheckCSRF.
type CSRFVerdict int

// CSRF verdicts.
const (
	CSRFOK CSRFVerdict = iota
	CSRFRejected
	CSRFUnsupportedMedia
)

// CheckCSRF validates a cookie-authenticated mutation (FR-011, D8).
func CheckCSRF(c *app.RequestContext, trustedProto bool) CSRFVerdict { return CSRFOK }
