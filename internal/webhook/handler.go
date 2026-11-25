// Package webhook is the CI-facing POST /webhook endpoint (contracts/webhook.md). It
// was inline in cmd/server; it lives here so it can be tested without package main.
package webhook

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/izzamoe/auto-deploy/internal/admission"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/httpx"
)

// Failed-authentication limit of the webhook (contracts/webhook.md W3, Q-4, FR-055): 30
// failures per minute per client IP; a blocked IP gets 429 + Retry-After until its
// window clears. Requests with a correct secret are never counted.
const (
	maxFailedAuthPerMinute = 30
	failedAuthWindow       = time.Minute
)

// Limits configures the rate limiter of HandlerWithLimits; zero values pick the defaults.
type Limits struct {
	// Failures counts failed authentications per client IP.
	Failures *auth.Window
	// ClientIP resolves the client address (TRUSTED_PROXY_CIDRS aware).
	ClientIP *auth.ClientIPResolver
}

// Admitter is what the handler needs from the admission service.
type Admitter interface {
	Admit(bearerToken, tag string) admission.AdmissionResult
}

// response is the body of every webhook answer. `code` (W2) is additive: clients that
// predate it ignore it.
type response struct {
	Status string `json:"status"`
	Tag    string `json:"tag,omitempty"`
	Error  string `json:"error,omitempty"`
	Code   string `json:"code,omitempty"`
}

func fail(c *app.RequestContext, status int, code, msg string) {
	c.JSON(status, response{Status: "error", Error: msg, Code: code})
}

// Handler returns the POST /webhook handler. Success shapes and the status codes 401,
// 400, 503 are exactly those of the pre-existing contract; the changes are:
//   - W1: a 500 never contains the internal error, only "internal error" (the real error
//     is logged with the request ID);
//   - W2: every error carries `code`;
//   - W6: the request ID middleware (installed by the server) puts X-Request-ID on the
//     response.
func Handler(adm Admitter) app.HandlerFunc { return HandlerWithLimits(adm, Limits{}) }

// HandlerWithLimits is Handler with an explicit rate limiter / client IP resolver.
func HandlerWithLimits(adm Admitter, lim Limits) app.HandlerFunc {
	if lim.Failures == nil {
		lim.Failures = auth.NewWindow(maxFailedAuthPerMinute, failedAuthWindow, time.Now)
	}
	if lim.ClientIP == nil {
		lim.ClientIP = auth.NewClientIPResolver(nil)
	}
	return func(ctx context.Context, c *app.RequestContext) {
		ip := lim.ClientIP.FromContext(c)
		if blocked, retry := lim.Failures.Blocked(ip); blocked {
			secs := int(retry.Round(time.Second) / time.Second)
			if secs < 1 {
				secs = 1
			}
			c.Header("Retry-After", strconv.Itoa(secs))
			fail(c, consts.StatusTooManyRequests, httpx.CodeRateLimited, "rate limited")
			return
		}
		unauthorized := func() {
			lim.Failures.Add(ip)
			fail(c, consts.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		}
		authHeader := string(c.GetHeader("Authorization"))
		if !strings.HasPrefix(authHeader, "Bearer ") {
			unauthorized()
			return
		}
		var body struct {
			Tag string `json:"tag"`
		}
		if err := c.BindJSON(&body); err != nil || body.Tag == "" {
			fail(c, consts.StatusBadRequest, httpx.CodeValidationFailed, "missing or empty tag")
			return
		}
		result := adm.Admit(strings.TrimPrefix(authHeader, "Bearer "), body.Tag)
		switch result.Outcome {
		case admission.OutcomeUnauthorized:
			unauthorized()
		case admission.OutcomeBadRequest:
			if result.Tag == "" {
				fail(c, consts.StatusBadRequest, httpx.CodeValidationFailed, "missing or empty tag")
			} else {
				fail(c, consts.StatusBadRequest, httpx.CodeInvalidTag, "invalid tag")
			}
		case admission.OutcomeDuplicate:
			c.JSON(consts.StatusOK, response{Status: "duplicate", Tag: body.Tag})
		case admission.OutcomeQueued:
			c.JSON(consts.StatusAccepted, response{Status: "queued", Tag: body.Tag})
		case admission.OutcomeError:
			if errors.Is(result.Error, admission.ErrQueueFull) {
				fail(c, consts.StatusServiceUnavailable, httpx.CodeQueueFull, "queue full")
				return
			}
			slog.Error("webhook admission failed", "err", result.Error, "request_id", httpx.FromContext(c))
			fail(c, consts.StatusInternalServerError, httpx.CodeInternalError, "internal error")
		default:
			slog.Error("webhook: unexpected admission outcome", "outcome", result.Outcome, "request_id", httpx.FromContext(c))
			fail(c, consts.StatusInternalServerError, httpx.CodeInternalError, "internal error")
		}
	}
}
