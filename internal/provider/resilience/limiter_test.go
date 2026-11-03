package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"checkwallet/internal/provider"
)

func TestLimiter_UnlimitedPasses(t *testing.T) {
	l := newLimiter(rate.Inf, 0, time.Millisecond)
	require.NoError(t, l.Wait(context.Background()))
}

func TestLimiter_BurstThenThrottle(t *testing.T) {
	l := newLimiter(rate.Every(50*time.Millisecond), 1, time.Second)
	require.NoError(t, l.Wait(context.Background())) // burst token
	require.NoError(t, l.Wait(context.Background())) // waits ~50ms
}

func TestLimiter_ExceedsMaxWaitFailsFast(t *testing.T) {
	l := newLimiter(rate.Every(time.Hour), 1, 10*time.Millisecond)
	require.NoError(t, l.Wait(context.Background()))
	var le *provider.LookupError
	err := l.Wait(context.Background())
	require.True(t, errors.As(err, &le))
	assert.Equal(t, provider.ErrRateLimited, le.Kind)
	assert.Greater(t, le.RetryAfter, time.Duration(0))
}

func TestLimiter_ContextCancelReleasesReservation(t *testing.T) {
	l := newLimiter(rate.Every(300*time.Millisecond), 1, 5*time.Second)
	require.NoError(t, l.Wait(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	var le *provider.LookupError
	err := l.Wait(ctx)
	require.True(t, errors.As(err, &le))
	assert.Equal(t, provider.ErrTimeout, le.Kind)

	// The cancelled reservation must not consume the next token — a fresh
	// wait can proceed once a refill arrives (~300ms << 5s max wait).
	require.NoError(t, l.Wait(context.Background()))
}
