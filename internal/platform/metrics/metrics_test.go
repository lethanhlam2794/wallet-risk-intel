package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestMetrics(t *testing.T) *Metrics {
	t.Helper()
	m := New(
		func(ctx context.Context) (time.Time, bool) { return time.Now(), true },
		func(ctx context.Context) (int64, bool) { return 1, true },
	)
	require.NotNil(t, m)
	return m
}

func TestProviderCollectors_RegisterAndCount(t *testing.T) {
	m := newTestMetrics(t)

	m.ObserveProviderRequest("goplus", "ok")
	m.ObserveProviderRequest("goplus", "rate_limited")
	m.IncProviderError("goplus", "upstream_5xx")
	m.IncProviderRateLimited("goplus")
	m.SetProviderCircuit("goplus", true)

	assert.Equal(t, 2.0, testutil.ToFloat64(
		m.ProviderRequestsTotal.WithLabelValues("goplus", "ok"))+
		testutil.ToFloat64(m.ProviderRequestsTotal.WithLabelValues("goplus", "rate_limited")))
	assert.Equal(t, 1.0, testutil.ToFloat64(
		m.ProviderErrorsTotal.WithLabelValues("goplus", "upstream_5xx")))
	assert.Equal(t, 1.0, testutil.ToFloat64(
		m.ProviderRateLimitedTotal.WithLabelValues("goplus")))
	assert.Equal(t, 1.0, testutil.ToFloat64(
		m.ProviderCircuitOpen.WithLabelValues("goplus")))

	m.SetProviderCircuit("goplus", false)
	assert.Equal(t, 0.0, testutil.ToFloat64(
		m.ProviderCircuitOpen.WithLabelValues("goplus")))
}

func TestProviderCollectors_Scrapeable(t *testing.T) {
	m := newTestMetrics(t)
	m.ObserveProviderRequest("publicaml", "ok")
	m.IncProviderError("publicaml", "timeout")
	m.IncProviderRateLimited("publicaml")
	m.SetProviderCircuit("publicaml", false)

	fams, err := m.registry.Gather()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, f := range fams {
		names[f.GetName()] = true
	}
	for _, want := range []string{
		"provider_requests_total",
		"provider_errors_total",
		"provider_rate_limited_total",
		"provider_circuit_open",
	} {
		assert.True(t, names[want], "collector %s must be registered", want)
	}
}
