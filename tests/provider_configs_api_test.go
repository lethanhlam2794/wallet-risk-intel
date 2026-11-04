package tests

// Admin provider-config API tests (phase 4): role gating, 404 unknown
// code, masked api_key on the wire, whitelisted PUT, audit pagination,
// restart disclosures — api_key plaintext never leaves the surface.

import (
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
	"checkwallet/internal/providerconfig"
	"checkwallet/internal/testutil"
)

func testProviderConfigAPI(t *testing.T) (*httptest.Server, *pgxpool.Pool, *redis.Client) {
	t.Helper()
	pool := testutil.RequireTestPool(t)
	rdb := testutil.RequireTestRedis(t)
	queries := db.New(pool)
	resolver := testResolver(t, queries)
	cfg := &config.Config{RateLimitRequests: 10000, RateLimitAnon: 10000,
		RateLimitWindow: time.Minute, SanctionsStaleAfter: 48 * time.Hour}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := ingestion.NewService(pool, resolver, nil, log)
	pcSvc := providerconfig.NewService(pool, queries, nil, nil,
		map[string][]string{"ethereum": {"chainscan"}},
		[]string{"ethereum", "bsc"})
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: queries,
		Admin:           ingestion.NewAdminHandler(svc, queries, log),
		ProviderConfigs: pcSvc,
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, pool, rdb
}

func pcReq(t *testing.T, srv *httptest.Server, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	w := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(w, req)
	return w
}

func TestProviderConfigs_RoleGate(t *testing.T) {
	srv, pool, _ := testProviderConfigAPI(t)
	reader := seedClient(t, pool, "pc-reader", "reader")
	admin := seedClient(t, pool, "pc-admin", "admin")

	assert.Equal(t, http.StatusUnauthorized,
		pcReq(t, srv, http.MethodGet, "/api/v1/admin/provider-configs", "", "").Code)
	assert.Equal(t, http.StatusForbidden,
		pcReq(t, srv, http.MethodGet, "/api/v1/admin/provider-configs", reader, "").Code)
	assert.Equal(t, http.StatusOK,
		pcReq(t, srv, http.MethodGet, "/api/v1/admin/provider-configs", admin, "").Code)
}

func TestProviderConfigs_UnknownCode404(t *testing.T) {
	srv, pool, _ := testProviderConfigAPI(t)
	admin := seedClient(t, pool, "pc-admin2", "admin")
	assert.Equal(t, http.StatusNotFound,
		pcReq(t, srv, http.MethodGet, "/api/v1/admin/provider-configs/ghost", admin, "").Code)
	assert.Equal(t, http.StatusNotFound,
		pcReq(t, srv, http.MethodPut, "/api/v1/admin/provider-configs/ghost",
			admin, `{"status":"inactive"}`).Code)
}

func TestProviderConfigs_PutMaskedAndAudited(t *testing.T) {
	srv, pool, _ := testProviderConfigAPI(t)
	admin := seedClient(t, pool, "pc-admin3", "admin")
	key := "fake-test-api-key-4242"

	put := pcReq(t, srv, http.MethodPut, "/api/v1/admin/provider-configs/covalent",
		admin, `{"api_key":"`+key+`","enabled_chains":["ethereum"]}`)
	require.Equal(t, http.StatusOK, put.Code)
	body := put.Body.String()
	assert.NotContains(t, body, key, "PUT response must mask the key")
	assert.Contains(t, body, `"last4":"4242"`)
	assert.Contains(t, body, `"requires_restart"`)
	assert.Contains(t, body, `"warnings"`)

	get := pcReq(t, srv, http.MethodGet, "/api/v1/admin/provider-configs/covalent", admin, "")
	require.Equal(t, http.StatusOK, get.Code)
	assert.NotContains(t, get.Body.String(), key)
	assert.Contains(t, get.Body.String(), `"set":true`)
	assert.Contains(t, get.Body.String(), `"pc-admin3"`, "updated_by reflects the actor")

	// Audit rows exist, masked, and the endpoint pages (rows written in
	// one tx share changed_at — uuid tie-break makes page-1 order
	// nondeterministic, so assert fields on the full page).
	changes := pcReq(t, srv, http.MethodGet,
		"/api/v1/admin/provider-configs/covalent/changes?limit=10", admin, "")
	require.Equal(t, http.StatusOK, changes.Code)
	assert.NotContains(t, changes.Body.String(), key)
	for _, f := range []string{`"field":"api_key"`, `"field":"enabled_chains"`} {
		assert.Contains(t, changes.Body.String(), f)
	}
	page := pcReq(t, srv, http.MethodGet,
		"/api/v1/admin/provider-configs/covalent/changes?limit=1", admin, "")
	require.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), `"next_cursor"`)
}

func TestProviderConfigs_ValidationAndWhitelist(t *testing.T) {
	srv, pool, _ := testProviderConfigAPI(t)
	admin := seedClient(t, pool, "pc-admin4", "admin")
	base := "/api/v1/admin/provider-configs/covalent"

	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodPut, base,
		admin, `{"bogus_field":1}`).Code, "unknown JSON key rejected")
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodPut, base,
		admin, `{"status":"sideways"}`).Code)
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodPut, base,
		admin, `{"enabled_chains":["solana"]}`).Code)
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodPut, base,
		admin, `{"api_key":"enc:v1:AAAA:BBBB"}`).Code)

	badURL := pcReq(t, srv, http.MethodPut, base,
		admin, `{"base_url":"https://evil.example.com"}`)
	assert.Equal(t, http.StatusBadRequest, badURL.Code)
	assert.Contains(t, badURL.Body.String(), "allowed_hosts")
}

func TestProviderConfigs_LastActiveWarning(t *testing.T) {
	srv, pool, _ := testProviderConfigAPI(t)
	admin := seedClient(t, pool, "pc-admin5", "admin")
	w := pcReq(t, srv, http.MethodPut,
		"/api/v1/admin/provider-configs/chainscan", admin, `{"status":"inactive"}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "last_active_member")
	assert.Contains(t, w.Body.String(), "ethereum")

	// Row materialized; GET shows inactive + effective_active false.
	g := pcReq(t, srv, http.MethodGet,
		"/api/v1/admin/provider-configs/chainscan", admin, "")
	assert.Contains(t, g.Body.String(), `"status":"inactive"`)
	assert.Contains(t, g.Body.String(), `"effective_active":false`)
}
