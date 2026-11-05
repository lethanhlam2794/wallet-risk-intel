package resilience

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/provider"
)

func TestBreaker_ClosedAllowsAndResets(t *testing.T) {
	b := newBreaker(3, time.Minute, time.Now)
	for i := 0; i < 2; i++ {
		require.NoError(t, b.Allow())
		b.Report(false)
	}
	// Mixed success resets the consecutive counter.
	require.NoError(t, b.Allow())
	b.Report(true)
	for i := 0; i < 3; i++ {
		require.NoError(t, b.Allow())
		b.Report(false)
	}
	var le *provider.LookupError
	require.True(t, errors.As(b.Allow(), &le))
	assert.Equal(t, provider.ErrCircuitOpen, le.Kind)
}

func TestBreaker_HalfOpenSingleProbe(t *testing.T) {
	now := time.Now()
	b := newBreaker(1, time.Minute, func() time.Time { return now })
	b.Report(false) // opens
	require.Error(t, b.Allow())

	now = now.Add(2 * time.Minute)
	require.NoError(t, b.Allow()) // half-open probe admitted

	// A second concurrent probe while the first is in flight is rejected.
	var le *provider.LookupError
	require.True(t, errors.As(b.Allow(), &le))
	assert.Equal(t, provider.ErrCircuitOpen, le.Kind)

	// Probe failure re-opens for another window.
	b.Report(false)
	require.True(t, errors.As(b.Allow(), &le))
	assert.Equal(t, provider.ErrCircuitOpen, le.Kind)

	// Next window: probe again, success → closed.
	now = now.Add(2 * time.Minute)
	require.NoError(t, b.Allow())
	b.Report(true)
	require.NoError(t, b.Allow()) // closed: normal flow resumes
}

func TestBreaker_OnlyConsecutiveFailuresCount(t *testing.T) {
	b := newBreaker(2, time.Minute, time.Now)
	b.Report(false)
	b.Report(true)  // success between failures resets
	b.Report(false) // count = 1, below threshold
	require.NoError(t, b.Allow())
}

func TestBreaker_ConcurrentAccess(t *testing.T) {
	b := newBreaker(5, time.Minute, time.Now)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := b.Allow(); err == nil {
				b.Report(i%2 == 0)
			}
		}(i)
	}
	wg.Wait()
}
