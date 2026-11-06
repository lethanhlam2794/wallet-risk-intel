package providerconfig

// Admin service integration tests (phase 4) — real test DB throughout:
// masked reads/writes, whitelisted validation, audit rows, last-active
// warning, cursor pagination. api_key fixtures are obviously fake.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/testutil"
)

const svcTestDSN = "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_providerconfig_test?sslmode=disable"

func newSvc(t *testing.T, keys *Keystore) (*Service, *pgxpool.Pool, *db.Queries) {
	t.Helper()
	pool := testutil.RequireTestPoolDSN(t, svcTestDSN)
	q := db.New(pool)
	svc := NewService(pool, q, keys, nil,
		map[string][]string{"ethereum": {"chainscan"}, "bsc": {"nodereal"}},
		[]string{"ethereum", "bsc"},
		WithAllowedHosts(func(string) []string { return []string{"api.covalenthq.com", "api.etherscan.io"} }))
	return svc, pool, q
}

func actor() Actor { return Actor{ID: "test-admin", Type: "api_client"} }

func TestService_GetUnknownCode404(t *testing.T) {
	svc, _, _ := newSvc(t, nil)
	_, err := svc.Get(context.Background(), "not-a-provider")
	require.ErrorIs(t, err, ErrUnknownProvider)
}

func TestService_ListMarksUnconfigured(t *testing.T) {
	svc, _, _ := newSvc(t, nil)
	views, err := svc.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, views, 15)
	for _, v := range views {
		assert.False(t, v.Configured, "%s unconfigured", v.SourceCode)
		assert.Equal(t, "active", v.Status)
		assert.Equal(t, []string{"api_key", "base_url", "enabled_chains"}, v.RequiresRestart)
	}
}

func TestService_UpdateMaskedWriteAndAudit(t *testing.T) {
	svc, pool, q := newSvc(t, nil)
	ctx := context.Background()
	key := "fake-admin-api-key-9999"
	url := "https://api.covalenthq.com"
	chains := []string{"ethereum"}
	res, err := svc.Update(ctx, "covalent", actor(), Patch{
		APIKey:        &key,
		BaseURL:       &url,
		EnabledChains: &chains,
	})
	require.NoError(t, err)
	assert.True(t, res.View.APIKey.Set)
	require.NotNil(t, res.View.APIKey.Last4)
	assert.Equal(t, "9999", *res.View.APIKey.Last4)
	assert.Equal(t, "test-admin", *res.View.UpdatedBy)

	// Stored row carries the plaintext key (no KEK) — the audit row must
	// never contain it.
	var apiKeyStored string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_key FROM provider_configs WHERE source_code='covalent'`).Scan(&apiKeyStored))
	assert.Equal(t, key, apiKeyStored, "plaintext mode stores the key verbatim")

	rows, err := q.ListProviderConfigChanges(ctx, db.ListProviderConfigChangesParams{
		SourceCode: "covalent", Lim: 20})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	var sawLast4 bool
	for _, r := range rows {
		assert.NotContains(t, string(r.NewValue), "fake-admin-api-key-9999",
			"audit %s row is masked — no plaintext key", r.Field)
		if r.Field == "api_key" {
			var mask map[string]any
			require.NoError(t, json.Unmarshal(r.NewValue, &mask))
			sawLast4 = mask["last4"] == "9999" && mask["set"] == true
		}
	}
	assert.True(t, sawLast4, "api_key audit carries the masked shape")

	// Changes endpoint is newest-first + cursor-paginates without dupes.
	p1, err := svc.ListChanges(ctx, "covalent", "", 2)
	require.NoError(t, err)
	require.Len(t, p1.Items, 2)
	require.NotEmpty(t, p1.NextCursor)
	p2, err := svc.ListChanges(ctx, "covalent", p1.NextCursor, 2)
	require.NoError(t, err)
	require.Len(t, p2.Items, 1)
	assert.Equal(t, p1.Items[0].ChangedAt.After(p2.Items[0].ChangedAt) ||
		p1.Items[0].ChangedAt.Equal(p2.Items[0].ChangedAt), true)
	_, err = svc.ListChanges(ctx, "covalent", "garbage!!!", 10)
	require.ErrorIs(t, err, ErrInvalidCursor)
}

func TestService_UpdateValidation(t *testing.T) {
	svc, _, _ := newSvc(t, nil)
	ctx := context.Background()

	encKey := "enc:v1:AAAA:BBBB"
	_, err := svc.Update(ctx, "covalent", actor(), Patch{APIKey: &encKey})
	require.ErrorIs(t, err, ErrEncPrefix)

	bad := "bogus"
	_, err = svc.Update(ctx, "covalent", actor(), Patch{Status: &bad})
	require.ErrorIs(t, err, ErrInvalidStatus)

	evil := "https://evil.example.com"
	_, err = svc.Update(ctx, "covalent", actor(), Patch{BaseURL: &evil})
	var hostErr *HostNotAllowedError
	require.ErrorAs(t, err, &hostErr)
	assert.Contains(t, hostErr.Allowed, "api.covalenthq.com")

	noScheme := "not-a-url"
	_, err = svc.Update(ctx, "covalent", actor(), Patch{BaseURL: &noScheme})
	require.ErrorAs(t, err, &hostErr)

	chains := []string{"solana"}
	_, err = svc.Update(ctx, "covalent", actor(), Patch{EnabledChains: &chains})
	require.ErrorIs(t, err, ErrInvalidChain)

	// Unknown code is a 404-shaped error before any validation.
	_, err = svc.Update(ctx, "ghost", actor(), Patch{Status: &bad})
	require.ErrorIs(t, err, ErrUnknownProvider)
}

func TestService_UnchangedFieldWritesNoAudit(t *testing.T) {
	svc, _, q := newSvc(t, nil)
	ctx := context.Background()
	active := "active"
	res, err := svc.Update(ctx, "covalent", actor(), Patch{Status: &active})
	require.NoError(t, err)
	assert.Empty(t, res.Warnings)
	rows, err := q.ListProviderConfigChanges(ctx, db.ListProviderConfigChangesParams{
		SourceCode: "covalent", Lim: 20})
	require.NoError(t, err)
	assert.Empty(t, rows, "writing the existing status audits nothing")
}

func TestService_LastActiveMemberWarning(t *testing.T) {
	svc, _, _ := newSvc(t, nil)
	inactive := "inactive"
	res, err := svc.Update(context.Background(), "chainscan", actor(),
		Patch{Status: &inactive})
	require.NoError(t, err)
	var found bool
	for _, w := range res.Warnings {
		if w.Code == "last_active_member" && w.Network == "ethereum" {
			found = true
		}
	}
	assert.True(t, found, "deactivating the only member of the ethereum chain warns")
}

func TestService_ExplicitEmptyOverridesFromNull(t *testing.T) {
	svc, pool, q := newSvc(t, nil)
	ctx := context.Background()

	// Pinned semantics: "" / [] are explicit overrides, not fallbacks —
	// writing them over a NULL column is a real change that must store
	// and audit, never a silent no-op.
	key, url := "", ""
	chains := []string{}
	res, err := svc.Update(ctx, "covalent", actor(), Patch{
		APIKey: &key, BaseURL: &url, EnabledChains: &chains})
	require.NoError(t, err)
	assert.True(t, res.View.Configured)
	assert.False(t, res.View.APIKey.Set)
	require.NotNil(t, res.View.BaseURL)
	assert.Equal(t, "", *res.View.BaseURL)
	require.NotNil(t, res.View.EnabledChains)
	assert.Empty(t, *res.View.EnabledChains)
	var sawKill bool
	for _, w := range res.Warnings {
		if w.Code == "no_enabled_chains" {
			sawKill = true
		}
	}
	assert.True(t, sawKill, "enabled_chains=[] discloses the kill channel")

	var apiKey, baseURL string
	var rawChains []byte
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_key, base_url, enabled_chains FROM provider_configs
		 WHERE source_code='covalent'`).Scan(&apiKey, &baseURL, &rawChains))
	assert.Equal(t, "", apiKey, "stored empty string, not NULL")
	assert.Equal(t, "", baseURL, "stored empty string, not NULL")
	assert.JSONEq(t, `[]`, string(rawChains), "stored empty array, not NULL")

	rows, err := q.ListProviderConfigChanges(ctx, db.ListProviderConfigChangesParams{
		SourceCode: "covalent", Lim: 20})
	require.NoError(t, err)
	require.Len(t, rows, 3, "every explicit-empty write audits")
	fields := map[string]json.RawMessage{}
	for _, r := range rows {
		fields[r.Field] = r.PreviousValue
	}
	assert.JSONEq(t, `{"set":false,"last4":null}`, string(fields["api_key"]))
	assert.JSONEq(t, `null`, string(fields["base_url"]))
	assert.JSONEq(t, `null`, string(fields["enabled_chains"]))
}

func TestService_ConcurrentTwoFieldPUTNoLostUpdate(t *testing.T) {
	svc, _, q := newSvc(t, nil)
	ctx := context.Background()
	inactive := "inactive"
	_, err := svc.Update(ctx, "covalent", actor(), Patch{Status: &inactive})
	require.NoError(t, err) // row exists — both writers take FOR UPDATE

	key := "fake-admin-api-key-2222"
	url := "https://api.covalenthq.com"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := svc.Update(ctx, "covalent", actor(), Patch{APIKey: &key})
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := svc.Update(ctx, "covalent", actor(), Patch{BaseURL: &url})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	row, err := q.GetProviderConfig(ctx, "covalent")
	require.NoError(t, err)
	assert.Equal(t, key, row.ApiKey.String, "concurrent api_key write landed")
	assert.Equal(t, url, row.BaseUrl.String, "concurrent base_url write landed")
	assert.Equal(t, "inactive", row.Status, "untouched field preserved")

	rows, err := q.ListProviderConfigChanges(ctx, db.ListProviderConfigChangesParams{
		SourceCode: "covalent", Lim: 20})
	require.NoError(t, err)
	fields := map[string]bool{}
	for _, r := range rows {
		fields[r.Field] = true
	}
	assert.True(t, fields["api_key"] && fields["base_url"] && fields["status"],
		"audit rows exist for both concurrent writes: %v", fields)
}

func TestService_ConcurrentFirstWriteSerialized(t *testing.T) {
	svc, _, q := newSvc(t, nil)
	ctx := context.Background()
	key := "fake-admin-api-key-3333"
	inactive := "inactive"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := svc.Update(ctx, "ankr", actor(), Patch{APIKey: &key})
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := svc.Update(ctx, "ankr", actor(), Patch{Status: &inactive})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "concurrent first-write never surfaces a 500")
	}
	row, err := q.GetProviderConfig(ctx, "ankr")
	require.NoError(t, err)
	assert.Equal(t, key, row.ApiKey.String)
	assert.Equal(t, "inactive", row.Status)
}

func TestService_DriftWarningsRequiresRestart(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, svcTestDSN)
	q := db.New(pool)
	resolver := &Resolver{resolved: map[string]*Resolved{
		"covalent": {
			SourceCode:    "covalent",
			APIKey:        "fake-boot-key",
			BaseURL:       "https://api.covalenthq.com",
			EnabledChains: []string{"ethereum"},
			Status:        "active",
		},
	}}
	svc := NewService(pool, q, nil, resolver,
		map[string][]string{"ethereum": {"chainscan"}},
		[]string{"ethereum", "bsc"},
		WithAllowedHosts(func(string) []string { return []string{"api.covalenthq.com", "api.etherscan.io"} }))
	ctx := context.Background()

	// Stored ≠ boot-resolved in-effect ⇒ requires_restart per field.
	newURL := "https://api.etherscan.io"
	chains := []string{"bsc"}
	res, err := svc.Update(ctx, "covalent", actor(), Patch{
		BaseURL: &newURL, EnabledChains: &chains})
	require.NoError(t, err)
	warnFields := map[string]bool{}
	for _, w := range res.Warnings {
		if w.Code == "requires_restart" {
			warnFields[w.Field] = true
		}
	}
	assert.Equal(t, map[string]bool{"base_url": true, "enabled_chains": true},
		warnFields, "drifted fields warn")

	// Writing values identical to the boot snapshot ⇒ no drift warning.
	key := "fake-boot-key"
	sameChains := []string{"ethereum"}
	res2, err := svc.Update(ctx, "covalent", actor(), Patch{
		APIKey: &key, EnabledChains: &sameChains})
	require.NoError(t, err)
	for _, w := range res2.Warnings {
		assert.NotEqual(t, "requires_restart", w.Code, "in-effect values warn nothing")
	}

	// A different key ⇒ api_key drift warns too.
	otherKey := "fake-other-key-0000"
	res3, err := svc.Update(ctx, "covalent", actor(), Patch{APIKey: &otherKey})
	require.NoError(t, err)
	var sawKey bool
	for _, w := range res3.Warnings {
		if w.Code == "requires_restart" && w.Field == "api_key" {
			sawKey = true
		}
	}
	assert.True(t, sawKey)
}

func TestService_EmptyPatchMaterializesNothing(t *testing.T) {
	svc, pool, _ := newSvc(t, nil)
	res, err := svc.Update(context.Background(), "covalent", actor(), Patch{})
	require.NoError(t, err)
	assert.False(t, res.View.Configured)
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM provider_configs WHERE source_code='covalent'`).Scan(&n))
	assert.Zero(t, n, "PUT {} never materializes an all-NULL row")
}

func TestService_EncryptedWriteAndMaskedRead(t *testing.T) {
	keys, err := NewKeystore(testKEKB64)
	require.NoError(t, err)
	svc, pool, _ := newSvc(t, keys)
	ctx := context.Background()
	key := "fake-admin-api-key-7777"
	res, err := svc.Update(ctx, "ankr", actor(), Patch{APIKey: &key})
	require.NoError(t, err)
	assert.Equal(t, "7777", *res.View.APIKey.Last4)

	var stored string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT api_key FROM provider_configs WHERE source_code='ankr'`).Scan(&stored))
	assert.True(t, strings.HasPrefix(stored, encV1Prefix),
		"KEK set ⇒ enc:v1 wire shape at rest")
	assert.NotContains(t, stored, key)
}
