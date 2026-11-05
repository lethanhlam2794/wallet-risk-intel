package resilience

import (
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// backoffFor computes the jittered exponential wait before attempt n
// (n >= 1): base<<n-1 with ±25% jitter — same posture as the OFAC
// downloader.
func backoffFor(attempt int, base time.Duration) time.Duration {
	b := base << (attempt - 1)
	if b <= 2 {
		return b
	}
	j := time.Duration(rand.Int63n(int64(b) / 2))
	return b - b/4 + j
}

// parseRetryAfter reads a Retry-After header as delta-seconds or HTTP-date.
// Callers bound the result before use.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
