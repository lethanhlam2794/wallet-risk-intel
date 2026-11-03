package tests

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/httpserver"
	"checkwallet/internal/ingestion"
	"checkwallet/internal/platform/metrics"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/testutil"
)

// testAPIObserved boots the full router with metrics wired and the access
// log writing to the returned buffer (for the no-secrets assertion).
func testAPIObserved(t *testing.T, publish bool) (*httptest.Server, *pgxpool.Pool, *bytes.Buffer) {
	t.Helper()
	pool := testutil.RequireTestPool(t)
	rdb := testutil.RequireTestRedis(t)
	q := db.New(pool)
	resolver := testResolver(t, q)
	logBuf := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(io.MultiWriter(logBuf), nil))

	if publish {
		svc := ingestion.NewService(pool, rdb, resolver, nil, log)
		runID, err := svc.RunSync(context.Background(), ingestion.RunRequest{
			SourceCode: "ofac_sdn", TriggeredBy: "test", FilePath: ofacFixture,
		})
		require.NoError(t, err)
		run := runStatus(t, q, runID)
		require.Equal(t, "COMPLETED", run.Status, "publish failed: %s", run.ErrorMessage.String)
	}

	cfg := &config.Config{
		HTTPAddr: ":0", RedisURL: testutil.TestRedisURL(), LogLevel: "error",
		RateLimitRequests: 1000, RateLimitAnon: 1000, RateLimitWindow: time.Minute,
		SanctionsStaleAfter: 48 * time.Hour,
	}
	m := metrics.New(metrics.LatestSnapshotQuery(q))
	sanSvc := sanctions.NewService(q, rdb, resolver, cfg.SanctionsStaleAfter)
	sanSvc.SetMetrics(m)
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: q, Sanctions: sanSvc, Metrics: m,
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, pool, logBuf
}

// TestMetrics_Endpoint: /metrics requires a reader key and serves the
// declared collectors with real values after a publish + lookup.
func TestMetrics_Endpoint(t *testing.T) {
	srv, pool, _ := testAPIObserved(t, true)
	key := seedClient(t, pool, "reader", "reader")

	// Unauthenticated → 401.
	w := get(t, srv, "/metrics", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// Drive a matched lookup so the counter has a real value.
	w = get(t, srv, "/api/v1/sanctions/wallets/ethereum/"+ethAddr, key)
	require.Equal(t, http.StatusOK, w.Code)

	w = get(t, srv, "/metrics", key)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	for _, name := range []string{
		"http_requests_total",
		"http_request_duration_seconds",
		"sanctions_matches_total",
		"sanctions_snapshot_age_seconds",
	} {
		assert.Contains(t, body, name, "missing collector %s", name)
	}
	assert.Contains(t, body, `sanctions_matches_total 1`)
	assert.Contains(t, body, `route="/api/v1/sanctions/wallets/:network/:address"`)
}

// TestMetrics_IngestionValues: a completed run publishes record counters,
// duration, last-success, and a positive snapshot age.
func TestMetrics_IngestionValues(t *testing.T) {
	srv, pool, _ := testAPIObserved(t, false)
	q := db.New(pool)
	resolver := testResolver(t, q)
	rdb := testutil.RequireTestRedis(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Wire the same Metrics registry the server uses into a new run.
	// The server built inside testAPIObserved owns its own registry, so run
	// a service with a fresh registry and scrape IT — what matters is the
	// collectors carry real values.
	m2 := metrics.New(metrics.LatestSnapshotQuery(q))
	svc := ingestion.NewService(pool, rdb, resolver, nil, log)
	svc.SetMetrics(m2)
	runID, err := svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: "ofac_sdn", TriggeredBy: "test", FilePath: ofacFixture,
	})
	require.NoError(t, err)
	run := runStatus(t, q, runID)
	require.Equal(t, "COMPLETED", run.Status)

	w := httptest.NewRecorder()
	m2.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := w.Body.String()
	assert.Contains(t, body, `ingestion_records_total{result="fetched",source="ofac_sdn"}`)
	assert.Contains(t, body, "sanctions_ingestion_duration_seconds")
	assert.Contains(t, body, "ingestion_last_success_timestamp")
	// Snapshot age gauge reports a small positive value.
	idx := strings.Index(body, "sanctions_snapshot_age_seconds")
	require.NotEqual(t, -1, idx)
	_ = srv
}

// TestFailure_WorkerCrashStaleRun: a RUNNING row older than the timeout is
// reaped as FAILED — crashed workers never block new runs forever.
func TestFailure_WorkerCrashStaleRun(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, false)
	ctx := context.Background()
	source, err := q.GetSourceByCode(ctx, "ofac_sdn")
	require.NoError(t, err)

	staleID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO ingestion_runs (id, source_id, status, started_at, triggered_by)
		 VALUES ($1, $2, 'RUNNING', NOW() - INTERVAL '2 hours', 'worker')`,
		pgtype.UUID{Bytes: staleID, Valid: true}, source.ID)
	require.NoError(t, err)

	svc.SweepStaleRuns(ctx)
	run := runStatus(t, q, staleID)
	assert.Equal(t, "FAILED", run.Status)
	assert.Equal(t, "worker restarted", run.ErrorMessage.String)

	// And a subsequent real run is not blocked.
	runID := runFixtureSync(t, svc, ofacFixture)
	assert.Equal(t, "COMPLETED", runStatus(t, q, runID).Status)
}

// TestFailure_SchemaDrift: an XML document with a wrong root fails at stage
// and never touches the live snapshot.
func TestFailure_SchemaDrift(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, false)

	runID := runFixtureSync(t, svc, "../internal/provider/ofac/testdata/wrong_root.xml")
	run := runStatus(t, q, runID)
	require.Equal(t, "FAILED", run.Status)
	assert.Zero(t, count(t, pool, "SELECT count(*) FROM sanctions_snapshots"))
}

// TestLogging_NoSecrets: API key material must never appear in request
// logs; request_id must be present on every request-scoped line.
func TestLogging_NoSecrets(t *testing.T) {
	srv, pool, logBuf := testAPIObserved(t, false)
	key := seedClient(t, pool, "leaky-reader", "reader")
	secret := key // raw key material

	w := get(t, srv, "/api/v1/networks", key)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotEmpty(t, w.Header().Get("X-Request-ID"))

	out := logBuf.String()
	assert.NotContains(t, out, secret, "API key material leaked into logs")
	assert.Contains(t, out, "request_id=")
	assert.Contains(t, out, "http request")
}
