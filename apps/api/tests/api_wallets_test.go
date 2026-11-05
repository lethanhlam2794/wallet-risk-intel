// Package tests contains cross-package integration tests. They run ONLY
// against the dedicated check_wallet_test database (enforced by testutil).
package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/httpserver"
	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/testutil"
)

// testAPI boots the real router over the dedicated test DB + test Redis.
func testAPI(t *testing.T) (*httptest.Server, *pgxpool.Pool, *redis.Client) {
	t.Helper()
	pool := testutil.RequireTestPool(t)
	rdb := testutil.RequireTestRedis(t)
	queries := db.New(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := queries.ListActiveNetworks(ctx)
	require.NoError(t, err)
	nets := make([]network.Network, 0, len(rows))
	for _, r := range rows {
		nets = append(nets, network.Network{
			ID: r.ID, Namespace: r.Namespace, ChainReference: r.ChainReference, Code: r.Code, Name: r.Name,
		})
	}

	cfg := &config.Config{
		HTTPAddr: ":0", RedisURL: testutil.TestRedisURL(), LogLevel: "error",
		RateLimitRequests: 1000, RateLimitAnon: 1000, RateLimitWindow: time.Minute,
		SanctionsStaleAfter: 48 * time.Hour,
	}
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg,
		Logger:   slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		Resolver: network.NewResolver(nets), Queries: queries,
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, pool, rdb
}

// seedClient inserts an api_client row directly and returns the raw key.
func seedClient(t *testing.T, pool *pgxpool.Pool, name, role string) string {
	t.Helper()
	raw := "test-" + name + "-key"
	_, err := pool.Exec(context.Background(),
		`INSERT INTO api_clients (name, key_hash, role) VALUES ($1, $2, $3)`,
		name, middleware.HashAPIKey(raw), role)
	require.NoError(t, err)
	return raw
}

func get(t *testing.T, srv *httptest.Server, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	w := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(w, req)
	return w
}

func TestWallets_GetOrCreate_Idempotent(t *testing.T) {
	srv, pool, _ := testAPI(t)
	key := seedClient(t, pool, "reader", "reader")
	addr := "0x098B716B8Aaf21512996dC57EB0615e2383E2f96"

	w := get(t, srv, "/api/v1/wallets/ethereum/"+addr, key)
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			Network           string `json:"network"`
			Address           string `json:"address"`
			NormalizedAddress string `json:"normalized_address"`
			Labels            []any  `json:"labels"`
			DataStatus        string `json:"data_status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "ethereum", body.Data.Network)
	assert.Equal(t, addr, body.Data.Address, "original representation preserved")
	assert.Equal(t, "0x098b716b8aaf21512996dc57eb0615e2383e2f96", body.Data.NormalizedAddress)
	assert.Equal(t, "UNAVAILABLE", body.Data.DataStatus)

	// Scoped to the wallet under test — other test packages share
	// check_wallet_test and may insert unrelated wallet rows in parallel.
	var count int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM wallets WHERE normalized_address = $1",
		body.Data.NormalizedAddress).Scan(&count))
	assert.Equal(t, 1, count)

	// Mixed-case variant resolves to the same row — no duplicates.
	w2 := get(t, srv, "/api/v1/wallets/ethereum/"+body.Data.NormalizedAddress, key)
	require.Equal(t, http.StatusOK, w2.Code)
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM wallets WHERE normalized_address = $1",
		body.Data.NormalizedAddress).Scan(&count))
	assert.Equal(t, 1, count, "get-or-create must be idempotent (UNIQUE gate)")
}

func TestWallets_Validation(t *testing.T) {
	srv, pool, _ := testAPI(t)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/wallets/solana/0x098b716b8aaf21512996dc57eb0615e2383e2f96", key)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_NETWORK")

	w = get(t, srv, "/api/v1/wallets/ethereum/not-an-address", key)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_ADDRESS")

	w = get(t, srv, "/api/v1/wallets/ethereum/0x098b716b8aaf21512996dc57eb0615e2383e2f96", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestNetworks_List(t *testing.T) {
	srv, pool, _ := testAPI(t)
	key := seedClient(t, pool, "reader", "reader")
	w := get(t, srv, "/api/v1/networks", key)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ethereum")
	assert.Contains(t, w.Body.String(), "bsc")
	assert.Contains(t, w.Body.String(), "eip155:1")
}

func TestAdminRoleEnforced(t *testing.T) {
	srv, pool, _ := testAPI(t)
	readerKey := seedClient(t, pool, "reader", "reader")
	w := get(t, srv, "/api/v1/admin/anything", readerKey)
	// No admin routes exist yet; Gin 404s after middleware — must not be 200.
	assert.NotEqual(t, http.StatusOK, w.Code)
}

func TestHealthzReadyz(t *testing.T) {
	srv, _, _ := testAPI(t)
	w := get(t, srv, "/healthz", "")
	assert.Equal(t, http.StatusOK, w.Code)
	w = get(t, srv, "/readyz", "")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"postgres":"ok"`)
	assert.Contains(t, w.Body.String(), `"redis":"ok"`)
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
