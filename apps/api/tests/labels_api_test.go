package tests

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"checkwallet/internal/label"
	"checkwallet/internal/platform/cache"
	"checkwallet/internal/provider/walletattribution"
	"checkwallet/internal/testutil"
)

const (
	lblAddr   = "0x00000000000000000000000000000000000000a1" // Binance exchange
	lblAddr2  = "0x00000000000000000000000000000000000000a2" // Phish Kit scam
	unlblAddr = "0x00000000000000000000000000000000deadbeef"
)

// testLabelsAPI boots the router with the labels service wired, after
// running the fixture CSVs through the real ingestion pipeline.
func testLabelsAPI(t *testing.T, publish bool) (*httptest.Server, *pgxpool.Pool, *redis.Client) {
	t.Helper()
	pool := testutil.RequireTestPool(t)
	rdb := testutil.RequireTestRedis(t)
	q := db.New(pool)
	resolver := testResolver(t, q)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if publish {
		svc := ingestion.NewService(pool, resolver, nil, log)
		runID, err := svc.RunSync(context.Background(), ingestion.RunRequest{
			SourceCode: walletattribution.SourceCode, TriggeredBy: "test",
			FilePath: "testdata/labels_eth.csv,testdata/labels_bsc.csv",
		})
		require.NoError(t, err)
		require.Equal(t, "COMPLETED", runStatus(t, q, runID).Status)
	}

	cfg := &config.Config{
		HTTPAddr: ":0", RedisURL: testutil.TestRedisURL(), LogLevel: "error",
		RateLimitRequests: 1000, RateLimitAnon: 1000, RateLimitWindow: time.Minute,
		LabelsStaleAfter: 168 * time.Hour,
	}
	labelSvc := label.NewService(q, rdb, cfg.LabelsStaleAfter)
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: q, Labels: labelSvc,
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, pool, rdb
}

func TestLabelsAPI_LabeledWallet(t *testing.T) {
	srv, pool, _ := testLabelsAPI(t, true)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels", key)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	d := bodyData(t, w)

	assert.Equal(t, "ethereum", d["network"])
	assert.Equal(t, "AVAILABLE", d["data_status"])
	assert.NotNil(t, d["last_synced_at"])
	assert.Equal(t, float64(1), d["labels_count"])

	labels := d["labels"].([]any)
	require.Len(t, labels, 1)
	l := labels[0].(map[string]any)
	assert.Equal(t, "EXCHANGE", l["category"])
	assert.Equal(t, "ENTITY_TYPE", l["category_type"])
	assert.Equal(t, "Binance: Hot Wallet", l["label"])
	assert.Equal(t, "reported", l["verification_status"]) // never auto-verified
	assert.Equal(t, "high", l["confidence"])
	assert.Equal(t, "exchange", l["upstream_category"])
	ent := l["entity"].(map[string]any)
	assert.Equal(t, "Binance", ent["name"])
	src := l["source"].(map[string]any)
	assert.Equal(t, "wallet_attribution", src["code"])
	assert.Equal(t, "eth-labels+cex-list", l["source_field"])
	assert.NotNil(t, l["evidence"])
	assert.Nil(t, l["revoked_at"])
}

func TestLabelsAPI_UnlabeledWallet(t *testing.T) {
	srv, pool, _ := testLabelsAPI(t, true)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/wallets/ethereum/"+unlblAddr+"/labels", key)
	require.Equal(t, http.StatusOK, w.Code)
	d := bodyData(t, w)
	// No labels ≠ safe: the contract exposes availability, not a verdict.
	assert.Equal(t, "AVAILABLE", d["data_status"])
	assert.Equal(t, float64(0), d["labels_count"])
	assert.Empty(t, d["labels"].([]any))
}

func TestLabelsAPI_UnavailableBeforeFirstPublish(t *testing.T) {
	srv, _, _ := testLabelsAPI(t, false)
	key := seedClient(t, testutil.RequireTestPool(t), "reader", "reader")

	w := get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels", key)
	require.Equal(t, http.StatusOK, w.Code)
	d := bodyData(t, w)
	assert.Equal(t, "UNAVAILABLE", d["data_status"])
	assert.Nil(t, d["last_synced_at"])
}

func TestLabelsAPI_Filters(t *testing.T) {
	srv, pool, _ := testLabelsAPI(t, true)
	key := seedClient(t, pool, "reader", "reader")

	// category filter (normalized code, case-insensitive upstream string too)
	w := get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels?category=exchange", key)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, float64(1), bodyData(t, w)["labels_count"])

	w = get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels?category=scam", key)
	assert.Equal(t, float64(0), bodyData(t, w)["labels_count"])

	// source filter
	w = get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels?source=wallet_attribution", key)
	assert.Equal(t, float64(1), bodyData(t, w)["labels_count"])
	w = get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels?source=other_src", key)
	assert.Equal(t, float64(0), bodyData(t, w)["labels_count"])

	// source_field substring match on the a+b+c vocabulary
	w = get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels?source_field=cex-list", key)
	assert.Equal(t, float64(1), bodyData(t, w)["labels_count"])
	w = get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels?source_field=forta", key)
	assert.Equal(t, float64(0), bodyData(t, w)["labels_count"])

	// verification_status
	w = get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels?verification_status=reported", key)
	assert.Equal(t, float64(1), bodyData(t, w)["labels_count"])
	w = get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels?verification_status=verified", key)
	assert.Equal(t, float64(0), bodyData(t, w)["labels_count"])
	w = get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels?verification_status=bogus", key)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	_ = pool
}

func TestLabelsAPI_RevokedHiddenThenShown(t *testing.T) {
	srv, pool, _ := testLabelsAPI(t, true)
	key := seedClient(t, pool, "reader", "reader")

	// Revoke the a5 label via a reduced republish, then check visibility.
	q := db.New(pool)
	svc := ingestion.NewService(pool, testResolver(t, q), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	runID, err := svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: walletattribution.SourceCode, TriggeredBy: "test",
		FilePath: "testdata/labels_eth_reduced.csv",
	})
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", runStatus(t, q, runID).Status)

	a5 := "0x00000000000000000000000000000000000000a5"
	w := get(t, srv, "/api/v1/wallets/ethereum/"+a5+"/labels", key)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, float64(0), bodyData(t, w)["labels_count"], "revoked hidden by default")

	w = get(t, srv, "/api/v1/wallets/ethereum/"+a5+"/labels?active_only=false", key)
	d := bodyData(t, w)
	assert.Equal(t, float64(1), d["labels_count"])
	l := d["labels"].([]any)[0].(map[string]any)
	assert.NotNil(t, l["revoked_at"], "revoked label must surface with active_only=false")
}

func TestLabelsAPI_PaginationAndInvalidCursor(t *testing.T) {
	srv, pool, _ := testLabelsAPI(t, true)
	key := seedClient(t, pool, "reader", "reader")

	// Seed extra labels for pagination: the dedup fixture gives 6 labels
	// across different wallets; use one wallet's two coexisting labels via
	// the coexist fixture for a 2-item page test.
	q := db.New(pool)
	svc := ingestion.NewService(pool, testResolver(t, q), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	runID, err := svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: walletattribution.SourceCode, TriggeredBy: "test",
		FilePath: "testdata/labels_coexist.csv",
	})
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", runStatus(t, q, runID).Status)

	c1 := "0x00000000000000000000000000000000000000c1"
	w := get(t, srv, "/api/v1/wallets/ethereum/"+c1+"/labels?limit=1", key)
	require.Equal(t, http.StatusOK, w.Code)
	d := bodyData(t, w)
	assert.Equal(t, float64(2), d["labels_count"], "count is total matches, not page size")
	labels := d["labels"].([]any)
	require.Len(t, labels, 1)
	require.NotEmpty(t, d["next_cursor"])

	// Page 2 via opaque cursor.
	w = get(t, srv, "/api/v1/wallets/ethereum/"+c1+"/labels?limit=1&cursor="+d["next_cursor"].(string), key)
	d = bodyData(t, w)
	require.Len(t, d["labels"].([]any), 1)
	assert.Empty(t, d["next_cursor"])

	// Malformed cursor → INVALID_CURSOR 400.
	w = get(t, srv, "/api/v1/wallets/ethereum/"+c1+"/labels?cursor=not-valid-base64!!", key)
	require.Equal(t, http.StatusBadRequest, w.Code)
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	assert.Equal(t, "INVALID_CURSOR", env.Error.Code)

	// A structurally-valid but negative cursor is also rejected.
	bad := base64.RawURLEncoding.EncodeToString([]byte("-5"))
	w = get(t, srv, "/api/v1/wallets/ethereum/"+c1+"/labels?cursor="+bad, key)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestLabelsAPI_CacheNamespacedByVersion(t *testing.T) {
	srv, pool, rdb := testLabelsAPI(t, true)
	key := seedClient(t, pool, "reader", "reader")

	get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels", key)
	// The cache key must embed the snapshot version.
	var found string
	iter := rdb.Scan(context.Background(), 0, cache.WalletLabels("ethereum", lblAddr, "*"), 100).Iterator()
	for iter.Next(context.Background()) {
		found = iter.Val()
	}
	require.NotEmpty(t, found, "labels cache entry must exist")
	assert.Contains(t, found, ":wattr-", "cache key must namespace by snapshot version")
}

func TestLabelsAPI_RequiresAuth(t *testing.T) {
	srv, _, _ := testLabelsAPI(t, true)
	w := get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestLabelsAPI_WalletSummaryShowsLabels(t *testing.T) {
	srv, pool, _ := testLabelsAPI(t, true)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr, key)
	require.Equal(t, http.StatusOK, w.Code)
	d := bodyData(t, w)
	assert.Equal(t, float64(1), d["labels_count"])
	labels := d["labels"].([]any)
	require.Len(t, labels, 1)
	l := labels[0].(map[string]any)
	assert.Equal(t, "EXCHANGE", l["category"])
	assert.Equal(t, "Binance: Hot Wallet", l["label"])
	// Wallet-level data_status stays sanctions-scoped (F10): no sanctions
	// snapshot published in this test → UNAVAILABLE is honest.
	assert.Equal(t, "UNAVAILABLE", d["data_status"])
}

func TestLabelsAPI_StaleAfterPublish(t *testing.T) {
	srv, pool, _ := testLabelsAPI(t, true)
	key := seedClient(t, pool, "reader", "reader")

	// Force the snapshot into staleness for this test only.
	_, err := pool.Exec(context.Background(),
		"UPDATE data_source_snapshots SET published_at = NOW() - interval '200 hours'")
	require.NoError(t, err)
	// Bust the cache namespace: the version key stays identical but
	// freshness is recomputed per response — STALE must surface regardless.
	w := get(t, srv, "/api/v1/wallets/ethereum/"+lblAddr+"/labels", key)
	d := bodyData(t, w)
	assert.Equal(t, "STALE", d["data_status"])
}
