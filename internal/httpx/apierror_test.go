package httpx

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func TestWriteError_ContractShape(t *testing.T) {
	h := server.New()
	h.Use(RequestID())
	h.GET("/plain", func(ctx context.Context, c *app.RequestContext) {
		WriteError(c, 404, CodeNotFound, "app not found")
	})
	h.GET("/fields", func(ctx context.Context, c *app.RequestContext) {
		WriteError(c, 400, CodeValidationFailed, "validation failed",
			FieldError{Field: "binaryPath", Code: "invalid", Message: "must be absolute"})
	})

	r := ut.PerformRequest(h.Engine, "GET", "/plain", nil).Result()
	if r.StatusCode() != 404 {
		t.Fatalf("status = %d, want 404", r.StatusCode())
	}
	if ct := string(r.Header.Peek("Content-Type")); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var m map[string]any
	if err := json.Unmarshal(r.Body(), &m); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, r.Body())
	}
	if m["status"] != "error" || m["code"] != "not_found" || m["error"] != "app not found" {
		t.Fatalf("unexpected body: %s", r.Body())
	}
	errs, ok := m["errors"].([]any)
	if !ok || len(errs) != 0 {
		t.Fatalf("errors must be an empty array, got %#v", m["errors"])
	}
	if rid, _ := m["requestId"].(string); rid == "" || rid != string(r.Header.Peek(HeaderRequestID)) {
		t.Fatalf("requestId %q does not match header %q", m["requestId"], r.Header.Peek(HeaderRequestID))
	}

	r = ut.PerformRequest(h.Engine, "GET", "/fields", nil).Result()
	var e APIError
	if err := json.Unmarshal(r.Body(), &e); err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 400 || e.Code != CodeValidationFailed || len(e.Errors) != 1 ||
		e.Errors[0].Field != "binaryPath" || e.Errors[0].Code != "invalid" || e.Errors[0].Message != "must be absolute" {
		t.Fatalf("unexpected body (status %d): %s", r.StatusCode(), r.Body())
	}
}

func TestWriteError_WithoutRequestIDMiddleware(t *testing.T) {
	h := server.New()
	h.GET("/x", func(ctx context.Context, c *app.RequestContext) {
		WriteError(c, 500, CodeInternalError, "internal error")
	})
	r := ut.PerformRequest(h.Engine, "GET", "/x", nil).Result()
	var m map[string]any
	if err := json.Unmarshal(r.Body(), &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["requestId"]; has {
		t.Fatalf("requestId must be omitted when no id is known: %s", r.Body())
	}
	if m["code"] != "internal_error" {
		t.Fatalf("unexpected body: %s", r.Body())
	}
}
