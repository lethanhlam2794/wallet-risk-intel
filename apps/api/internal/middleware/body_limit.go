package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxBodyBytes caps request bodies (spec §16 request size limits). Requests
// that exceed the cap fail fast with 413 before the handler parses.
func MaxBodyBytes(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		c.Next()
	}
}
