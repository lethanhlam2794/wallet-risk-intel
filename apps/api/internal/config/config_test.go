package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_MissingDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

func TestLoad_ValidEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable")
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:6380/1")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SANCTIONS_STALE_AFTER", "24h")
	t.Setenv("RATE_LIMIT_REQUESTS", "10")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.HTTPAddr)
	assert.Equal(t, "postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable", cfg.DatabaseURL)
	assert.Equal(t, "redis://127.0.0.1:6380/1", cfg.RedisURL)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, 24*time.Hour, cfg.SanctionsStaleAfter)
	assert.Equal(t, 10, cfg.RateLimitRequests)
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, ":8080", cfg.HTTPAddr)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, 48*time.Hour, cfg.SanctionsStaleAfter)
	assert.Equal(t, "https://sanctionslistservice.ofac.treas.gov/api/PublicationPreview/exports/SDN_ADVANCED.XML", cfg.OFACSnapshotURL)
	assert.Greater(t, cfg.OFACDownloadMaxBytes, int64(0))
}

func TestLoad_ProviderDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable")

	cfg, err := Load()
	require.NoError(t, err)
	// Sane provider defaults; optional API keys absent is fine.
	assert.Equal(t, "https://api.gopluslabs.io", cfg.GoPlusBaseURL)
	assert.Equal(t, "", cfg.GoPlusAPIKey)
	assert.Equal(t, 30, cfg.GoPlusRatePerMin)
	assert.Equal(t, "https://intelapi.publicaml.org", cfg.PublicAMLBaseURL)
	assert.Equal(t, "", cfg.PublicAMLAPIKey)
	assert.Equal(t, 15, cfg.PublicAMLRatePerMin)
	assert.Equal(t, "https://api.chainabuse.com", cfg.ChainabuseBaseURL)
	assert.Equal(t, "", cfg.ChainabuseAPIKey)
	assert.Equal(t, 1, cfg.ChainabuseRatePerMin)
	assert.Equal(t, 15*time.Minute, cfg.EnrichmentCacheTTL)
	assert.Equal(t, time.Minute, cfg.EnrichmentNegativeTTL)
	assert.Equal(t, time.Minute, cfg.EnrichmentRefreshCooldown)
	assert.Equal(t, 25*time.Second, cfg.EnrichmentDeadline)
}

func TestLoad_ProviderOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable")
	t.Setenv("GOPLUS_RATE_PER_MIN", "10")
	t.Setenv("PUBLICAML_API_KEY", "test-key")
	t.Setenv("CHAINABUSE_API_KEY", "ca-key")
	t.Setenv("ENRICHMENT_DEADLINE", "10s")
	t.Setenv("ENRICHMENT_CACHE_TTL", "5m")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 10, cfg.GoPlusRatePerMin)
	assert.Equal(t, "test-key", cfg.PublicAMLAPIKey)
	assert.Equal(t, "ca-key", cfg.ChainabuseAPIKey)
	assert.Equal(t, 10*time.Second, cfg.EnrichmentDeadline)
	assert.Equal(t, 5*time.Minute, cfg.EnrichmentCacheTTL)
}
