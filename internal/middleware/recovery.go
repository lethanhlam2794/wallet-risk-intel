package middleware

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Recovery replaces gin.Recovery: a panicked request still answers the
// stable error envelope (with request_id) instead of a bare 500, and the
// panic is logged for diagnosis.
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("request panic", "panic", r,
					"request_id", GetRequestID(c),
					"method", c.Request.Method,
					"path", c.Request.URL.Path)
				RespondError(c, http.StatusInternalServerError, CodeInternal, "internal error")
			}
		}()
		c.Next()
	}
}
