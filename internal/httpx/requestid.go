// Package httpx holds small HTTP helpers shared by internal/admin and internal/auth:
// request IDs and the API error envelope. It must not import internal/admin.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"

	"github.com/cloudwego/hertz/pkg/app"
)

// HeaderRequestID is the request/response header carrying the request ID.
const HeaderRequestID = "X-Request-ID"

// ContextKey is the key under which the request ID is stored in app.RequestContext.
const ContextKey = "request_id"

var validIncomingID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// NewRequestID returns 16 random bytes as 32 lowercase hex characters.
func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; never block a request on it.
		panic("httpx: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// RequestID accepts an incoming X-Request-ID only when it matches
// ^[A-Za-z0-9._-]{1,64}$ and otherwise generates a fresh one. The ID is set on
// the response header and stored on the request context for handlers and logs.
func RequestID() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		id := string(c.GetHeader(HeaderRequestID))
		if !validIncomingID.MatchString(id) {
			id = NewRequestID()
		}
		c.Set(ContextKey, id)
		c.Header(HeaderRequestID, id)
		c.Next(ctx)
	}
}

// FromContext returns the request ID stored by RequestID, or "" when absent.
func FromContext(c *app.RequestContext) string {
	if v, ok := c.Get(ContextKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
