package middleware

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"checkwallet/internal/platform/cache"
)

// incrWithTTL atomically increments the fixed-window counter and applies a
// TTL exactly once — never leaves a TTL-less phantom counter behind.
var incrWithTTL = redis.NewScript(`
local c = redis.call('INCR', KEYS[1])
if redis.call('PTTL', KEYS[1]) < 0 then
	redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return c
`)

// RateLimiter is a Redis fixed-window limiter with separate buckets for
// authenticated clients and anonymous traffic (401-probing is bucketed too).
//
// Availability decision (documented): the limiter fails OPEN on Redis
// errors — sanctions-read availability is the product; the onStoreError hook
// records the failure metric and /readyz reports Redis health separately.
type RateLimiter struct {
	rdb          *redis.Client
	limitClient  int64
	limitAnon    int64
	window       time.Duration
	onStoreError func()
}

// NewRateLimiter builds a limiter; onStoreError is invoked on store failure.
func NewRateLimiter(rdb *redis.Client, limitClient, limitAnon int64, window time.Duration, onStoreError func()) *RateLimiter {
	if onStoreError == nil {
		onStoreError = func() {}
	}
	return &RateLimiter{rdb: rdb, limitClient: limitClient, limitAnon: limitAnon, window: window, onStoreError: onStoreError}
}

// Middleware enforces the limit; anonymous requests are bucketed by IP.
func (rl *RateLimiter) Middleware() gin.HandlerFunc {
	windowMS := int64(rl.window / time.Millisecond)
	if windowMS <= 0 {
		windowMS = 1
	}
	return func(c *gin.Context) {
		window := time.Now().UnixMilli() / windowMS
		var key string
		var limit int64
		if client := ClientFrom(c); client != nil {
			key = cache.RateLimitClient(fmt.Sprintf("%d", client.ID), window)
			limit = rl.limitClient
		} else {
			key = cache.RateLimitAnon(c.ClientIP(), window)
			limit = rl.limitAnon
		}
		count, err := incrWithTTL.Run(c.Request.Context(), rl.rdb, []string{key}, windowMS).Int64()
		if err != nil {
			rl.onStoreError()
			c.Next()
			return
		}
		if count > limit {
			c.Header("Retry-After", fmt.Sprintf("%d", int64(rl.window/time.Second)))
			RespondError(c, http.StatusTooManyRequests, CodeRateLimited, "rate limit exceeded")
			return
		}
		c.Next()
	}
}
