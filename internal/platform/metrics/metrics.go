// Package metrics registers the Prometheus collectors for the shipped
// surface (spec §17 scope-applicable subset) and the middleware that feeds
// them. Route labels use route patterns, never raw paths.
package metrics

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"checkwallet/internal/db"
)

// Metrics bundles every collector and the registry that serves /metrics.
type Metrics struct {
	registry *prometheus.Registry

	RequestsTotal   *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec

	SanctionsMatches     prometheus.Counter
	IngestionDuration    *prometheus.HistogramVec
	IngestionRecords     *prometheus.CounterVec
	IngestionLastSuccess prometheus.Gauge
	RateLimitStoreErrors prometheus.Counter

	DynamicLabelCategories prometheus.Counter

	// Live provider instrumentation (spec §15/§17).
	ProviderRequestsTotal    *prometheus.CounterVec // {source,result}
	ProviderErrorsTotal      *prometheus.CounterVec // {source,reason}
	ProviderRateLimitedTotal *prometheus.CounterVec // {source}
	ProviderCircuitOpen      *prometheus.GaugeVec   // {source}
}

// New builds the registry. snapshotQuery supplies the latest published_at
// for the age gauge (computed on scrape, never cached); labelsActiveQuery
// supplies the live wallet_labels count for the active-labels gauge —
// scrape-time so API and worker processes never diverge.
func New(snapshotQuery func(ctx context.Context) (time.Time, bool),
	labelsActiveQuery func(ctx context.Context) (int64, bool)) *Metrics {
	reg := prometheus.NewRegistry()
	fac := promauto.With(reg)

	m := &Metrics{
		registry: reg,
		RequestsTotal: fac.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests by route pattern and status.",
		}, []string{"route", "status"}),
		RequestDuration: fac.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency by route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
		SanctionsMatches: fac.NewCounter(prometheus.CounterOpts{
			Name: "sanctions_matches_total",
			Help: "Direct sanctions matches served.",
		}),
		IngestionDuration: fac.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "sanctions_ingestion_duration_seconds",
			Help:    "Ingestion run wall time by outcome.",
			Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1200},
		}, []string{"source", "result"}),
		IngestionRecords: fac.NewCounterVec(prometheus.CounterOpts{
			Name: "ingestion_records_total",
			Help: "Records processed by source and outcome class.",
		}, []string{"source", "result"}),
		IngestionLastSuccess: fac.NewGauge(prometheus.GaugeOpts{
			Name: "ingestion_last_success_timestamp",
			Help: "Unix timestamp of the last completed ingestion run.",
		}),
		RateLimitStoreErrors: fac.NewCounter(prometheus.CounterOpts{
			Name: "rate_limit_store_errors_total",
			Help: "Rate-limiter store failures served fail-open.",
		}),
		DynamicLabelCategories: fac.NewCounter(prometheus.CounterOpts{
			Name: "labels_dynamic_categories_total",
			Help: "Risk categories created dynamically from unseen upstream codes.",
		}),
		ProviderRequestsTotal: fac.NewCounterVec(prometheus.CounterOpts{
			Name: "provider_requests_total",
			Help: "On-demand provider lookups by source and outcome.",
		}, []string{"source", "result"}),
		ProviderErrorsTotal: fac.NewCounterVec(prometheus.CounterOpts{
			Name: "provider_errors_total",
			Help: "Provider lookup failures by source and typed reason.",
		}, []string{"source", "reason"}),
		ProviderRateLimitedTotal: fac.NewCounterVec(prometheus.CounterOpts{
			Name: "provider_rate_limited_total",
			Help: "Provider lookups rejected by upstream 429 or the local limiter.",
		}, []string{"source"}),
		ProviderCircuitOpen: fac.NewGaugeVec(prometheus.GaugeOpts{
			Name: "provider_circuit_open",
			Help: "Provider circuit breaker state (1=open/half-open, 0=closed).",
		}, []string{"source"}),
	}

	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "wallet_labels_active_total",
		Help: "Live (non-revoked) wallet labels across all sources.",
	}, func() float64 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		n, ok := labelsActiveQuery(ctx)
		if !ok {
			return 0
		}
		return float64(n)
	}))

	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "sanctions_snapshot_age_seconds",
		Help: "Age of the newest published sanctions snapshot.",
	}, func() float64 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		pub, ok := snapshotQuery(ctx)
		if !ok {
			return 0
		}
		return time.Since(pub).Seconds()
	}))

	return m
}

// Handler returns the /metrics scrape handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveRequest records one completed HTTP request.
func (m *Metrics) ObserveRequest(route string, status int, d time.Duration) {
	m.RequestsTotal.WithLabelValues(route, itoa(status)).Inc()
	m.RequestDuration.WithLabelValues(route).Observe(d.Seconds())
}

// ObserveIngestion records a finished run's duration and record counts.
func (m *Metrics) ObserveIngestion(source, result string, d time.Duration) {
	m.IngestionDuration.WithLabelValues(source, result).Observe(d.Seconds())
	if result == "COMPLETED" {
		m.IngestionLastSuccess.SetToCurrentTime()
	}
}

// IncIngestionRecords adds to the per-result record counter.
func (m *Metrics) IncIngestionRecords(source, result string, n int64) {
	if n > 0 {
		m.IngestionRecords.WithLabelValues(source, result).Add(float64(n))
	}
}

// IncSanctionsMatch counts a direct-match lookup.
func (m *Metrics) IncSanctionsMatch() { m.SanctionsMatches.Inc() }

// IncRateLimitStoreError counts fail-open rate-limiter store failures.
func (m *Metrics) IncRateLimitStoreError() { m.RateLimitStoreErrors.Inc() }

// IncDynamicLabelCategories counts categories created at publish time from
// unseen upstream codes (warn-and-track posture).
func (m *Metrics) IncDynamicLabelCategories(n int64) {
	m.DynamicLabelCategories.Add(float64(n))
}

// ObserveProviderRequest counts one provider outcome ("ok" or an ErrKind).
func (m *Metrics) ObserveProviderRequest(source, result string) {
	m.ProviderRequestsTotal.WithLabelValues(source, result).Inc()
}

// IncProviderError counts one failed provider lookup by typed reason.
func (m *Metrics) IncProviderError(source, reason string) {
	m.ProviderErrorsTotal.WithLabelValues(source, reason).Inc()
}

// IncProviderRateLimited counts 429/limiter rejections per source.
func (m *Metrics) IncProviderRateLimited(source string) {
	m.ProviderRateLimitedTotal.WithLabelValues(source).Inc()
}

// SetProviderCircuit drives the breaker-state gauge.
func (m *Metrics) SetProviderCircuit(source string, open bool) {
	v := 0.0
	if open {
		v = 1
	}
	m.ProviderCircuitOpen.WithLabelValues(source).Set(v)
}

// ProviderHooks adapts the collectors to the resilience toolkit hook shape
// (avoids a resilience→metrics import).
func (m *Metrics) ProviderHooks(source string) (onResult, onError func(s, v string),
	onRateLimited func(s string), onCircuit func(s string, open bool)) {
	return func(s, v string) { m.ObserveProviderRequest(s, v) },
		func(s, v string) { m.IncProviderError(s, v) },
		func(s string) { m.IncProviderRateLimited(s) },
		func(s string, open bool) { m.SetProviderCircuit(s, open) }
}

func itoa(i int) string { return strconv.Itoa(i) }

// LabelsActiveQuery adapts the generated query to the gauge callback.
func LabelsActiveQuery(q *db.Queries) func(context.Context) (int64, bool) {
	return func(ctx context.Context) (int64, bool) {
		n, err := q.CountActiveLabels(ctx)
		return n, err == nil
	}
}

// LatestSnapshotQuery adapts the generated query to the gauge callback.
func LatestSnapshotQuery(q *db.Queries) func(context.Context) (time.Time, bool) {
	return func(ctx context.Context) (time.Time, bool) {
		snap, err := q.LatestSnapshotPublished(ctx)
		if err != nil {
			if err == pgx.ErrNoRows {
				return time.Time{}, false
			}
			return time.Time{}, false
		}
		return snap.PublishedAt.Time.UTC(), snap.PublishedAt.Valid
	}
}
