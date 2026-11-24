package httpx

import (
	"github.com/cloudwego/hertz/pkg/app"
)

// Error codes (admin-api.v1 ErrorCode enum).
const (
	CodeSessionExpired         = "session_expired"
	CodeSessionRevoked         = "session_revoked"
	CodeUnauthorized           = "unauthorized"
	CodePasswordChangeNeeded   = "password_change_required"
	CodeCSRFRejected           = "csrf_rejected"
	CodeRateLimited            = "rate_limited"
	CodeValidationFailed       = "validation_failed"
	CodeInvalidTag             = "invalid_tag"
	CodeInvalidJSON            = "invalid_json"
	CodeNotFound               = "not_found"
	CodeDuplicate              = "duplicate"
	CodeQueueFull              = "queue_full"
	CodeUpstreamError          = "upstream_error"
	CodeArtifactDigestMismatch = "artifact_digest_mismatch"
	CodeArtifactArchMismatch   = "artifact_arch_mismatch"
	CodeInternalError          = "internal_error"
	CodeForbidden              = "forbidden"
	CodeUnsupportedMediaType   = "unsupported_media_type"
	CodePayloadTooLarge        = "payload_too_large"
	CodeMethodNotAllowed       = "method_not_allowed"
)

// FieldError describes one invalid field in a validation_failed response.
type FieldError struct {
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// APIError is the error envelope of the admin API (contract schema "Error").
// Error is a safe, human readable English message; never put err.Error() of an
// internal failure in it.
type APIError struct {
	Status    string       `json:"status"`
	Code      string       `json:"code"`
	Error     string       `json:"error"`
	Errors    []FieldError `json:"errors"`
	RequestID string       `json:"requestId,omitempty"`
}

// WriteError writes the error envelope with the given HTTP status. `errors` is
// always a JSON array (empty when there are no field errors).
func WriteError(c *app.RequestContext, httpStatus int, code, msg string, fields ...FieldError) {
	if fields == nil {
		fields = []FieldError{}
	}
	c.JSON(httpStatus, APIError{
		Status:    "error",
		Code:      code,
		Error:     msg,
		Errors:    fields,
		RequestID: FromContext(c),
	})
}
