package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/enrichment"
	"checkwallet/internal/httpserver"
	"checkwallet/internal/label"
	"checkwallet/internal/network"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/screening"
	"checkwallet/internal/testutil"
	"checkwallet/internal/wallet"
)

// screeningEnricher fakes enrichment.Service.Enrich without distorting
// its return contract (same *enrichment.Result shape).
type screeningEnricher struct {
	res   *enrichment.Result
	err   error
	delay time.Duration
}

func (f *screeningEnricher) Enrich(ctx context.Context, net network.Network,
	rawAddress, normalized string, opts enrichment.Options) (*enrichment.Result, error) {
	if f.delay > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(f.delay):
		}
	}
	return f.res, f.err
}

// screeningAPI boots the full router with the screening service wired:
// real sanctions/label services on the test pool; enrichment is the
// caller-supplied fake (or nil).
func screeningAPI(t *testing.T, en screening.Enricher) (*httptest.Server, *pgxpool.Pool) {
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
			ID: r.ID, Namespace: r.Namespace, ChainReference: r.ChainReference,
			Code: r.Code, Name: r.Name,
		})
	}
	resolver := network.NewResolver(nets)
	cfg := &config.Config{
		HTTPAddr: ":0", RedisURL: testutil.TestRedisURL(), LogLevel: "error",
		RateLimitRequests: 10000, RateLimitAnon: 10000, RateLimitWindow: time.Minute,
		SanctionsStaleAfter: 48 * time.Hour, LabelsStaleAfter: 168 * time.Hour,
	}
	sanctionsSvc := sanctions.NewService(queries, rdb, resolver, cfg.SanctionsStaleAfter)
	labelSvc := label.NewService(queries, rdb, cfg.LabelsStaleAfter)
	scr := screening.New(screening.Deps{
		Pool: pool, Queries: queries,
		Sanctions:  sanctionsSvc,
		Labels:     labelSvc,
		Enrichment: en,
		Wallets:    wallet.NewService(queries),
		Logger:     slog.New(slog.DiscardHandler),
	})
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg,
		Logger:   slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		Resolver: resolver, Queries: queries,
		Sanctions: sanctionsSvc, Labels: labelSvc,
		Screening: scr,
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, pool
}

const scrAddr = "0x098B716B8Aaf21512996dC57EB0615e2383E2f96"
const scrAddrNorm = "0x098b716b8aaf21512996dc57eb0615e2383e2f96"

func postScreening(t *testing.T, srv *httptest.Server, key, body, idemKey string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/screenings",
		strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	w := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(w, req)
	return w
}

func seedSanctionsSnapshot(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO sanctions_snapshots (source_id, version, checksum,
			entity_count, wallet_count, program_count, published_at)
		VALUES ((SELECT id FROM data_sources WHERE code='ofac_sdn'),
			'snap-test', 'c', 1, 1, 0, NOW())`)
	require.NoError(t, err)
}

func seedLabelSnapshot(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO data_source_snapshots (source_id, version, checksum,
			row_count, published_at)
		VALUES ((SELECT id FROM data_sources WHERE code='wallet_attribution'),
			'v-test', 'c', 2, NOW())`)
	require.NoError(t, err)
}

// seedSanctionedWallet registers a CHAIN_SPECIFIC OFAC listing for addr.
func seedSanctionedWallet(t *testing.T, pool *pgxpool.Pool, addr string) {
	t.Helper()
	var entityID int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO sanctions_entities (source_id, external_entity_id, primary_name)
		VALUES ((SELECT id FROM data_sources WHERE code='ofac_sdn'),
			'OFAC-TEST-1', 'Test Entity') RETURNING id`).Scan(&entityID))
	_, err := pool.Exec(context.Background(), `
		INSERT INTO sanctions_wallets (sanctions_entity_id, listed_address,
			normalized_address, network_id, identifier_type, match_scope)
		VALUES ($1, $2, $2, 1, 'ETH', 'CHAIN_SPECIFIC')`, entityID, addr)
	require.NoError(t, err)
}

func seedLabel(t *testing.T, pool *pgxpool.Pool, addr, fingerprint, cat,
	verif string, revoked bool) {
	t.Helper()
	var walletID int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO wallets (network_id, address, normalized_address, address_type)
		VALUES (1, $1, $1, 'evm')
		ON CONFLICT (network_id, normalized_address) DO UPDATE SET address = EXCLUDED.address
		RETURNING id`, addr).Scan(&walletID))
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wallet_labels (wallet_id, category_id, source_id, fingerprint,
			label, confidence, verification_status, revoked_at)
		VALUES ($1, (SELECT id FROM risk_categories WHERE code=$2),
			(SELECT id FROM data_sources WHERE code='wallet_attribution'),
			$3, 'seeded', 'high', $4,
			CASE WHEN $5 THEN NOW() ELSE NULL END)`,
		walletID, cat, fingerprint, verif, revoked)
	require.NoError(t, err)
}

func parseBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

func dataOf(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := parseBody(t, w)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "response must carry data envelope: %s", w.Body.String())
	return data
}

// --- Required test list (phase-04 amendment 11) ---

// 1. Concurrent identical POSTs → one screening; loser replays or 409.
func TestScreenings_ConcurrentIdentical(t *testing.T) {
	srv, pool := screeningAPI(t, nil)
	key := seedClient(t, pool, "scr-conc", "reader")
	body := fmt.Sprintf(`{"network":"ethereum","address":%q}`, scrAddr)

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	replays := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := postScreening(t, srv, key, body, "idem-conc-1")
			codes[i] = w.Code
			replays[i] = w.Header().Get("Idempotent-Replay") == "true"
		}(i)
	}
	wg.Wait()

	created, replayed, conflicts := 0, 0, 0
	for i := 0; i < n; i++ {
		switch codes[i] {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			require.True(t, replays[i], "200 without replay marker is wrong")
			replayed++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected status %d: concurrent POST", codes[i])
		}
	}
	assert.Equal(t, 1, created, "exactly one screening must be created")
	assert.Equal(t, n-1, replayed+conflicts,
		"losers either replay or get bounded 409 IN_FLIGHT")

	var rows int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM screening_results`).Scan(&rows))
	assert.Equal(t, 1, rows, "one screening result row total")
}

// 2. Same key + different payload → 409 IDEMPOTENCY_CONFLICT.
func TestScreenings_KeyPayloadMismatch(t *testing.T) {
	srv, pool := screeningAPI(t, nil)
	key := seedClient(t, pool, "scr-conflict", "reader")

	w1 := postScreening(t, srv, key,
		fmt.Sprintf(`{"network":"ethereum","address":%q}`, scrAddr), "idem-mismatch")
	require.Equal(t, http.StatusCreated, w1.Code)

	w2 := postScreening(t, srv, key,
		`{"network":"ethereum","address":"0x0000000000000000000000000000000000000abc"}`,
		"idem-mismatch")
	require.Equal(t, http.StatusConflict, w2.Code)
	assert.Equal(t, "IDEMPOTENCY_CONFLICT",
		parseBody(t, w2)["error"].(map[string]any)["code"])

	// Options differences are payload differences too.
	w3 := postScreening(t, srv, key,
		fmt.Sprintf(`{"network":"ethereum","address":%q,"options":{"include_labels":false}}`, scrAddr),
		"idem-mismatch")
	assert.Equal(t, http.StatusConflict, w3.Code)
}

// 3. No published snapshots → INSUFFICIENT_DATA + NULL score.
func TestScreenings_InsufficientData_NoSnapshots(t *testing.T) {
	srv, pool := screeningAPI(t, nil)
	key := seedClient(t, pool, "scr-insuf", "reader")

	w := postScreening(t, srv, key,
		fmt.Sprintf(`{"network":"ethereum","address":%q}`, scrAddr), "")
	require.Equal(t, http.StatusCreated, w.Code)
	data := dataOf(t, w)
	assert.Equal(t, "INSUFFICIENT_DATA", data["status"])
	assert.Nil(t, data["risk_score"], "risk_score must be NULL")
	assert.Equal(t, "UNKNOWN", data["risk_level"])
	assert.Equal(t, "/api/v1/screenings/"+data["screening_id"].(string),
		w.Header().Get("Location"))

	// Persisted NULL verified at the row.
	var valid bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT risk_score IS NOT NULL FROM screening_results WHERE id=$1`,
		data["screening_id"]).Scan(&valid))
	assert.False(t, valid)
}

// 4. Sanctioned wallet + include_sanctions=false → no flag + evaluated:false.
func TestScreenings_SanctionsExcluded(t *testing.T) {
	srv, pool := screeningAPI(t, nil)
	key := seedClient(t, pool, "scr-excl", "reader")
	seedSanctionsSnapshot(t, pool)
	seedSanctionedWallet(t, pool, scrAddrNorm)
	seedLabelSnapshot(t, pool)

	w := postScreening(t, srv, key,
		fmt.Sprintf(`{"network":"ethereum","address":%q,"options":{"include_sanctions":false}}`, scrAddr), "")
	require.Equal(t, http.StatusCreated, w.Code)
	data := dataOf(t, w)
	ds := data["direct_sanctions"].(map[string]any)
	assert.Equal(t, false, ds["evaluated"])
	flags, _ := data["scoring"].(map[string]any)["flags"].([]any)
	assert.NotContains(t, flags, "DIRECT_SANCTIONS")
	assert.Equal(t, "NOT_REQUESTED",
		data["data_completeness"].(map[string]any)["sanctions"].(map[string]any)["data_status"])
	// And the same wallet WITH sanctions evaluated flags the match.
	w2 := postScreening(t, srv, key,
		fmt.Sprintf(`{"network":"ethereum","address":%q}`, scrAddr), "")
	data2 := dataOf(t, w2)
	assert.Contains(t, data2["scoring"].(map[string]any)["flags"], "DIRECT_SANCTIONS")
	assert.Equal(t, float64(100), data2["risk_score"])
}

// 5. All providers failed + sanctions OK → PARTIAL + typed statuses.
func TestScreenings_AllProvidersFailed_Partial(t *testing.T) {
	srv, pool := screeningAPI(t, &screeningEnricher{res: &enrichment.Result{
		Status: enrichment.StatusFailed,
		Providers: []enrichment.ProviderResult{
			{Source: "goplus", Enabled: true, Status: "TIMEOUT",
				Error: &enrichment.ErrorInfo{Code: "timeout"}},
			{Source: "publicaml", Enabled: true, Status: "UPSTREAM_5XX",
				Error: &enrichment.ErrorInfo{Code: "upstream_5xx"}},
			{Source: "chainabuse", Enabled: false, Status: "DISABLED"},
		},
	}})
	key := seedClient(t, pool, "scr-prov", "reader")
	seedSanctionsSnapshot(t, pool)

	w := postScreening(t, srv, key,
		fmt.Sprintf(`{"network":"ethereum","address":%q}`, scrAddr), "")
	require.Equal(t, http.StatusCreated, w.Code)
	data := dataOf(t, w)
	assert.Equal(t, "PARTIAL", data["status"])
	provs := data["provider_status"].(map[string]any)["providers"].(map[string]any)
	assert.Equal(t, "TIMEOUT", provs["goplus"].(map[string]any)["status"])
	assert.Equal(t, "UPSTREAM_5XX", provs["publicaml"].(map[string]any)["status"])
	assert.Equal(t, "DISABLED", provs["chainabuse"].(map[string]any)["status"])
}

// 6. Disputed label → 0 contribution; revoked label → excluded.
func TestScreenings_DisputedRevokedLabels(t *testing.T) {
	srv, pool := screeningAPI(t, nil)
	key := seedClient(t, pool, "scr-labels", "reader")
	seedLabelSnapshot(t, pool)
	seedSanctionsSnapshot(t, pool)
	seedLabel(t, pool, scrAddrNorm, "fp-disputed", "SCAM", "disputed", false)
	seedLabel(t, pool, scrAddrNorm, "fp-revoked", "HACK", "verified", true)

	w := postScreening(t, srv, key,
		fmt.Sprintf(`{"network":"ethereum","address":%q}`, scrAddr), "")
	require.Equal(t, http.StatusCreated, w.Code)
	data := dataOf(t, w)
	labels := data["direct_labels"].(map[string]any)["items"].([]any)
	require.Len(t, labels, 1, "revoked label must not appear")
	assert.Equal(t, "disputed",
		labels[0].(map[string]any)["verification_status"])
	assert.Equal(t, float64(0), data["risk_score"],
		"disputed label scores 0")
}

// 7. Client disconnect mid-screening → terminal row still lands.
func TestScreenings_ClientDisconnect_StillLands(t *testing.T) {
	srv, pool := screeningAPI(t, &screeningEnricher{
		delay: 400 * time.Millisecond,
		res:   &enrichment.Result{Status: enrichment.StatusCompleted},
	})
	key := seedClient(t, pool, "scr-dc", "reader")
	seedSanctionsSnapshot(t, pool)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		srv.URL+"/api/v1/screenings",
		strings.NewReader(fmt.Sprintf(`{"network":"ethereum","address":%q}`, scrAddr)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key)

	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	resp, err := srv.Client().Do(req)
	if err == nil {
		resp.Body.Close()
	}
	// The request errored (disconnect) — but the detached pipeline must
	// still persist the terminal row.
	require.Eventually(t, func() bool {
		var n int
		_ = pool.QueryRow(context.Background(),
			`SELECT count(*) FROM screening_results`).Scan(&n)
		return n == 1
	}, 10*time.Second, 200*time.Millisecond,
		"terminal row must land despite client disconnect")
}

// 8. Replay returns stored result byte-identically + Idempotent-Replay.
func TestScreenings_ReplayByteIdentical(t *testing.T) {
	srv, pool := screeningAPI(t, nil)
	key := seedClient(t, pool, "scr-replay", "reader")
	body := fmt.Sprintf(`{"network":"ethereum","address":%q}`, scrAddr)

	w1 := postScreening(t, srv, key, body, "idem-replay")
	require.Equal(t, http.StatusCreated, w1.Code)

	w2 := postScreening(t, srv, key, body, "idem-replay")
	require.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "true", w2.Header().Get("Idempotent-Replay"))
	assert.Equal(t, w1.Body.Bytes(), w2.Body.Bytes(),
		"replay must return the stored body byte-identically")

	// GET returns the same bytes.
	id := dataOf(t, w1)["screening_id"].(string)
	w3 := get(t, srv, "/api/v1/screenings/"+id, key)
	require.Equal(t, http.StatusOK, w3.Code)
	assert.Equal(t, w1.Body.Bytes(), w3.Body.Bytes(),
		"GET must return the stored body byte-identically")
}

// 9. Malformed/oversized key → 400; absent key works.
func TestScreenings_KeyValidation(t *testing.T) {
	srv, pool := screeningAPI(t, nil)
	key := seedClient(t, pool, "scr-keyval", "reader")
	body := fmt.Sprintf(`{"network":"ethereum","address":%q}`, scrAddr)

	w := postScreening(t, srv, key, body, "has space in it")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "BAD_REQUEST",
		parseBody(t, w)["error"].(map[string]any)["code"])

	long := strings.Repeat("k", 256)
	w = postScreening(t, srv, key, body, long)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Absent → new screening each time (no idempotency).
	w1 := postScreening(t, srv, key, body, "")
	require.Equal(t, http.StatusCreated, w1.Code)
	w2 := postScreening(t, srv, key, body, "")
	require.Equal(t, http.StatusCreated, w2.Code)
	assert.NotEqual(t,
		dataOf(t, w1)["screening_id"], dataOf(t, w2)["screening_id"])

	// Whitespace-only → treated as absent.
	w3 := postScreening(t, srv, key, body, "   ")
	assert.Equal(t, http.StatusCreated, w3.Code)
}

// 10. GET unknown UUID → 404; anonymous → 401.
func TestScreenings_GetByID_NotFoundAndAuth(t *testing.T) {
	srv, pool := screeningAPI(t, nil)
	key := seedClient(t, pool, "scr-get", "reader")

	w := get(t, srv, "/api/v1/screenings/00000000-0000-0000-0000-000000000000", key)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "NOT_FOUND",
		parseBody(t, w)["error"].(map[string]any)["code"])

	w = get(t, srv, "/api/v1/screenings/00000000-0000-0000-0000-000000000000", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	w = postScreening(t, srv, "",
		fmt.Sprintf(`{"network":"ethereum","address":%q}`, scrAddr), "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// Extra: max_hops validation (reject, not clamp).
func TestScreenings_MaxHopsRejected(t *testing.T) {
	srv, pool := screeningAPI(t, nil)
	key := seedClient(t, pool, "scr-hops", "reader")
	for _, hops := range []string{"3", "-1"} {
		body := fmt.Sprintf(`{"network":"ethereum","address":%q,"options":{"max_hops":%s}}`,
			scrAddr, hops)
		w := postScreening(t, srv, key, body, "")
		assert.Equal(t, http.StatusBadRequest, w.Code, "max_hops=%s", hops)
	}
}

// Extra: invalid network/address codes + valid response shape.
func TestScreenings_Validation(t *testing.T) {
	srv, pool := screeningAPI(t, nil)
	key := seedClient(t, pool, "scr-val", "reader")

	w := postScreening(t, srv, key,
		`{"network":"solana","address":"0x0000000000000000000000000000000000000abc"}`, "")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "INVALID_NETWORK",
		parseBody(t, w)["error"].(map[string]any)["code"])

	w = postScreening(t, srv, key,
		`{"network":"ethereum","address":"not-an-address"}`, "")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "INVALID_ADDRESS",
		parseBody(t, w)["error"].(map[string]any)["code"])
}

// Extra: response carries all 14 §9.7 fields.
func TestScreenings_ResponseShape(t *testing.T) {
	srv, pool := screeningAPI(t, nil)
	key := seedClient(t, pool, "scr-shape", "reader")
	seedSanctionsSnapshot(t, pool)

	w := postScreening(t, srv, key,
		fmt.Sprintf(`{"network":"ethereum","address":%q}`, scrAddr), "")
	require.Equal(t, http.StatusCreated, w.Code)
	data := dataOf(t, w)
	for _, field := range []string{
		"screening_id", "network", "address", "status", "risk_score",
		"risk_level", "direct_sanctions", "direct_labels", "exposures",
		"scoring", "evidence", "provider_status", "data_completeness",
		"screened_at",
	} {
		assert.Contains(t, data, field, "§9.7 field %s missing", field)
	}
	scoring := data["scoring"].(map[string]any)
	assert.Equal(t, "v1.0.0", scoring["policy_version"])
	assert.Contains(t, data["exposures"].(map[string]any)["coverage"], "tracing")
}
