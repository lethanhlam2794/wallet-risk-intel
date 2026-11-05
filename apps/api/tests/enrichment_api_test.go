package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
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

// enrichFake is a scripted provider for API-level tests.
type enrichFake struct {
	name    string
	enabled bool
	calls   int64
	result  *provider.LookupResult
	err     error
}

func (f *enrichFake) Name() string  { return f.name }
func (f *enrichFake) Enabled() bool { return f.enabled }

func (f *enrichFake) LookupAddress(ctx context.Context, net network.Network,
	normalized string) (*provider.LookupResult, error) {
	atomic.AddInt64(&f.calls, 1)
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func fakeOK(source string) *provider.LookupResult {
	score := 100.0
	return &provider.LookupResult{
		Source:         source,
		Network:        "ethereum",
		Address:        "0x098b716b8aaf21512996dc57eb0615e2383e2f96",
		RequestedChain: "ETH",
		Observations: []provider.Observation{{
			Network: "ethereum", Address: "0x098b716b8aaf21512996dc57eb0615e2383e2f96",
			Category: "SANCTIONED", Label: "sanction", Entity: "Lazarus Group",
			Source: source, Confidence: "unknown", VerificationStatus: "reported",
			ObservedAt: time.Now(), Evidence: json.RawMessage(`{"category":"sanction"}`),
		}},
		Signals:       map[string]any{"label": "Lazarus Group"},
		ProviderScore: &score,
		Exposures: []provider.Exposure{{
			Counterparty: "0x35fb6f6db4fb05e6a4ce86f2c93691425626d4b1",
			Category:     "sanction", Hops: 1, Direction: "outbound", Score: &score,
		}},
		Raw:       json.RawMessage(`{"entities":[{}]}`),
		FetchedAt: time.Now(),
	}
}

// enrichAPI boots the router with the enrichment service wired to fakes.
func enrichAPI(t *testing.T, provs ...provider.Provider) (*httptest.Server, *pgxpool.Pool, *redis.Client) {
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
	resolver := network.NewResolver(nets)

	cfg := &config.Config{
		HTTPAddr: ":0", RedisURL: testutil.TestRedisURL(), LogLevel: "error",
		RateLimitRequests: 1000, RateLimitAnon: 1000, RateLimitWindow: time.Minute,
	}
	svc := enrichment.New(provs, pool, queries, rdb, wallet.NewService(queries),
		slog.New(slog.DiscardHandler), enrichment.Config{
			PositiveTTL: 15 * time.Minute, NegativeTTL: time.Minute,
			RefreshCooldown: time.Minute, Deadline: 25 * time.Second, Retain: 25,
		})
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg,
		Logger:   slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		Resolver: resolver, Queries: queries,
		Enrichment: svc,
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, pool, rdb
}

const enrichAddr = "0x098B716B8Aaf21512996dC57EB0615e2383E2f96"

func TestEnrichment_HappyPathShape(t *testing.T) {
	ok := &enrichFake{name: "goplus", enabled: true, result: fakeOK("goplus")}
	srv, pool, _ := enrichAPI(t, ok)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/wallets/ethereum/"+enrichAddr+"/enrichment", key)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Data struct {
			Network           string `json:"network"`
			Address           string `json:"address"`
			NormalizedAddress string `json:"normalized_address"`
			Status            string `json:"status"`
			Providers         []struct {
				Source        string         `json:"source"`
				Enabled       bool           `json:"enabled"`
				Status        string         `json:"status"`
				Cached        bool           `json:"cached"`
				Signals       map[string]any `json:"signals"`
				ProviderScore *float64       `json:"provider_score"`
				Observations  []struct {
					Category           string `json:"category"`
					VerificationStatus string `json:"verification_status"`
				} `json:"observations"`
				Exposures []struct {
					Counterparty string `json:"counterparty"`
					Hops         int    `json:"hops"`
				} `json:"exposures"`
				Persisted bool `json:"persisted"`
			} `json:"providers"`
			EnrichedAt string `json:"enriched_at"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "COMPLETED", body.Data.Status)
	assert.Equal(t, "ethereum", body.Data.Network)
	assert.Equal(t, "0x098b716b8aaf21512996dc57eb0615e2383e2f96", body.Data.NormalizedAddress)
	require.Len(t, body.Data.Providers, 1)
	p := body.Data.Providers[0]
	assert.Equal(t, "goplus", p.Source)
	assert.Equal(t, "OK", p.Status)
	assert.True(t, p.Persisted)
	require.NotNil(t, p.ProviderScore)
	assert.Equal(t, 100.0, *p.ProviderScore)
	require.Len(t, p.Observations, 1)
	assert.Equal(t, "SANCTIONED", p.Observations[0].Category)
	assert.Equal(t, "reported", p.Observations[0].VerificationStatus)
	require.Len(t, p.Exposures, 1)
	assert.Equal(t, 1, p.Exposures[0].Hops)

	// Second call → cached:true, provider not re-called.
	w = get(t, srv, "/api/v1/wallets/ethereum/"+enrichAddr+"/enrichment", key)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.True(t, body.Data.Providers[0].Cached)
	assert.Equal(t, int64(1), atomic.LoadInt64(&ok.calls))
}

func TestEnrichment_RefreshRefetches(t *testing.T) {
	ok := &enrichFake{name: "goplus", enabled: true, result: fakeOK("goplus")}
	srv, pool, _ := enrichAPI(t, ok)
	key := seedClient(t, pool, "reader", "reader")

	path := "/api/v1/wallets/ethereum/" + enrichAddr + "/enrichment"
	require.Equal(t, http.StatusOK, get(t, srv, path, key).Code)
	w := get(t, srv, path+"?refresh=1", key)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, int64(2), atomic.LoadInt64(&ok.calls), "refresh=1 must refetch")
}

func TestEnrichment_ValidationAndProviderFilter(t *testing.T) {
	ok := &enrichFake{name: "goplus", enabled: true, result: fakeOK("goplus")}
	srv, pool, _ := enrichAPI(t, ok)
	key := seedClient(t, pool, "reader", "reader")
	base := "/api/v1/wallets/"

	w := get(t, srv, base+"solana/"+enrichAddr+"/enrichment", key)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_NETWORK")

	w = get(t, srv, base+"ethereum/not-an-address/enrichment", key)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_ADDRESS")

	w = get(t, srv, base+"ethereum/"+enrichAddr+"/enrichment?providers=nosuch", key)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = get(t, srv, base+"ethereum/"+enrichAddr+"/enrichment?refresh=notabool", key)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = get(t, srv, base+"ethereum/"+enrichAddr+"/enrichment?providers=goplus", key)
	assert.Equal(t, http.StatusOK, w.Code)

	// Auth still enforced.
	w = get(t, srv, base+"ethereum/"+enrichAddr+"/enrichment", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestEnrichment_PartialWhenProviderFails(t *testing.T) {
	ok := &enrichFake{name: "goplus", enabled: true, result: fakeOK("goplus")}
	bad := &enrichFake{name: "publicaml", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrUpstream5xx, HTTPStatus: 502, Msg: "bad gateway"}}
	srv, pool, _ := enrichAPI(t, ok, bad)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/wallets/ethereum/"+enrichAddr+"/enrichment", key)
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			Status    string `json:"status"`
			Providers []struct {
				Source string `json:"source"`
				Status string `json:"status"`
				Error  *struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"providers"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "PARTIAL", body.Data.Status)
	bySource := map[string]string{}
	for _, p := range body.Data.Providers {
		bySource[p.Source] = p.Status
	}
	assert.Equal(t, "OK", bySource["goplus"])
	assert.Equal(t, "UPSTREAM_5XX", bySource["publicaml"],
		"provider failure must stay visible — never a clean result")
}

func TestEnrichment_DisabledProviderZeroTraffic(t *testing.T) {
	disabled := &enrichFake{name: "chainabuse", enabled: false}
	srv, pool, _ := enrichAPI(t, disabled)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/wallets/ethereum/"+enrichAddr+"/enrichment", key)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "INSUFFICIENT_DATA")
	assert.Contains(t, w.Body.String(), "DISABLED")
	assert.Equal(t, int64(0), atomic.LoadInt64(&disabled.calls))
}

func TestEnrichment_PersistsRawRows(t *testing.T) {
	ok := &enrichFake{name: "goplus", enabled: true, result: fakeOK("goplus")}
	srv, pool, _ := enrichAPI(t, ok)
	key := seedClient(t, pool, "reader", "reader")

	w := get(t, srv, "/api/v1/wallets/ethereum/"+enrichAddr+"/enrichment", key)
	require.Equal(t, http.StatusOK, w.Code)

	var status string
	var payload []byte
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT po.status, po.payload FROM provider_observations po
		JOIN data_sources ds ON ds.id = po.source_id
		WHERE ds.code = 'goplus'`).Scan(&status, &payload))
	assert.Equal(t, "OK", status)
	assert.JSONEq(t, `{"entities":[{}]}`, string(payload))
}
