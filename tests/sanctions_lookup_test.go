package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/httpserver"
	"checkwallet/internal/ingestion"
	"checkwallet/internal/platform/cache"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/testutil"
)

const (
	ethAddr = "0x098B716B8Aaf21512996dC57EB0615e2383E2f96" // ETH + USDT listings
	bnbAddr = "0xAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAa" // BNB listing (BSC)
)

// testAPIFull boots the router with sanctions lookups wired, after running
// the fixture through the real ingestion pipeline.
func testAPIFull(t *testing.T, publish bool) (*httptest.Server, *pgxpool.Pool, *redis.Client) {
	t.Helper()
	pool := testutil.RequireTestPool(t)
	rdb := testutil.RequireTestRedis(t)
	q := db.New(pool)
	resolver := testResolver(t, q)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if publish {
		svc := ingestion.NewService(pool, resolver, nil, log)
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
	sanSvc := sanctions.NewService(q, rdb, resolver, cfg.SanctionsStaleAfter)
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: q, Sanctions: sanSvc,
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, pool, rdb
}

func bodyData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	return env.Data
}

func TestSanctions_ExactMatch(t *testing.T) {
	srv, pool, _ := testAPIFull(t, true)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/sanctions/wallets/ethereum/"+ethAddr, key)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	d := bodyData(t, w)

	assert.Equal(t, true, d["matched"])
	assert.Equal(t, "WALLET_EXACT", d["match_type"])
	assert.Equal(t, "CHAIN_SPECIFIC", d["match_scope"])
	assert.Equal(t, "AVAILABLE", d["data_status"])
	assert.NotNil(t, d["last_synced_at"])

	sanctions, ok := d["sanctions"].([]any)
	require.True(t, ok)
	require.Len(t, sanctions, 1, "same entity listed under two Feature@IDs must dedupe")
	entry := sanctions[0].(map[string]any)
	assert.Equal(t, "LAZARUS GROUP", entry["entity_name"])
	assert.Equal(t, "31940", entry["external_entity_id"])
	progs := entry["programs"].([]any)
	codes := []string{progs[0].(map[string]any)["code"].(string),
		progs[1].(map[string]any)["code"].(string)}
	assert.ElementsMatch(t, []string{"CYBER2", "DPRK3"}, codes)

	reason := entry["reason"].(map[string]any)
	assert.Equal(t, "PROGRAM_CONTEXT_ONLY", reason["status"])
	assert.Nil(t, reason["summary"], "reason must never be synthesized")

	// The same address's USDT listing is network-unresolvable → surfaced
	// under unresolved_network_listings, never inside sanctions.
	unresolved := d["unresolved_network_listings"].([]any)
	require.Len(t, unresolved, 1)
	u := unresolved[0].(map[string]any)
	assert.Nil(t, u["network_code"])
	assert.Equal(t, "USDT", u["currency_code"])
	assert.Empty(t, d["cross_chain_observations"].([]any))
}

func TestSanctions_CrossChainNeverMatches(t *testing.T) {
	srv, pool, _ := testAPIFull(t, true)
	key := seedClient(t, pool, "reader", "reader")

	// The BNB-evidenced address queried on ethereum: observed, never matched.
	w := get(t, srv, "/api/v1/sanctions/wallets/ethereum/"+bnbAddr, key)
	require.Equal(t, http.StatusOK, w.Code)
	d := bodyData(t, w)
	assert.Equal(t, false, d["matched"])
	assert.Empty(t, d["sanctions"].([]any))
	cross := d["cross_chain_observations"].([]any)
	require.Len(t, cross, 1)
	assert.Equal(t, "bsc", cross[0].(map[string]any)["network_code"])
	assert.Equal(t, "KIM SOK IL", cross[0].(map[string]any)["entity_name"])

	// And the ETH-listed address on bsc: cross-chain, not a match.
	w = get(t, srv, "/api/v1/sanctions/wallets/bsc/"+ethAddr, key)
	d = bodyData(t, w)
	assert.Equal(t, false, d["matched"])
	cross = d["cross_chain_observations"].([]any)
	require.Len(t, cross, 1)
	assert.Equal(t, "ethereum", cross[0].(map[string]any)["network_code"])
	// USDT unresolved listing still surfaced on the bsc query.
	assert.Len(t, d["unresolved_network_listings"].([]any), 1)
}

func TestSanctions_NoMatchIsNotSafe(t *testing.T) {
	srv, pool, _ := testAPIFull(t, true)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv,
		"/api/v1/sanctions/wallets/ethereum/0x0000000000000000000000000000000000000001", key)
	require.Equal(t, http.StatusOK, w.Code)
	d := bodyData(t, w)
	assert.Equal(t, false, d["matched"])
	assert.Nil(t, d["match_type"])
	assert.Equal(t, "AVAILABLE", d["data_status"])
	assert.Empty(t, d["sanctions"].([]any))
	assert.Empty(t, d["cross_chain_observations"].([]any))
	assert.Empty(t, d["unresolved_network_listings"].([]any))
	// Response must contain no "safe"/"clean" verdict fields.
	assert.NotContains(t, strings.ToLower(w.Body.String()), "\"safe\"")
}

func TestSanctions_UnavailableBeforeFirstPublish(t *testing.T) {
	srv, pool, _ := testAPIFull(t, false)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/sanctions/wallets/ethereum/"+ethAddr, key)
	require.Equal(t, http.StatusOK, w.Code)
	d := bodyData(t, w)
	assert.Equal(t, false, d["matched"])
	assert.Equal(t, "UNAVAILABLE", d["data_status"])
	assert.Nil(t, d["last_synced_at"])
}

func TestSanctions_StaleDataStatus(t *testing.T) {
	srv, pool, _ := testAPIFull(t, true)
	key := seedClient(t, pool, "reader", "reader")

	_, err := pool.Exec(context.Background(),
		"UPDATE sanctions_snapshots SET published_at = NOW() - INTERVAL '72 hours'")
	require.NoError(t, err)

	w := get(t, srv, "/api/v1/sanctions/wallets/ethereum/"+ethAddr, key)
	d := bodyData(t, w)
	assert.Equal(t, "STALE", d["data_status"])
}

func TestSanctions_CacheVersionedKeys(t *testing.T) {
	srv, pool, rdb := testAPIFull(t, true)
	key := seedClient(t, pool, "reader", "reader")
	ctx := context.Background()

	w := get(t, srv, "/api/v1/sanctions/wallets/ethereum/"+ethAddr, key)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, true, bodyData(t, w)["matched"])

	// Find the versioned key that was written.
	var version string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT version FROM sanctions_snapshots ORDER BY published_at DESC, id DESC LIMIT 1").Scan(&version))
	cacheKey := cache.WalletSanctions("ethereum", strings.ToLower(ethAddr), version)
	n, err := rdb.Exists(ctx, cacheKey).Result()
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "lookup must populate wallet:sanctions:{net}:{addr}:{version}")

	// Sentinel overwrite: a cache HIT must serve the sentinel, proving the
	// second response comes from cache, not the repo.
	require.NoError(t, rdb.Set(ctx, cacheKey,
		`{"matched":false,"sanctions":[],"cross_chain_observations":[],"unresolved_network_listings":[]}`,
		time.Hour).Err())
	w = get(t, srv, "/api/v1/sanctions/wallets/ethereum/"+ethAddr, key)
	assert.Equal(t, false, bodyData(t, w)["matched"], "cache hit must serve the cached payload")

	// Freshness fields are recomputed even on cache hits.
	d := bodyData(t, w)
	assert.Equal(t, "AVAILABLE", d["data_status"])
	require.NoError(t, rdb.Del(ctx, cacheKey).Err())

	// New snapshot version → old key unreachable; new lookup repopulates.
	// The republish runs on the SAME pool — a second RequireTestPool would
	// truncate api_clients and 401 the reader key.
	reduced := rewriteFixture(t, ofacFixture, map[string]string{
		usdtFeatureBlock: "",
		"<Day>28</Day>":  "<Day>29</Day>",
	})
	q := db.New(pool)
	svc := ingestion.NewService(pool, testResolver(t, q), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	runID, err := svc.RunSync(ctx, ingestion.RunRequest{
		SourceCode: "ofac_sdn", TriggeredBy: "test", FilePath: reduced,
	})
	require.NoError(t, err)
	run := runStatus(t, q, runID)
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)

	var newVersion string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT version FROM sanctions_snapshots ORDER BY published_at DESC, id DESC LIMIT 1").Scan(&newVersion))
	require.NotEqual(t, version, newVersion)
	w = get(t, srv, "/api/v1/sanctions/wallets/ethereum/"+ethAddr, key)
	require.Equal(t, true, bodyData(t, w)["matched"])
}

func TestSanctions_EntityAndProgramEndpoints(t *testing.T) {
	srv, pool, _ := testAPIFull(t, true)
	key := seedClient(t, pool, "reader", "reader")
	ctx := context.Background()

	var entityID int64
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id FROM sanctions_entities WHERE external_entity_id='31940'").Scan(&entityID))

	w := get(t, srv, fmt.Sprintf("/api/v1/sanctions/entities/%d", entityID), key)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	d := bodyData(t, w)
	assert.Equal(t, "LAZARUS GROUP", d["entity_name"])
	assert.Equal(t, "31940", d["external_entity_id"])
	assert.Len(t, d["programs"].([]any), 2)
	assert.Len(t, d["digital_currency_addresses"].([]any), 3)
	assert.Equal(t, "PROGRAM_CONTEXT_ONLY",
		d["reason"].(map[string]any)["status"])
	assert.NotNil(t, d["last_synced_at"])

	w = get(t, srv, "/api/v1/sanctions/entities/999999", key)
	assert.Equal(t, http.StatusNotFound, w.Code)

	w = get(t, srv, "/api/v1/sanctions/programs/DPRK3", key)
	require.Equal(t, http.StatusOK, w.Code)
	d = bodyData(t, w)
	assert.Equal(t, "DPRK3", d["code"])
	assert.Equal(t, float64(2), d["associated_entities_count"])

	w = get(t, srv, "/api/v1/sanctions/programs/NOPE", key)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestSanctions_Unauthenticated(t *testing.T) {
	srv, _, _ := testAPIFull(t, true)
	w := get(t, srv, "/api/v1/sanctions/wallets/ethereum/"+ethAddr, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestSanctions_InvalidNetworkAndAddress(t *testing.T) {
	srv, pool, _ := testAPIFull(t, true)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/sanctions/wallets/solana/"+ethAddr, key)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w = get(t, srv, "/api/v1/sanctions/wallets/ethereum/notanaddress", key)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
