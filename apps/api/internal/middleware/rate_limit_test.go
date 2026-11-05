package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/testutil"

	"github.com/redis/go-redis/v9"
)

// newBrokenRedis returns a client pointed at a closed port so every command
// errors — used to prove the fail-open path on a real client, not a mock.
func newBrokenRedis(t *testing.T) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: 0})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func rateLimitRouter(rl *RateLimiter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID(), rl.Middleware())
	r.GET("/x", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return r
}

// Real Redis instance: string-type INCR+EXPIRE contract must be proven on a
// live server (P-008 — mocks cannot surface WRONGTYPE/atomicity bugs).
func TestRateLimit_ExceedsLimit(t *testing.T) {
	rdb := testutil.RequireTestRedis(t)
	var storeErrs int
	rl := NewRateLimiter(rdb, 5, 3, time.Minute, func() { storeErrs++ })
	r := rateLimitRouter(rl)

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "203.0.113.9:9999"
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "request %d", i+1)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "203.0.113.9:9999"
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Contains(t, w.Body.String(), "RATE_LIMITED")
	assert.NotEmpty(t, w.Header().Get("Retry-After"))
	assert.Equal(t, 0, storeErrs)
}

func TestRateLimit_AnonBucketIsPerIP(t *testing.T) {
	rdb := testutil.RequireTestRedis(t)
	rl := NewRateLimiter(rdb, 1, 1, time.Minute, func() {})
	r := rateLimitRouter(rl)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "198.51.100.1:1"
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "198.51.100.2:1" // different IP → own bucket
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRateLimit_FailsOpenOnStoreError(t *testing.T) {
	rdb := testutil.RequireTestRedis(t)
	var storeErrs int
	rl := NewRateLimiter(rdb, 1, 1, time.Minute, func() { storeErrs++ })
	// Poison the limiter: swap the client for a closed one.
	broken := *rl
	broken.rdb = newBrokenRedis(t)
	r := rateLimitRouter(&broken)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "192.0.2.1:1"
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "rate limiter must fail open (availability decision)")
	assert.Equal(t, 1, storeErrs)
}
