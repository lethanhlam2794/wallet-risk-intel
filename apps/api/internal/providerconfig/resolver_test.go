package providerconfig

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
)

// fakeStore mirrors the db.Queries contract for provider config reads:
// absent row ⇒ pgx.ErrNoRows, never an empty struct.
type fakeStore struct {
	rows  map[string]db.ProviderConfig
	err   error
	all   []db.ProviderConfig
	allEr error
}

func (f *fakeStore) GetProviderConfig(_ context.Context, code string) (db.ProviderConfig, error) {
	if f.err != nil {
		return db.ProviderConfig{}, f.err
	}
	r, ok := f.rows[code]
	if !ok {
		return db.ProviderConfig{}, pgx.ErrNoRows
	}
	return r, nil
}

func (f *fakeStore) ListProviderConfigs(_ context.Context) ([]db.ProviderConfig, error) {
	if f.allEr != nil {
		return nil, f.allEr
	}
	if f.all != nil {
		return f.all, nil
	}
	out := make([]db.ProviderConfig, 0, len(f.rows))
	for _, r := range f.rows {
		out = append(out, r)
	}
	return out, nil
}

func testCfg() *config.Config {
	return &config.Config{
		ChainscanBaseURL:        "https://api.etherscan.io/v2/api",
		ChainscanEnabledChains:  "ethereum",
		EtherscanAPIKey:         "env-etherscan-key",
		CovalentAPIKey:          "env-covalent-key",
		CovalentBaseURL:         "https://api.covalenthq.com",
		CovalentEnabledChains:   "bsc",
		NoderealAPIKey:          "env-nodereal-key",
		NoderealBaseURL:         "https://bsc-mainnet.nodereal.io/v1",
		BlockscoutBaseURL:       "https://eth.blockscout.com",
		BlockscoutEnabledChains: "ethereum",
	}
}

func resolve(t *testing.T, cfg *config.Config, store Store) *Resolver {
	t.Helper()
	r, err := ResolveAll(context.Background(), cfg, store, nil,
		WithLogger(slog.New(slog.DiscardHandler)),
		WithAllowedHosts(func(string) []string {
			return []string{"api.etherscan.io", "api.covalenthq.com",
				"bsc-mainnet.nodereal.io", "eth.blockscout.com"}
		}))
	require.NoError(t, err)
	return r
}

func TestDescriptorMap_CoversAllTxProviders(t *testing.T) {
	want := []string{"alchemy", "alchemy_bsc", "alchemy_eth", "ankr", "bitquery",
		"blockscout", "blockscout_pro", "chainbase", "chainscan", "covalent",
		"moralis", "nodereal", "oklink", "tatum", "tokenview"}
	assert.ElementsMatch(t, want, Codes())
	assert.Equal(t, "ETHERSCAN_API_KEY", descriptorFor("chainscan").KeyEnv,
		"chainscan resolves its key from ETHERSCAN_API_KEY")
}

// Truth table (critic R1): row-absent ×{env set,env unset} + db-null
// ×{env set,env unset} + db-empty ×{env set,env unset} + db-set
// ×{env set,env unset} — 8 cases per field, all covered.
func TestResolve_APIKeyTruthTable(t *testing.T) {
	row := func(v string, valid bool) *fakeStore {
		return &fakeStore{rows: map[string]db.ProviderConfig{"covalent": {
			SourceCode: "covalent",
			ApiKey:     pgtype.Text{String: v, Valid: valid},
		}}}
	}

	t.Run("row-absent env-set", func(t *testing.T) {
		t.Setenv("COVALENT_API_KEY", "env-covalent-key")
		r := resolve(t, testCfg(), &fakeStore{})
		got := r.Get("covalent")
		assert.Equal(t, "env-covalent-key", got.APIKey)
		assert.Equal(t, OriginEnv, got.Origins["api_key"])
	})
	t.Run("row-absent env-unset", func(t *testing.T) {
		cfg := testCfg()
		cfg.CovalentAPIKey = ""
		r := resolve(t, cfg, &fakeStore{})
		assert.Equal(t, "", r.Get("covalent").APIKey)
		assert.Equal(t, OriginDefault, r.Get("covalent").Origins["api_key"])
	})
	t.Run("db-null env-set", func(t *testing.T) {
		t.Setenv("COVALENT_API_KEY", "env-covalent-key")
		r := resolve(t, testCfg(), &fakeStore{rows: map[string]db.ProviderConfig{
			"covalent": {SourceCode: "covalent"}}})
		assert.Equal(t, "env-covalent-key", r.Get("covalent").APIKey,
			"NULL falls back to env")
		assert.Equal(t, OriginEnv, r.Get("covalent").Origins["api_key"])
	})
	t.Run("db-null env-unset", func(t *testing.T) {
		cfg := testCfg()
		cfg.CovalentAPIKey = ""
		r := resolve(t, cfg, &fakeStore{rows: map[string]db.ProviderConfig{
			"covalent": {SourceCode: "covalent"}}})
		assert.Equal(t, "", r.Get("covalent").APIKey)
	})
	t.Run("db-empty env-set", func(t *testing.T) {
		t.Setenv("COVALENT_API_KEY", "env-covalent-key")
		r := resolve(t, testCfg(), row("", true))
		assert.Equal(t, "", r.Get("covalent").APIKey,
			"empty string is an explicit override — env does not rescue it")
		assert.Equal(t, OriginDB, r.Get("covalent").Origins["api_key"])
	})
	t.Run("db-empty env-unset", func(t *testing.T) {
		cfg := testCfg()
		cfg.CovalentAPIKey = ""
		r := resolve(t, cfg, row("", true))
		assert.Equal(t, "", r.Get("covalent").APIKey)
	})
	t.Run("db-set env-set", func(t *testing.T) {
		t.Setenv("COVALENT_API_KEY", "env-covalent-key")
		r := resolve(t, testCfg(), row("db-covalent-key", true))
		assert.Equal(t, "db-covalent-key", r.Get("covalent").APIKey)
		assert.Equal(t, OriginDB, r.Get("covalent").Origins["api_key"])
	})
	t.Run("db-set env-unset", func(t *testing.T) {
		cfg := testCfg()
		cfg.CovalentAPIKey = ""
		r := resolve(t, cfg, row("db-covalent-key", true))
		assert.Equal(t, "db-covalent-key", r.Get("covalent").APIKey)
	})
}

func TestResolve_BaseURLNullVsEmpty(t *testing.T) {
	row := func(v string, valid bool) *fakeStore {
		return &fakeStore{rows: map[string]db.ProviderConfig{"covalent": {
			SourceCode: "covalent",
			BaseUrl:    pgtype.Text{String: v, Valid: valid},
		}}}
	}
	t.Run("db-null falls back to env", func(t *testing.T) {
		r := resolve(t, testCfg(), row("", false))
		assert.Equal(t, "https://api.covalenthq.com", r.Get("covalent").BaseURL)
		assert.Equal(t, OriginDefault, r.Get("covalent").Origins["base_url"])
	})
	t.Run("db-empty is an explicit override", func(t *testing.T) {
		r := resolve(t, testCfg(), row("", true))
		assert.Equal(t, "", r.Get("covalent").BaseURL)
		assert.Equal(t, OriginDB, r.Get("covalent").Origins["base_url"])
	})
	t.Run("db-set wins over env", func(t *testing.T) {
		r := resolve(t, testCfg(), row("https://api.covalenthq.com", true))
		assert.Equal(t, "https://api.covalenthq.com", r.Get("covalent").BaseURL)
	})
}

func TestResolve_EnabledChainsNullVsEmpty(t *testing.T) {
	row := func(j []byte) *fakeStore {
		return &fakeStore{rows: map[string]db.ProviderConfig{"covalent": {
			SourceCode:    "covalent",
			EnabledChains: j,
		}}}
	}
	t.Run("db-null falls back to env comma list", func(t *testing.T) {
		r := resolve(t, testCfg(), row(nil))
		assert.Equal(t, []string{"bsc"}, r.Get("covalent").EnabledChains)
	})
	t.Run("db-empty-array is explicit kill", func(t *testing.T) {
		r := resolve(t, testCfg(), row([]byte(`[]`)))
		assert.Empty(t, r.Get("covalent").EnabledChains)
		assert.Equal(t, OriginDB, r.Get("covalent").Origins["enabled_chains"])
	})
	t.Run("db-set wins over env", func(t *testing.T) {
		r := resolve(t, testCfg(), row([]byte(`["ethereum","bsc"]`)))
		assert.Equal(t, []string{"ethereum", "bsc"}, r.Get("covalent").EnabledChains)
	})
}

func TestResolve_Status(t *testing.T) {
	r := resolve(t, testCfg(), &fakeStore{})
	assert.Equal(t, "active", r.Get("covalent").Status, "absent row ⇒ active")

	r = resolve(t, testCfg(), &fakeStore{rows: map[string]db.ProviderConfig{
		"covalent": {SourceCode: "covalent", Status: "inactive"}}})
	assert.Equal(t, "inactive", r.Get("covalent").Status)
}

// Env parity: absent rows ⇒ resolved config is byte-identical to the
// env-only path (the built-in rollback for the whole feature).
func TestResolve_AbsentRowEnvParity(t *testing.T) {
	r := resolve(t, testCfg(), &fakeStore{})

	cs := r.Get("chainscan")
	assert.Equal(t, testCfg().EtherscanAPIKey, cs.APIKey)
	assert.Equal(t, testCfg().ChainscanBaseURL, cs.BaseURL)
	assert.Equal(t, []string{"ethereum"}, cs.EnabledChains)

	nr := r.Get("nodereal")
	assert.Equal(t, testCfg().NoderealAPIKey, nr.APIKey)
	assert.Equal(t, testCfg().NoderealBaseURL, nr.BaseURL)

	bs := r.Get("blockscout")
	assert.Equal(t, "", bs.APIKey, "blockscout is keyless")
	assert.Equal(t, testCfg().BlockscoutBaseURL, bs.BaseURL)
}

// Any resolution error is fatal and names provider+field — never an env
// fallback on error (fallback happens only on absent values).
func TestResolve_ErrorsAreFatalAndNamed(t *testing.T) {
	assertProviderField := func(t *testing.T, err error, provider, field string) {
		t.Helper()
		require.Error(t, err)
		var re *Error
		require.True(t, errors.As(err, &re), "want *Error, got %T", err)
		assert.Equal(t, provider, re.Provider)
		assert.Equal(t, field, re.Field)
	}

	t.Run("db read failure", func(t *testing.T) {
		_, err := ResolveAll(context.Background(), testCfg(),
			&fakeStore{err: errors.New("connection reset")}, nil)
		assertProviderField(t, err, "alchemy", "config")
	})

	t.Run("decrypt failure", func(t *testing.T) {
		keys, err := NewKeystore(testKEKB64)
		require.NoError(t, err)
		_, err = ResolveAll(context.Background(), testCfg(), &fakeStore{
			rows: map[string]db.ProviderConfig{"covalent": {
				SourceCode: "covalent",
				ApiKey:     pgtype.Text{String: "enc:v1:AAAA:BBBB", Valid: true},
			}}}, keys)
		assertProviderField(t, err, "covalent", "api_key")
	})

	t.Run("allowlist rejection", func(t *testing.T) {
		_, err := ResolveAll(context.Background(), testCfg(), &fakeStore{
			rows: map[string]db.ProviderConfig{"covalent": {
				SourceCode: "covalent",
				BaseUrl:    pgtype.Text{String: "https://evil.example.com", Valid: true},
			}}}, nil,
			WithAllowedHosts(func(string) []string { return []string{"api.covalenthq.com"} }))
		assertProviderField(t, err, "covalent", "base_url")
	})

	for _, tc := range []struct {
		name string
		json string
	}{
		{"non-array object", `{"a":1}`},
		{"non-string element", `["ethereum",1]`},
		{"malformed json", `[`},
	} {
		t.Run("enabled_chains "+tc.name, func(t *testing.T) {
			_, err := ResolveAll(context.Background(), testCfg(), &fakeStore{
				rows: map[string]db.ProviderConfig{"covalent": {
					SourceCode:    "covalent",
					EnabledChains: []byte(tc.json),
				}}}, nil)
			assertProviderField(t, err, "covalent", "enabled_chains")
		})
	}
}

// Stale rows for unregistered source codes are ignored (warn logged),
// never resolved or fatal.
func TestResolve_StaleRowsIgnored(t *testing.T) {
	r, err := ResolveAll(context.Background(), testCfg(), &fakeStore{
		rows: map[string]db.ProviderConfig{
			"covalent": {SourceCode: "covalent", Status: "inactive"},
		},
		all: []db.ProviderConfig{
			{SourceCode: "ghost_provider", Status: "inactive"},
			{SourceCode: "covalent", Status: "inactive"},
		},
	}, nil, WithLogger(slog.New(slog.DiscardHandler)))
	require.NoError(t, err)
	assert.Nil(t, r.Get("ghost_provider"))
	assert.Equal(t, "inactive", r.Get("covalent").Status)
}

// Encrypted api_key round-trips through resolution when KEK is set.
func TestResolve_EncryptedAPIKey(t *testing.T) {
	keys, err := NewKeystore(testKEKB64)
	require.NoError(t, err)
	enc, err := keys.Encrypt("secret-key-value")
	require.NoError(t, err)

	r, err := ResolveAll(context.Background(), testCfg(), &fakeStore{
		rows: map[string]db.ProviderConfig{"covalent": {
			SourceCode: "covalent",
			ApiKey:     pgtype.Text{String: enc, Valid: true},
		}}}, keys)
	require.NoError(t, err)
	assert.Equal(t, "secret-key-value", r.Get("covalent").APIKey)
}
