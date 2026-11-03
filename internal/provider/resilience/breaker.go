package resilience

import (
	"sync"
	"time"

	"checkwallet/internal/provider"
)

// breaker is the consecutive-failure circuit breaker (spec §15):
// closed → open after `threshold` straight failures → half-open after
// `openFor`, admitting exactly one probe. Probe success closes; probe
// failure re-opens for another window. Success inside closed resets the
// counter; only consecutive failures count.
type breaker struct {
	mu        sync.Mutex
	threshold int
	openFor   time.Duration
	now       func() time.Time

	state        breakerState
	failures     int
	openUntil    time.Time
	probeRunning bool
	onTransition func(open bool)
}

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

func newBreaker(threshold int, openFor time.Duration, now func() time.Time) *breaker {
	if threshold <= 0 {
		threshold = 5
	}
	if openFor <= 0 {
		openFor = time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &breaker{threshold: threshold, openFor: openFor, now: now}
}

// Allow admits or rejects one attempt. An open breaker rejects fast as
// circuit_open; a half-open breaker admits a single probe.
func (b *breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerOpen:
		if b.now().Before(b.openUntil) {
			return provider.Errorf(provider.ErrCircuitOpen,
				"circuit open until %s", b.openUntil.Format(time.RFC3339))
		}
		b.state = breakerHalfOpen
		b.probeRunning = true
		return nil
	case breakerHalfOpen:
		if b.probeRunning {
			return provider.Errorf(provider.ErrCircuitOpen, "half-open probe in flight")
		}
		b.probeRunning = true
		return nil
	default:
		return nil
	}
}

// Report records the attempt outcome.
func (b *breaker) Report(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		if b.state != breakerClosed {
			b.transitionLocked(breakerClosed)
		}
		b.failures = 0
		b.probeRunning = false
		return
	}
	if b.state == breakerHalfOpen {
		b.probeRunning = false
		b.openUntil = b.now().Add(b.openFor)
		b.transitionLocked(breakerOpen)
		return
	}
	b.failures++
	if b.failures >= b.threshold && b.state == breakerClosed {
		b.openUntil = b.now().Add(b.openFor)
		b.transitionLocked(breakerOpen)
	}
}

func (b *breaker) transitionLocked(s breakerState) {
	b.state = s
	if b.onTransition != nil {
		b.onTransition(s != breakerClosed)
	}
}
