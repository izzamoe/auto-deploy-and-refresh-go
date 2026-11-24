package httpx

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

var generatedID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func newRequestIDServer() *server.Hertz {
	h := server.New()
	h.Use(RequestID())
	h.GET("/id", func(ctx context.Context, c *app.RequestContext) {
		c.String(200, FromContext(c))
	})
	return h
}

func get(h *server.Hertz, incoming *string) (headerID, bodyID string) {
	hdrs := []ut.Header{}
	if incoming != nil {
		hdrs = append(hdrs, ut.Header{Key: HeaderRequestID, Value: *incoming})
	}
	r := ut.PerformRequest(h.Engine, "GET", "/id", nil, hdrs...).Result()
	return string(r.Header.Peek(HeaderRequestID)), string(r.Body())
}

func TestRequestID_GeneratesAndPropagates(t *testing.T) {
	h := newRequestIDServer()

	// No incoming header: a 16-byte hex id is generated, echoed in the response
	// and visible to handlers through FromContext.
	hdr1, body1 := get(h, nil)
	if !generatedID.MatchString(hdr1) {
		t.Fatalf("generated id %q is not 32 hex chars", hdr1)
	}
	if body1 != hdr1 {
		t.Fatalf("handler saw id %q, response header has %q", body1, hdr1)
	}
	hdr2, _ := get(h, nil)
	if hdr1 == hdr2 {
		t.Fatalf("two requests got the same generated id %q", hdr1)
	}

	// A valid incoming id is propagated unchanged (boundaries: 1 and 64 chars).
	for _, in := range []string{"abc-123.X_y", "a", strings.Repeat("A", 64)} {
		in := in
		hdr, body := get(h, &in)
		if hdr != in || body != in {
			t.Fatalf("valid incoming %q: header=%q handler=%q", in, hdr, body)
		}
	}
}

func TestRequestID_RejectsInvalidIncoming(t *testing.T) {
	h := newRequestIDServer()
	for _, in := range []string{
		"bad id!",
		"a/b",
		"semi;colon",
		"quote\"",
		"<script>",
		"unicode-é",
		strings.Repeat("A", 65),
	} {
		in := in
		hdr, body := get(h, &in)
		if hdr == in {
			t.Errorf("invalid incoming %q was echoed back", in)
		}
		if !generatedID.MatchString(hdr) {
			t.Errorf("invalid incoming %q: replacement %q is not 32 hex chars", in, hdr)
		}
		if body != hdr {
			t.Errorf("invalid incoming %q: handler saw %q, header has %q", in, body, hdr)
		}
	}
}
