// Package middleware provides request-id, auth, rate limiting, and the
// stable error envelope shared by every endpoint.
package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// CtxRequestID is the gin context key for the request id.
const CtxRequestID = "request_id"

// HeaderRequestID is the response header carrying the request id.
const HeaderRequestID = "X-Request-ID"

// RequestID assigns a request id (honoring an inbound X-Request-ID) and
// exposes it on the response and in the gin context for log enrichment.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(HeaderRequestID)
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(CtxRequestID, id)
		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

// GetRequestID extracts the request id from context ("" when absent).
func GetRequestID(c *gin.Context) string {
	if v, ok := c.Get(CtxRequestID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
