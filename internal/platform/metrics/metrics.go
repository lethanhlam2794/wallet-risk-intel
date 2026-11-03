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
}

// New builds the registry. snapshotQuery supplies the latest published_at
// for the age gauge (computed on scrape, never cached).
func New(snapshotQuery func(ctx context.Context) (time.Time, bool)) *Metrics {
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
	}

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

func itoa(i int) string { return strconv.Itoa(i) }

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
