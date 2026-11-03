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
