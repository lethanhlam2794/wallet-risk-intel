package middleware

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"checkwallet/internal/platform/metrics"
)

// AccessLog emits one structured line per request with the request id, and
// feeds the http_* Prometheus collectors. It never logs headers, so API
// keys cannot leak through this path (spec §12/§16).
func AccessLog(log *slog.Logger, m *metrics.Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched" // NoRoute/NoMethod — bounded cardinality
		}
		status := c.Writer.Status()
		d := time.Since(start)
		if m != nil {
			m.ObserveRequest(route, status, d)
		}
		log.Info("http request",
			"request_id", GetRequestID(c),
			"method", c.Request.Method,
			"route", route,
			"status", strconv.Itoa(status),
			"duration_ms", d.Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}
