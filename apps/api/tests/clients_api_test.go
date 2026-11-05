package tests

// Admin api-clients API tests (admin-api-clients phase 2): role gating,
// key issuance disclosed once with no-store, view enumeration without
// key material, whitelisted PATCH with lockout guards, keyset
// pagination, env_managed disclosure.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/apiclient"
	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/httpserver"
	"checkwallet/internal/ingestion"
	"checkwallet/internal/middleware"
	"checkwallet/internal/testutil"
)

func testClientsAPI(t *testing.T, bootHashes []string) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.RequireTestPool(t)
	rdb := testutil.RequireTestRedis(t)
	queries := db.New(pool)
	resolver := testResolver(t, queries)
	cfg := &config.Config{RateLimitRequests: 10000, RateLimitAnon: 10000,
		RateLimitWindow: time.Minute, SanctionsStaleAfter: 48 * time.Hour}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := ingestion.NewService(pool, resolver, nil, log)
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: queries,
		Admin:   ingestion.NewAdminHandler(svc, queries, log),
		Clients: apiclient.New(pool, queries, bootHashes, log),
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, pool
}

// clientID resolves a seeded client's id by name for PATCH paths.
func clientID(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT id FROM api_clients WHERE name = $1", name).Scan(&id))
	return id
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// clientViewKeys enumerates the wire fields allowed on a client view —
// key_hash / api_key must never appear.
var clientViewKeys = map[string]bool{
	"id": true, "name": true, "role": true, "is_active": true,
	"created_at": true, "last_seen_at": true,
}

func decodeData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Data
}

func TestClients_RoleGate(t *testing.T) {
	srv, pool := testClientsAPI(t, nil)
	reader := seedClient(t, pool, "cl-reader", "reader")
	admin := seedClient(t, pool, "cl-admin", "admin")

	assert.Equal(t, http.StatusUnauthorized,
		pcReq(t, srv, http.MethodGet, "/api/v1/admin/clients", "", "").Code)
	assert.Equal(t, http.StatusForbidden,
		pcReq(t, srv, http.MethodGet, "/api/v1/admin/clients", reader, "").Code)
	assert.Equal(t, http.StatusOK,
		pcReq(t, srv, http.MethodGet, "/api/v1/admin/clients", admin, "").Code)
}

func TestClients_RoutesExistWithoutAdminDep(t *testing.T) {
	// Per-dep route guard: Clients alone must still mount the admin group.
	pool := testutil.RequireTestPool(t)
	rdb := testutil.RequireTestRedis(t)
	queries := db.New(pool)
	resolver := testResolver(t, queries)
	cfg := &config.Config{RateLimitRequests: 10000, RateLimitAnon: 10000,
		RateLimitWindow: time.Minute}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: queries,
		Clients: apiclient.New(pool, queries, nil, log),
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)

	admin := seedClient(t, pool, "solo-admin", "admin")
	assert.Equal(t, http.StatusOK,
		pcReq(t, srv, http.MethodGet, "/api/v1/admin/clients", admin, "").Code)
}

func TestClients_CreateDisclosesKeyOnce(t *testing.T) {
	srv, pool := testClientsAPI(t, nil)
	admin := seedClient(t, pool, "issuer", "admin")

	w := pcReq(t, srv, http.MethodPost, "/api/v1/admin/clients",
		admin, `{"name":"partner-a","role":"reader"}`)
	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"),
		"key material must not be retained by shared caches")

	var body struct {
		Data struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			Role      string `json:"role"`
			IsActive  bool   `json:"is_active"`
			APIKey    string `json:"api_key"`
			Note      string `json:"note"`
			KeyHash   string `json:"key_hash"`
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.NotZero(t, body.Data.ID)
	assert.Equal(t, "partner-a", body.Data.Name)
	assert.Equal(t, "reader", body.Data.Role)
	assert.Contains(t, body.Data.APIKey, "cw_")
	assert.NotEmpty(t, body.Data.Note)
	assert.Empty(t, body.Data.KeyHash, "key_hash never leaves the surface")

	// The minted key authenticates as a reader: 200 on /networks, 403 on
	// the admin surface.
	assert.Equal(t, http.StatusOK,
		pcReq(t, srv, http.MethodGet, "/api/v1/networks", body.Data.APIKey, "").Code)
	assert.Equal(t, http.StatusForbidden,
		pcReq(t, srv, http.MethodGet, "/api/v1/admin/clients", body.Data.APIKey, "").Code)

	// Minted key is absent from every subsequent response body.
	list := pcReq(t, srv, http.MethodGet, "/api/v1/admin/clients", admin, "")
	require.Equal(t, http.StatusOK, list.Code)
	assert.NotContains(t, list.Body.String(), body.Data.APIKey)
	assert.NotContains(t, list.Body.String(), "key_hash")

	// An admin-role minted key can drive the admin surface itself.
	w2 := pcReq(t, srv, http.MethodPost, "/api/v1/admin/clients",
		admin, `{"name":"ops-b","role":"admin"}`)
	require.Equal(t, http.StatusCreated, w2.Code)
	var body2 struct {
		Data struct {
			APIKey string `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &body2))
	assert.Equal(t, http.StatusOK,
		pcReq(t, srv, http.MethodGet, "/api/v1/admin/clients", body2.Data.APIKey, "").Code)
}

func TestClients_ListViewFieldsAndPagination(t *testing.T) {
	srv, pool := testClientsAPI(t, nil)
	admin := seedClient(t, pool, "lister", "admin")
	for _, n := range []string{"p1", "p2", "p3"} {
		require.Equal(t, http.StatusCreated,
			pcReq(t, srv, http.MethodPost, "/api/v1/admin/clients",
				admin, `{"name":"`+n+`","role":"reader"}`).Code)
	}

	w := pcReq(t, srv, http.MethodGet, "/api/v1/admin/clients?limit=2", admin, "")
	require.Equal(t, http.StatusOK, w.Code)
	var page1 struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"next_cursor"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page1))
	require.Len(t, page1.Data.Items, 2)
	require.NotEmpty(t, page1.Data.NextCursor)
	for _, item := range page1.Data.Items {
		for k := range item {
			assert.True(t, clientViewKeys[k], "unexpected view field %q", k)
		}
	}
	assert.Equal(t, "p3", page1.Data.Items[0]["name"], "newest first")

	w2 := pcReq(t, srv, http.MethodGet,
		"/api/v1/admin/clients?limit=2&cursor="+page1.Data.NextCursor, admin, "")
	require.Equal(t, http.StatusOK, w2.Code)
	var page2 struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &page2))
	assert.Len(t, page2.Data.Items, 2, "remaining clients on page 2")

	// A cursor anchored in the past yields an empty page — items:[] not null.
	past := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"t":"2000-01-01T00:00:00Z","i":0}`))
	empty := pcReq(t, srv, http.MethodGet,
		"/api/v1/admin/clients?cursor="+past, admin, "")
	require.Equal(t, http.StatusOK, empty.Code)
	assert.Contains(t, empty.Body.String(), `"items":[]`)
}

func TestClients_PatchLifecycle(t *testing.T) {
	srv, pool := testClientsAPI(t, nil)
	admin := seedClient(t, pool, "ops", "admin")
	target := seedClient(t, pool, "target", "reader")
	id := clientID(t, pool, "target")

	// Rename + role change.
	w := pcReq(t, srv, http.MethodPatch, "/api/v1/admin/clients/"+itoa(id),
		admin, `{"name":"renamed","role":"admin"}`)
	require.Equal(t, http.StatusOK, w.Code)
	data := decodeData(t, w)
	assert.Equal(t, "renamed", data["name"])
	assert.Equal(t, "admin", data["role"])

	// Deactivate → key stops authenticating; reactivate → works again.
	w = pcReq(t, srv, http.MethodPatch, "/api/v1/admin/clients/"+itoa(id),
		admin, `{"is_active":false}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, http.StatusUnauthorized,
		pcReq(t, srv, http.MethodGet, "/api/v1/networks", target, "").Code)

	w = pcReq(t, srv, http.MethodPatch, "/api/v1/admin/clients/"+itoa(id),
		admin, `{"is_active":true}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, http.StatusOK,
		pcReq(t, srv, http.MethodGet, "/api/v1/networks", target, "").Code)

	// PATCH {} is a no-op returning the current view.
	w = pcReq(t, srv, http.MethodPatch, "/api/v1/admin/clients/"+itoa(id),
		admin, `{}`)
	require.Equal(t, http.StatusOK, w.Code)
	data = decodeData(t, w)
	assert.Equal(t, "renamed", data["name"])
	assert.Equal(t, "admin", data["role"])
}

func TestClients_ErrorMatrix(t *testing.T) {
	srv, pool := testClientsAPI(t, nil)
	admin := seedClient(t, pool, "ops2", "admin")
	id := clientID(t, pool, "ops2")
	base := "/api/v1/admin/clients/"

	// {id} parse: non-numeric → 400; valid-but-absent → 404.
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodPatch,
		base+"abc", admin, `{}`).Code)
	for _, missing := range []string{"0", "-1", "99999"} {
		assert.Equal(t, http.StatusNotFound, pcReq(t, srv, http.MethodPatch,
			base+missing, admin, `{"name":"x"}`).Code, "id %s", missing)
	}

	// Unknown JSON keys rejected on POST and PATCH.
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodPost,
		"/api/v1/admin/clients", admin, `{"name":"x","bogus":1}`).Code)
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodPatch,
		base+itoa(id), admin, `{"key_hash":"injected"}`).Code)

	// Validation: role enum, name bounds.
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodPost,
		"/api/v1/admin/clients", admin, `{"name":"x","role":"root"}`).Code)
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodPost,
		"/api/v1/admin/clients", admin, `{"name":"   ","role":"reader"}`).Code)
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodPost,
		"/api/v1/admin/clients", admin, `{"role":"reader"}`).Code)
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodPatch,
		base+itoa(id), admin, `{"role":"viewer"}`).Code)

	// limit: <=0/non-int → 400; >200 clamps (200).
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodGet,
		"/api/v1/admin/clients?limit=0", admin, "").Code)
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodGet,
		"/api/v1/admin/clients?limit=abc", admin, "").Code)
	assert.Equal(t, http.StatusOK, pcReq(t, srv, http.MethodGet,
		"/api/v1/admin/clients?limit=500", admin, "").Code)

	// Invalid cursor → 400.
	assert.Equal(t, http.StatusBadRequest, pcReq(t, srv, http.MethodGet,
		"/api/v1/admin/clients?cursor=not-a-cursor", admin, "").Code)
}

func TestClients_AdminLockout409(t *testing.T) {
	srv, pool := testClientsAPI(t, nil)
	sole := seedClient(t, pool, "sole-ops", "admin")
	id := clientID(t, pool, "sole-ops")

	w := pcReq(t, srv, http.MethodPatch, "/api/v1/admin/clients/"+itoa(id),
		sole, `{"role":"reader"}`)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "ADMIN_LOCKOUT")

	w = pcReq(t, srv, http.MethodPatch, "/api/v1/admin/clients/"+itoa(id),
		sole, `{"is_active":false}`)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "ADMIN_LOCKOUT")

	// Self no-op and self-rename are allowed even as the last admin.
	assert.Equal(t, http.StatusOK, pcReq(t, srv, http.MethodPatch,
		"/api/v1/admin/clients/"+itoa(id), sole, `{"role":"admin"}`).Code)
	assert.Equal(t, http.StatusOK, pcReq(t, srv, http.MethodPatch,
		"/api/v1/admin/clients/"+itoa(id), sole, `{"name":"sole-ops-2"}`).Code)

	// With a second admin, demoting the OTHER is allowed.
	other := seedClient(t, pool, "second-ops", "admin")
	otherID := clientID(t, pool, "second-ops")
	w = pcReq(t, srv, http.MethodPatch, "/api/v1/admin/clients/"+itoa(otherID),
		sole, `{"role":"reader"}`)
	require.Equal(t, http.StatusOK, w.Code)
	// Demoted key loses admin reach immediately.
	assert.Equal(t, http.StatusForbidden, pcReq(t, srv, http.MethodGet,
		"/api/v1/admin/clients", other, "").Code)
}

func TestClients_EnvManagedWarning(t *testing.T) {
	// Seed first so we know the raw key, then wire its hash as env-managed.
	pool := testutil.RequireTestPool(t)
	rdb := testutil.RequireTestRedis(t)
	envRaw := "test-env-ops-key"
	_, err := pool.Exec(context.Background(),
		`INSERT INTO api_clients (name, key_hash, role) VALUES ($1, $2, $3)`,
		"env-ops", middleware.HashAPIKey(envRaw), "admin")
	require.NoError(t, err)

	queries := db.New(pool)
	resolver := testResolver(t, queries)
	cfg := &config.Config{RateLimitRequests: 10000, RateLimitAnon: 10000,
		RateLimitWindow: time.Minute}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := ingestion.NewService(pool, resolver, nil, log)
	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb, Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: queries,
		Admin: ingestion.NewAdminHandler(svc, queries, log),
		Clients: apiclient.New(pool, queries,
			[]string{middleware.HashAPIKey(envRaw)}, log),
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)

	id := clientID(t, pool, "env-ops")
	w := pcReq(t, srv, http.MethodPatch, "/api/v1/admin/clients/"+itoa(id),
		envRaw, `{"name":"env-ops-renamed"}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "env_managed")
	assert.Contains(t, w.Body.String(), "API_BOOTSTRAP_")

	// Non-env rows carry no warning.
	seedClient(t, pool, "normal-ops", "admin")
	w = pcReq(t, srv, http.MethodPatch,
		"/api/v1/admin/clients/"+itoa(clientID(t, pool, "normal-ops")),
		envRaw, `{"name":"normal-2"}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "env_managed")
}

func TestClients_DuplicateNamesCoexist(t *testing.T) {
	srv, pool := testClientsAPI(t, nil)
	admin := seedClient(t, pool, "dup-issuer", "admin")

	w1 := pcReq(t, srv, http.MethodPost, "/api/v1/admin/clients",
		admin, `{"name":"dup","role":"reader"}`)
	w2 := pcReq(t, srv, http.MethodPost, "/api/v1/admin/clients",
		admin, `{"name":"dup","role":"reader"}`)
	require.Equal(t, http.StatusCreated, w1.Code)
	require.Equal(t, http.StatusCreated, w2.Code)
	d1, d2 := decodeData(t, w1), decodeData(t, w2)
	assert.NotEqual(t, d1["id"], d2["id"], "duplicate names get distinct ids")
}
