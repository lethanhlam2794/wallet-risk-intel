package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Stable error codes (spec §9: stable error codes on every error).
const (
	CodeInvalidAddress = "INVALID_ADDRESS"
	CodeInvalidNetwork = "INVALID_NETWORK"
	CodeUnauthorized   = "UNAUTHORIZED"
	CodeForbidden      = "FORBIDDEN"
	CodeNotFound       = "NOT_FOUND"
	CodeRateLimited    = "RATE_LIMITED"
	CodeInternal       = "INTERNAL"
	CodeBadRequest     = "BAD_REQUEST"
	CodeConflict       = "INGESTION_RUNNING"
	CodeInvalidCursor  = "INVALID_CURSOR"
	CodeInvalidSource  = "INVALID_SOURCE"

	// Screening idempotency conflicts (plan §3.3).
	CodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	CodeIdempotencyInFlight = "IDEMPOTENCY_IN_FLIGHT"
)

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// RespondError emits the stable error envelope with request id.
func RespondError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, errorEnvelope{Error: errorBody{
		Code: code, Message: message, RequestID: GetRequestID(c),
	}})
}

// InternalError is the safe generic failure: never leaks internals.
func InternalError(c *gin.Context) {
	RespondError(c, http.StatusInternalServerError, CodeInternal, "internal error")
}
