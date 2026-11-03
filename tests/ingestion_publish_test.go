package tests

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/httpserver"
	"checkwallet/internal/ingestion"
	"checkwallet/internal/network"
	"checkwallet/internal/platform/cache"
	"checkwallet/internal/testutil"
)

func testResolver(t *testing.T, q *db.Queries) *network.Resolver {
	t.Helper()
	rows, err := q.ListActiveNetworks(context.Background())
	require.NoError(t, err)
	nets := make([]network.Network, 0, len(rows))
	for _, r := range rows {
		nets = append(nets, network.Network{
			ID: r.ID, Namespace: r.Namespace, ChainReference: r.ChainReference,
			Code: r.Code, Name: r.Name,
		})
	}
	return network.NewResolver(nets)
}

func newIngestionService(t *testing.T, withRedis bool) (*ingestion.Service, *db.Queries, *pgxpool.Pool, *redis.Client) {
	t.Helper()
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	var rdb *redis.Client
	if withRedis {
		rdb = testutil.RequireTestRedis(t)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := ingestion.NewService(pool, rdb, testResolver(t, q), nil, log)
	return svc, q, pool, rdb
}

func runFixtureSync(t *testing.T, svc *ingestion.Service, path string) uuid.UUID {
	t.Helper()
	runID, err := svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: "ofac_sdn", TriggeredBy: "test", FilePath: path,
	})
	require.NoError(t, err)
	return runID
}

func runStatus(t *testing.T, q *db.Queries, runID uuid.UUID) db.IngestionRun {
	t.Helper()
	run, err := q.GetIngestionRun(context.Background(),
		pgtype.UUID{Bytes: runID, Valid: true})
	require.NoError(t, err)
	return run
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func TestIngestion_PublishFixture(t *testing.T) {
	svc, q, pool, rdb := newIngestionService(t, true)
	ctx := context.Background()

	runID := runFixtureSync(t, svc, ofacFixture)
	run := runStatus(t, q, runID)
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)
	require.True(t, run.SnapshotVersion.Valid)
	require.Equal(t, "test", run.TriggeredBy.String)

	assert.Equal(t, int64(2), count(t, pool, "SELECT count(*) FROM sanctions_entities"))
	assert.Equal(t, int64(5), count(t, pool, "SELECT count(*) FROM sanctions_wallets WHERE revoked_at IS NULL"))
	assert.Equal(t, int64(3), count(t, pool, "SELECT count(*) FROM wallets"))
	assert.Equal(t, int64(3), count(t, pool, "SELECT count(*) FROM sanctions_entity_programs"))
	assert.Equal(t, int64(2), count(t, pool, "SELECT count(*) FROM sanctions_programs"))
	assert.Equal(t, int64(1), count(t, pool, "SELECT count(*) FROM sanctions_snapshots"))

	// Match-scope invariants: ambiguous listings stay UNRESOLVED with NULL
	// network; chain-bound listings are CHAIN_SPECIFIC, and only the three
	// evidenced EVM listings resolve to an indexed network_id.
	assert.Equal(t, int64(1), count(t, pool,
		"SELECT count(*) FROM sanctions_wallets WHERE match_scope='UNRESOLVED' AND network_id IS NULL"))
	assert.Equal(t, int64(4), count(t, pool,
		"SELECT count(*) FROM sanctions_wallets WHERE match_scope='CHAIN_SPECIFIC'"))
	assert.Equal(t, int64(3), count(t, pool,
		"SELECT count(*) FROM sanctions_wallets WHERE network_id IS NOT NULL"))
	assert.Equal(t, int64(2), count(t, pool,
		"SELECT count(*) FROM sanctions_wallets WHERE network_id IS NULL"))

	// Cache pointer moved after commit.
	version, err := rdb.Get(ctx, cache.CurrentSnapshot()).Result()
	require.NoError(t, err)
	assert.Equal(t, run.SnapshotVersion.String, version)

	// Successful runs do not keep staging.
	assert.Zero(t, count(t, pool,
		"SELECT count(*) FROM stg_sanctions_entities WHERE run_id=$1",
		pgtype.UUID{Bytes: runID, Valid: true}))
}

func TestIngestion_ChecksumNoOp(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, true)

	runFixtureSync(t, svc, ofacFixture)
	runID2 := runFixtureSync(t, svc, ofacFixture)

	run2 := runStatus(t, q, runID2)
	require.Equal(t, "COMPLETED", run2.Status)
	assert.Equal(t, int64(1), count(t, pool,
		"SELECT count(*) FROM sanctions_snapshots"),
		"identical checksum must not add a snapshot")
	assert.Zero(t, count(t, pool,
		"SELECT count(*) FROM stg_sanctions_entities WHERE run_id=$1",
		pgtype.UUID{Bytes: runID2, Valid: true}),
		"checksum no-op must skip staging entirely")
}

func TestIngestion_ConcurrentRejected(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, false)
	ctx := context.Background()
	source, err := q.GetSourceByCode(ctx, "ofac_sdn")
	require.NoError(t, err)

	// Simulate an in-flight run holding the advisory lock on another conn.
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	var locked bool
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT pg_try_advisory_lock($1)", source.ID).Scan(&locked))
	require.True(t, locked)
	defer func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", source.ID)
		conn.Release()
	}()

	_, err = svc.Enqueue(ctx, ingestion.RunRequest{SourceCode: "ofac_sdn"})
	assert.ErrorIs(t, err, ingestion.ErrIngestionRunning)
}

func TestIngestion_FailedRunKeepsStagingAndLeavesLiveAlone(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, false)

	// First failure mode: truncated XML fails at stage; run marked FAILED,
	// nothing published.
	runID := runFixtureSync(t, svc, "../internal/provider/ofac/testdata/truncated.xml")
	run := runStatus(t, q, runID)
	require.Equal(t, "FAILED", run.Status)
	assert.Contains(t, run.ErrorMessage.String, "stage")
	assert.Zero(t, count(t, pool, "SELECT count(*) FROM sanctions_snapshots"))
	assert.Zero(t, count(t, pool, "SELECT count(*) FROM sanctions_wallets"))

	// Second failure mode: an orphan program link (SanctionsEntry pointing
	// at a ProfileID not in DistinctParties) must fail validation AFTER
	// staging completed — staging stays for diagnosis.
	orphan := rewriteFixture(t, ofacFixture, map[string]string{
		"  </SanctionsEntries>": `    <SanctionsEntry ID="99999" ProfileID="99999" ListID="1550">
      <EntryEvent ID="99999" EntryEventTypeID="1" LegalBasisID="92049">
        <Date CalendarTypeID="1"><Year>2023</Year><Month>1</Month><Day>1</Day></Date>
      </EntryEvent>
      <SanctionsMeasure ID="99999" SanctionsTypeID="1">
        <Comment>ORPHAN_PROG</Comment>
      </SanctionsMeasure>
    </SanctionsEntry>
  </SanctionsEntries>`,
	})
	runID2 := runFixtureSync(t, svc, orphan)
	run2 := runStatus(t, q, runID2)
	require.Equal(t, "FAILED", run2.Status)
	assert.Contains(t, run2.ErrorMessage.String, "validate")
	assert.Positive(t, count(t, pool,
		"SELECT count(*) FROM stg_sanctions_entities WHERE run_id=$1",
		pgtype.UUID{Bytes: runID2, Valid: true}),
		"validation failures must keep staged rows for diagnosis")
}

func TestIngestion_Revocation(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, true)

	runFixtureSync(t, svc, ofacFixture)

	// Reduced snapshot: drop the USDT Feature from entity 31940.
	reduced := rewriteFixture(t, ofacFixture, map[string]string{
		usdtFeatureBlock: "",
		"<Day>28</Day>":  "<Day>29</Day>",
	})
	runID2 := runFixtureSync(t, svc, reduced)
	run2 := runStatus(t, q, runID2)
	require.Equal(t, "COMPLETED", run2.Status, "run failed: %s", run2.ErrorMessage.String)

	assert.Equal(t, int64(4), count(t, pool,
		"SELECT count(*) FROM sanctions_wallets WHERE revoked_at IS NULL"))
	assert.Equal(t, int64(1), count(t, pool,
		"SELECT count(*) FROM sanctions_wallets WHERE revoked_at IS NOT NULL"),
		"removed USDT listing must be revoked, not deleted")

	var currency string
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT currency_code FROM sanctions_wallets WHERE revoked_at IS NOT NULL").Scan(&currency))
	assert.Equal(t, "USDT", currency)
}

func TestIngestion_DateRegression(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, true)

	runFixtureSync(t, svc, ofacFixture)

	older := rewriteFixture(t, ofacFixture, map[string]string{
		"<Day>28</Day>": "<Day>20</Day>",
	})
	runID2 := runFixtureSync(t, svc, older)
	run2 := runStatus(t, q, runID2)
	require.Equal(t, "FAILED", run2.Status)
	assert.Contains(t, run2.ErrorMessage.String, "DateOfIssue regression")
	assert.Equal(t, int64(1), count(t, pool,
		"SELECT count(*) FROM sanctions_snapshots"))
}

const usdtFeatureBlock = `        <Feature ID="40578" FeatureTypeID="887">
          <FeatureVersion ID="38297" ReliabilityID="1">
            <VersionDetail DetailTypeID="1432">0x098B716B8Aaf21512996dC57EB0615e2383E2f96</VersionDetail>
          </FeatureVersion>
        </Feature>
`

func rewriteFixture(t *testing.T, src string, repl map[string]string) string {
	t.Helper()
	b, err := os.ReadFile(src)
	require.NoError(t, err)
	s := string(b)
	for old, new := range repl {
		require.Contains(t, s, old, "fixture %s missing expected block", src)
		s = strings.ReplaceAll(s, old, new)
	}
	dst := filepath.Join(t.TempDir(), "modified.xml")
	require.NoError(t, os.WriteFile(dst, []byte(s), 0o600))
	return dst
}

// testAPIWithAdmin boots the full router including admin ingestion routes.
func testAPIWithAdmin(t *testing.T) (*httptest.Server, *pgxpool.Pool, *redis.Client) {
	t.Helper()
	pool := testutil.RequireTestPool(t)
	rdb := testutil.RequireTestRedis(t)
	queries := db.New(pool)
	resolver := testResolver(t, queries)

	cfg := &config.Config{
		HTTPAddr: ":0", RedisURL: testutil.TestRedisURL(), LogLevel: "error",
		RateLimitRequests: 1000, RateLimitAnon: 1000, RateLimitWindow: time.Minute,
		SanctionsStaleAfter: 48 * time.Hour,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := ingestion.NewService(pool, rdb, resolver, nil, log)
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: queries,
		Admin: ingestion.NewAdminHandler(svc, queries, log),
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, pool, rdb
}

func TestAdminTrigger_EndToEnd(t *testing.T) {
	srv, pool, _ := testAPIWithAdmin(t)
	readerKey := seedClient(t, pool, "reader", "reader")
	adminKey := seedClient(t, pool, "admin", "admin")
	q := db.New(pool)
	ctx := context.Background()

	post := func(key, path string) *httptest.ResponseRecorder {
		req, err := http.NewRequest(http.MethodPost, srv.URL+path,
			strings.NewReader(`{"source":"ofac_sdn"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		w := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(w, req)
		return w
	}

	// Reader role is rejected.
	w := post(readerKey, "/api/v1/admin/ingestion-runs")
	assert.Equal(t, http.StatusForbidden, w.Code)

	// Admin triggers → 202 with a queryable run row.
	w = post(adminKey, "/api/v1/admin/ingestion-runs")
	require.Equal(t, http.StatusAccepted, w.Code, "body: %s", w.Body.String())
	var runID uuid.UUID
	require.Eventually(t, func() bool {
		return count(t, pool, "SELECT count(*) FROM ingestion_runs") == 1
	}, 5*time.Second, 50*time.Millisecond)
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id FROM ingestion_runs LIMIT 1").Scan(&runID))
	require.Eventually(t, func() bool {
		run, err := q.GetIngestionRun(ctx, pgtype.UUID{Bytes: runID, Valid: true})
		return err == nil && run.Status == "FAILED" // nil downloader → fetch fails
	}, 5*time.Second, 50*time.Millisecond)

	// GET the run row back over the API.
	w = get(t, srv, "/api/v1/admin/ingestion-runs/"+runID.String(), adminKey)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), runID.String())

	// Unknown run id → 404.
	w = get(t, srv, "/api/v1/admin/ingestion-runs/"+uuid.NewString(), adminKey)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
