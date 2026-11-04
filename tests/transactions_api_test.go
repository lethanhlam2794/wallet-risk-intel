package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/httpserver"
	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/testutil"
	"checkwallet/internal/tracing"
	"checkwallet/internal/transaction"
	"checkwallet/internal/wallet"
)

const txTestDSN = "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_txapi_test?sslmode=disable"

var (
	txRoot = "0x00000000000000000000000000000000000000a1"
	txCP   = "0x00000000000000000000000000000000000000a2"
)

type apiStub struct {
	name    string
	enabled bool
	head    int64
	calls   atomic.Int64
	mu      sync.Mutex
	pages   map[string][]provider.Transfer
}

func (s *apiStub) Name() string                 { return s.name }
func (s *apiStub) Enabled(network.Network) bool { return s.enabled }
func (s *apiStub) HeadBlock(context.Context, network.Network) (int64, error) {
	return s.head, nil
}
func (s *apiStub) LookupTransfers(_ context.Context, _ network.Network,
	addr string, _ provider.TransferLookupOpts) (*provider.TransferPage, error) {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return &provider.TransferPage{Transfers: s.pages[addr], UpstreamCalls: 1}, nil
}

// testTxAPI boots the router with stubbed TxProvider (Deps injection) over
// the dedicated API test DB.
func testTxAPI(t *testing.T, stub *apiStub) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.RequireTestPoolDSN(t, txTestDSN)
	rdb := testutil.RequireTestRedis(t)
	queries := db.New(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := queries.ListActiveNetworks(ctx)
	require.NoError(t, err)
	nets := make([]network.Network, 0, len(rows))
	for _, r := range rows {
		nets = append(nets, network.Network{
			ID: r.ID, Namespace: r.Namespace, ChainReference: r.ChainReference,
			Code: r.Code, Name: r.Name,
		})
	}
	resolver := network.NewResolver(nets)
	cfg := &config.Config{
		HTTPAddr: ":0", RedisURL: testutil.TestRedisURL(), LogLevel: "error",
		RateLimitRequests: 1000, RateLimitAnon: 1000, RateLimitWindow: time.Minute,
	}
	adapters := map[string]provider.TxProvider{}
	for _, n := range nets {
		if n.Code == "ethereum" {
			adapters[n.Code] = stub
		}
	}
	wallets := wallet.NewService(queries)
	txSvc := transaction.New(pool, queries, adapters, wallets, transaction.Config{
		FreshTTL: time.Minute, NegativeTTL: time.Minute,
		StaleAfter: 15 * time.Minute, PageSize: 100,
		Budget: map[string]int{stub.Name(): 64},
	}, slog.New(slog.DiscardHandler))
	sanSvc := sanctions.NewService(queries, rdb, resolver, 48*time.Hour)
	tracingSvc := tracing.New(queries, txSvc, sanSvc, resolver, tracing.Config{
		MaxNodes: 10, MaxEdges: 100, Deadline: 30 * time.Second,
	})
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg,
		Logger:   slog.New(slog.DiscardHandler),
		Resolver: resolver, Queries: queries,
		Transactions: txSvc, Tracing: tracingSvc,
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, pool
}

func apiKey(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	raw := "testreader-" + t.Name()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO api_clients (name, key_hash, role)
		 VALUES ($1, $2, 'reader')`,
		t.Name(), middleware.HashAPIKey(raw))
	require.NoError(t, err)
	return raw
}

func getJSON(t *testing.T, srv *httptest.Server, key, path string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("X-API-Key", key)
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// Test 11/21b: /transactions E2E — items + coverage envelope, cursor,
// limit bound, self-row once, INVALID_CURSOR.
func TestTransactionsAPI(t *testing.T) {
	stub := &apiStub{name: "chainscan", enabled: true, head: 100,
		pages: map[string][]provider.Transfer{
			txRoot: {
				{TxHash: "0xaa1", Stream: "from", Type: provider.TransferKindNative,
					From: txCP, To: txRoot, AmountRaw: "55",
					BlockNumber: 90, BlockTimestamp: time.Unix(1700000000, 0).UTC(),
					Confirmations: 10},
				{TxHash: "0xab2", Stream: "from", Type: provider.TransferKindNative,
					From: txRoot, To: txRoot, AmountRaw: "3",
					BlockNumber: 91, BlockTimestamp: time.Unix(1700000012, 0).UTC(),
					Confirmations: 9},
			},
		}}
	srv, pool := testTxAPI(t, stub)
	key := apiKey(t, pool)

	code, body := getJSON(t, srv, key,
		"/api/v1/wallets/ethereum/"+txRoot+"/transactions")
	require.Equal(t, http.StatusOK, code, "%v", body)
	data := body["data"].(map[string]any)
	assert.Equal(t, "fresh", data["index_status"])
	assert.Equal(t, "native+token", data["index_scope"])
	items := data["items"].([]any)
	require.Len(t, items, 2)
	selfRow := items[0].(map[string]any)
	assert.Equal(t, "self", selfRow["direction"], "W→W once as self")
	inRow := items[1].(map[string]any)
	assert.Equal(t, "in", inRow["direction"])
	assert.Equal(t, "55", inRow["amount_raw"])
	assert.Contains(t, data, "provider_status")
	assert.NotNil(t, data["indexed_until_block"])

	// direction=self isolates the self-row.
	_, body = getJSON(t, srv, key,
		"/api/v1/wallets/ethereum/"+txRoot+"/transactions?direction=self")
	items = body["data"].(map[string]any)["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "self", items[0].(map[string]any)["direction"])

	// Invalid cursor → 400 INVALID_CURSOR.
	code, body = getJSON(t, srv, key,
		"/api/v1/wallets/ethereum/"+txRoot+"/transactions?cursor=not-base64!!")
	assert.Equal(t, http.StatusBadRequest, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "INVALID_CURSOR", errObj["code"])

	// max_hops validation on /exposures: 0 and 6 → 400 (phase 7: 1..5).
	for _, h := range []string{"0", "6"} {
		code, _ = getJSON(t, srv, key,
			fmt.Sprintf("/api/v1/wallets/ethereum/%s/exposures?max_hops=%s", txRoot, h))
		assert.Equal(t, http.StatusBadRequest, code, "max_hops=%s must 400", h)
	}
	// Valid boundary values reach the service layer.
	for _, h := range []string{"1", "5"} {
		code, _ = getJSON(t, srv, key,
			fmt.Sprintf("/api/v1/wallets/ethereum/%s/exposures?max_hops=%s", txRoot, h))
		assert.Equal(t, http.StatusOK, code, "max_hops=%s must 200", h)
	}
}

// Test 4/12: /exposures E2E — a labeled counterparty yields
// direct_counterparty with evidence; coverage envelope present;
// relationship vocabulary never claims ownership.
func TestExposuresAPI(t *testing.T) {
	stub := &apiStub{name: "chainscan", enabled: true, head: 100,
		pages: map[string][]provider.Transfer{
			txRoot: {
				{TxHash: "0xcc1", Stream: "from", Type: provider.TransferKindNative,
					From: txCP, To: txRoot, AmountRaw: "9",
					BlockNumber: 90, BlockTimestamp: time.Unix(1700000000, 0).UTC(),
					Confirmations: 10},
			},
		}}
	srv, pool := testTxAPI(t, stub)
	key := apiKey(t, pool)
	seedLabelFor(t, pool, txCP, "SCAM")

	code, body := getJSON(t, srv, key,
		"/api/v1/wallets/ethereum/"+txRoot+"/exposures?max_hops=2")
	require.Equal(t, http.StatusOK, code, "%v", body)
	data := body["data"].(map[string]any)
	assert.Equal(t, "bounded_bfs", data["tracing_method"])
	items := data["items"].([]any)
	require.Len(t, items, 1)
	it := items[0].(map[string]any)
	assert.Equal(t, "direct_counterparty", it["relationship"])
	assert.Equal(t, "SCAM", it["category"])
	paths := it["paths"].([]any)
	require.Len(t, paths, 1)
	assert.Equal(t, false, paths[0].(map[string]any)["path_verified"])

	cov := data["coverage"].(map[string]any)
	assert.Contains(t, cov, "root")
	assert.Contains(t, cov, "provider_status")
	root := cov["root"].(map[string]any)
	assert.Equal(t, "fresh", root["index_status"])

	// No ownership vocabulary anywhere in the payload.
	for _, banned := range []string{"owner", "ownership", "controls", "same_wallet"} {
		assert.NotContains(t, fmt.Sprintf("%v", body), banned)
	}
}

// §6-20 + cross-net label E2E: seeded 3-hop graph at max_hops=3 —
// exposure_policy_version on the envelope, per-hop coverage maps,
// foreign-network label as kind:"label" signal with network +
// listed_address provenance; solely-foreign item is
// category="cross_chain_listing". BFS never crosses networks — the
// bsc row is label evidence only.
func TestExposuresAPI_Advanced(t *testing.T) {
	v1 := "0x00000000000000000000000000000000000000b1"
	v2 := "0x00000000000000000000000000000000000000b2"
	cpX := "0x00000000000000000000000000000000000000c9"
	cpXorig := "0x00000000000000000000000000000000000000C9"
	stub := &apiStub{name: "chainscan", enabled: true, head: 100,
		pages: map[string][]provider.Transfer{
			txRoot: {
				{TxHash: "0xd1", Stream: "from", Type: provider.TransferKindNative,
					From: v1, To: txRoot, AmountRaw: "7",
					BlockNumber: 90, BlockTimestamp: time.Unix(1700000000, 0).UTC(),
					Confirmations: 10},
			},
			v1: {
				{TxHash: "0xd2", Stream: "from", Type: provider.TransferKindNative,
					From: v2, To: v1, AmountRaw: "7",
					BlockNumber: 95, BlockTimestamp: time.Unix(1700000005, 0).UTC(),
					Confirmations: 5},
			},
			v2: {
				{TxHash: "0xd3", Stream: "from", Type: provider.TransferKindNative,
					From: cpX, To: v2, AmountRaw: "7",
					BlockNumber: 100, BlockTimestamp: time.Unix(1700000010, 0).UTC(),
					Confirmations: 1},
			},
		}}
	srv, pool := testTxAPI(t, stub)
	key := apiKey(t, pool)
	// cpX's only label lives on bsc, with a checksum-case original.
	seedForeignLabel(t, pool, "bsc", cpX, cpXorig, "SCAM")

	code, body := getJSON(t, srv, key,
		"/api/v1/wallets/ethereum/"+txRoot+"/exposures?max_hops=3")
	require.Equal(t, http.StatusOK, code, "%v", body)
	data := body["data"].(map[string]any)
	assert.Equal(t, "bounded_bfs", data["tracing_method"])
	assert.Equal(t, "exposure-v1", data["exposure_policy_version"],
		"policy version is always on the envelope")
	assert.NotContains(t, fmt.Sprintf("%v", body), "risk_score")

	items := data["items"].([]any)
	require.Len(t, items, 1, "only cpX matches; v1/v2 are clean intermediaries")
	it := items[0].(map[string]any)
	assert.Equal(t, cpX, it["counterparty"])
	assert.Equal(t, "indirect_hop", it["relationship"])
	assert.Equal(t, float64(3), it["hops"])
	assert.Equal(t, "cross_chain_listing", it["category"],
		"solely-foreign item never claims a local category")
	assert.Equal(t, float64(12), it["exposure_score"],
		"70×reported(.7)×high(1.0)×hop-3(.25) = 12.25 → 12 per-term")

	sigs := it["matched_signals"].([]any)
	require.Len(t, sigs, 1)
	sig := sigs[0].(map[string]any)
	assert.Equal(t, "label", sig["kind"])
	assert.Equal(t, "bsc", sig["network"])
	assert.Equal(t, cpXorig, sig["listed_address"],
		"original representation, not normalized")
	assert.Equal(t, "reported", sig["verification_status"])

	paths := it["paths"].([]any)
	require.Len(t, paths, 1)
	p := paths[0].(map[string]any)
	assert.Equal(t, float64(3), p["hop_count"])
	assert.Equal(t, true, p["temporal_order_valid"])
	assert.Len(t, p["edges"].([]any), 3)

	cov := data["coverage"].(map[string]any)
	assert.Equal(t, float64(2), cov["counterparties_total"])
	assert.Equal(t, float64(2), cov["nodes_examined"])
	assert.Equal(t, float64(2), cov["positions_emitted"])
	assert.Equal(t, float64(3), cov["max_depth_reached"])
	assert.Equal(t, map[string]any{"2": float64(1), "3": float64(1)},
		cov["frontier_by_hop"])
	assert.Equal(t, map[string]any{"2": float64(1), "3": float64(1)},
		cov["nodes_examined_by_hop"])
	assert.Equal(t, map[string]any{"2": float64(1), "3": float64(1)},
		cov["frontier_emitted_by_hop"])
	assert.Equal(t, map[string]any{"2": float64(0), "3": float64(0)},
		cov["nodes_remaining_by_hop"])
}

// Capture regeneration (manual): REGEN_CAPTURES=1 go test ./tests/
// -run TestRegenCaptures — rewrites the disabled-lifecycle captures
// under docs/feature-maps/assets/ through the real engine+DB.
func TestRegenCaptures(t *testing.T) {
	if os.Getenv("REGEN_CAPTURES") == "" {
		t.Skip("set REGEN_CAPTURES=1 to regenerate")
	}
	stub := &apiStub{name: "chainscan", enabled: false}
	srv, pool := testTxAPI(t, stub)
	key := apiKey(t, pool)

	dump := func(path, file string) {
		code, body := getJSON(t, srv, key, path)
		require.Equal(t, http.StatusOK, code, "%v", body)
		raw, err := json.MarshalIndent(body, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(
			"../docs/feature-maps/assets/"+file, append(raw, '\n'), 0o644))
	}
	dump("/api/v1/wallets/ethereum/"+txRoot+"/transactions",
		"live-transactions-disabled.json")
	dump("/api/v1/wallets/ethereum/"+txRoot+"/exposures",
		"live-exposures-disabled.json")
}

// seedForeignLabel writes the label on a foreign network's wallet row —
// original representation preserved so listed_address ≠ normalized.
func seedForeignLabel(t *testing.T, pool *pgxpool.Pool, netCode,
	normalized, original, cat string) {
	t.Helper()
	var walletID int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO wallets (network_id, address, normalized_address, address_type)
		VALUES ((SELECT id FROM blockchain_networks WHERE code=$1), $2, $3, 'evm')
		ON CONFLICT (network_id, normalized_address)
		DO UPDATE SET address = EXCLUDED.address
		RETURNING id`, netCode, original, normalized).Scan(&walletID))
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wallet_labels (wallet_id, category_id, source_id, fingerprint,
			label, confidence, verification_status)
		VALUES ($1, (SELECT id FROM risk_categories WHERE code=$2),
			(SELECT id FROM data_sources WHERE code='wallet_attribution'),
			$3, 'seeded-foreign', 'high', 'reported')`,
		walletID, cat, "fp-"+netCode+"-"+normalized)
	require.NoError(t, err)
}

func seedLabelFor(t *testing.T, pool *pgxpool.Pool, addr, cat string) {
	t.Helper()
	var walletID int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO wallets (network_id, address, normalized_address, address_type)
		VALUES ((SELECT id FROM blockchain_networks WHERE code='ethereum'),
			$1, $1, 'evm')
		ON CONFLICT (network_id, normalized_address)
		DO UPDATE SET address = EXCLUDED.address
		RETURNING id`, addr).Scan(&walletID))
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wallet_labels (wallet_id, category_id, source_id, fingerprint,
			label, confidence, verification_status)
		VALUES ($1, (SELECT id FROM risk_categories WHERE code=$2),
			(SELECT id FROM data_sources WHERE code='wallet_attribution'),
			$3, 'seeded', 'high', 'reported')`,
		walletID, cat, "fp-"+addr)
	require.NoError(t, err)
}

// Review M6/m2/m3 regressions: from_timestamp applies (spec name),
// limit=0 → 400, empty exposures emit items:[] — never null.
func TestTransactionsAPI_ContractRegressions(t *testing.T) {
	stub := &apiStub{name: "chainscan", enabled: true, head: 100,
		pages: map[string][]provider.Transfer{
			txRoot: {
				{TxHash: "0xold", Stream: "from", Type: provider.TransferKindNative,
					From: txCP, To: txRoot, AmountRaw: "1",
					BlockNumber: 90, BlockTimestamp: time.Unix(1700000000, 0).UTC(),
					Confirmations: 10},
				{TxHash: "0xnew", Stream: "from", Type: provider.TransferKindNative,
					From: txCP, To: txRoot, AmountRaw: "2",
					BlockNumber: 91, BlockTimestamp: time.Unix(1700000100, 0).UTC(),
					Confirmations: 9},
			},
		}}
	srv, pool := testTxAPI(t, stub)
	key := apiKey(t, pool)

	// from_timestamp (spec name) must filter — the newer row only.
	code, body := getJSON(t, srv, key,
		"/api/v1/wallets/ethereum/"+txRoot+"/transactions?from_timestamp=2023-11-14T22:13:40Z")
	require.Equal(t, http.StatusOK, code, "%v", body)
	items := body["data"].(map[string]any)["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "0xnew", items[0].(map[string]any)["tx_hash"])

	// limit=0 → 400 on /exposures.
	code, _ = getJSON(t, srv, key,
		"/api/v1/wallets/ethereum/"+txRoot+"/exposures?limit=0")
	assert.Equal(t, http.StatusBadRequest, code)

	// Empty exposure set → items must be [] on the wire, not null.
	stub2 := &apiStub{name: "chainscan", enabled: true, head: 100,
		pages: map[string][]provider.Transfer{
			"0x00000000000000000000000000000000000000b9": {},
		}}
	srv2, pool2 := testTxAPI(t, stub2)
	key2 := apiKey(t, pool2)
	code, body = getJSON(t, srv2, key2,
		"/api/v1/wallets/ethereum/0x00000000000000000000000000000000000000b9/exposures")
	require.Equal(t, http.StatusOK, code, "%v", body)
	raw, _ := json.Marshal(body["data"])
	assert.Contains(t, string(raw), `"items":[]`)
}
