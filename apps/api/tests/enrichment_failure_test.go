package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"log/slog"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/enrichment"
	"checkwallet/internal/httpserver"
	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/testutil"
	"checkwallet/internal/wallet"
)

// Redis outage → endpoint still serves providers, marked bypassed.
func TestEnrichment_RedisOutageFailOpen(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	queries := db.New(pool)
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6399",
		DialTimeout: 50 * time.Millisecond, MaxRetries: 0})
	t.Cleanup(func() { dead.Close() })

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
	svc := enrichment.New(
		[]provider.Provider{&enrichFake{name: "goplus", enabled: true, result: fakeOK("goplus")}},
		pool, queries, dead, wallet.NewService(queries),
		slog.New(slog.DiscardHandler), enrichment.Config{
			PositiveTTL: 15 * time.Minute, NegativeTTL: time.Minute,
			RefreshCooldown: time.Minute, Deadline: 25 * time.Second, Retain: 25,
		})
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: dead,
		Cfg: &config.Config{HTTPAddr: ":0", RedisURL: "redis://127.0.0.1:6399",
			LogLevel: "error", RateLimitRequests: 1000, RateLimitAnon: 1000,
			RateLimitWindow: time.Minute},
		Logger:     slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		Resolver:   network.NewResolver(nets),
		Queries:    queries,
		Enrichment: svc,
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/wallets/ethereum/0x098B716B8Aaf21512996dC57EB0615e2383E2f96/enrichment", key)
	require.Equal(t, http.StatusOK, w.Code, "Redis outage must fail open, not 5xx")
	assert.Contains(t, w.Body.String(), `"cache":"bypassed"`,
		"outage must be visible to consumers")
	assert.Contains(t, w.Body.String(), `"status":"OK"`)
}

// Provider typed errors surface verbatim — never rewritten as clean.
func TestEnrichment_ProviderErrorsSurfaceTyped(t *testing.T) {
	rl := &enrichFake{name: "goplus", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrRateLimited,
			RetryAfter: 45 * time.Second, Msg: "429 too many"}}
	tout := &enrichFake{name: "publicaml", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrTimeout, Msg: "deadline exceeded"}}
	srv, pool, _ := enrichAPI(t, rl, tout)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/wallets/ethereum/0x098B716B8Aaf21512996dC57EB0615e2383E2f96/enrichment", key)
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			Status    string `json:"status"`
			Providers []struct {
				Source string `json:"source"`
				Status string `json:"status"`
				Error  struct {
					Code       string `json:"code"`
					RetryAfter int64  `json:"retry_after"`
				} `json:"error"`
			} `json:"providers"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "FAILED", body.Data.Status)
	byCode := map[string]int64{}
	byStatus := map[string]string{}
	for _, p := range body.Data.Providers {
		byStatus[p.Source] = p.Status
		byCode[p.Source] = p.Error.RetryAfter
	}
	assert.Equal(t, "RATE_LIMITED", byStatus["goplus"])
	assert.Equal(t, "TIMEOUT", byStatus["publicaml"])
	assert.Equal(t, int64(45), byCode["goplus"], "retry_after in seconds")

	// Negative replay: the 60s window serves the same typed failure —
	// never a clean replay.
	w = get(t, srv, "/api/v1/wallets/ethereum/0x098B716B8Aaf21512996dC57EB0615e2383E2f96/enrichment", key)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	for _, p := range body.Data.Providers {
		assert.NotEqual(t, "OK", p.Status, "negative cache must never replay clean")
	}
}
