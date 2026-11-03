package resilience

import (
	"context"
	"time"

	"golang.org/x/time/rate"

	"checkwallet/internal/provider"
)

// limiter bounds the wait on a token bucket: reservation delays beyond
// maxWait fail fast as typed rate_limited instead of queueing (F2), and a
// cancelled wait returns its reservation so the token is not leaked.
type limiter struct {
	l       *rate.Limiter
	maxWait time.Duration
}

func newLimiter(lim rate.Limit, burst int, maxWait time.Duration) *limiter {
	return &limiter{l: rate.NewLimiter(lim, burst), maxWait: maxWait}
}

// Wait consumes one token per call (one per HTTP attempt).
func (l *limiter) Wait(ctx context.Context) error {
	r := l.l.Reserve()
	if !r.OK() {
		return provider.Errorf(provider.ErrRateLimited, "limiter reservation refused")
	}
	delay := r.Delay()
	if delay > l.maxWait {
		r.Cancel()
		return &provider.LookupError{
			Kind:       provider.ErrRateLimited,
			RetryAfter: delay,
			Msg:        "local rate limiter saturated",
		}
	}
	if delay <= 0 {
		return nil
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		r.Cancel() // return the token — a cancelled wait must not burn quota
		return provider.Errorf(provider.ErrTimeout, "limiter wait: %v", ctx.Err())
	case <-t.C:
		return nil
	}
}
