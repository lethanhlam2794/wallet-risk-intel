// Package config loads service configuration from environment variables.
// No secrets may be hardcoded; everything comes from env (spec §16).
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const defaultOFACSnapshotURL = "https://sanctionslistservice.ofac.treas.gov/api/PublicationPreview/exports/SDN_ADVANCED.XML"
const defaultWattrDataBaseURL = "https://raw.githubusercontent.com/prettydeath/wallet-attribution/main/data"
const defaultGoPlusBaseURL = "https://api.gopluslabs.io"
const defaultPublicAMLBaseURL = "https://intelapi.publicaml.org"
const defaultChainabuseBaseURL = "https://api.chainabuse.com"

// Config is the runtime configuration for api and worker processes.
type Config struct {
	HTTPAddr    string
	DatabaseURL string
	RedisURL    string
	LogLevel    string

	OFACSnapshotURL      string
	OFACDownloadMaxBytes int64
	OFACDownloadTimeout  time.Duration

	WattrDataBaseURL      string
	WattrDownloadMaxBytes int64

	SanctionsStaleAfter time.Duration
	LabelsStaleAfter    time.Duration

	RateLimitRequests int
	RateLimitWindow   time.Duration
	RateLimitAnon     int

	// Live enrichment providers (spec §4.4–4.6, phase 4). Optional API keys
	// may be absent; absent keys never disable goplus/publicaml (keyless
	// tiers) but do disable chainabuse.
	GoPlusBaseURL        string
	GoPlusAPIKey         string
	GoPlusRatePerMin     int
	PublicAMLBaseURL     string
	PublicAMLAPIKey      string
	PublicAMLRatePerMin  int
	ChainabuseBaseURL    string
	ChainabuseAPIKey     string
	ChainabuseRatePerMin int

	EnrichmentCacheTTL        time.Duration // positive provider cache TTL (spec §14)
	EnrichmentNegativeTTL     time.Duration // degraded/negative cache TTL
	EnrichmentRefreshCooldown time.Duration // refresh=1 quota guard cooldown (F3)
	EnrichmentDeadline        time.Duration // provider fan-out parent deadline (F2)

	BootstrapAdminKey  string
	BootstrapReaderKey string
}

// Load reads configuration from the environment and fails fast when a
// required value is missing.
func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:                  envOr("HTTP_ADDR", ":8080"),
		DatabaseURL:               os.Getenv("DATABASE_URL"),
		RedisURL:                  envOr("REDIS_URL", "redis://127.0.0.1:6380/0"),
		LogLevel:                  envOr("LOG_LEVEL", "info"),
		OFACSnapshotURL:           envOr("OFAC_SNAPSHOT_URL", defaultOFACSnapshotURL),
		WattrDataBaseURL:          envOr("WATTR_DATA_BASE_URL", defaultWattrDataBaseURL),
		SanctionsStaleAfter:       envDur("SANCTIONS_STALE_AFTER", 48*time.Hour),
		LabelsStaleAfter:          envDur("LABELS_STALE_AFTER", 168*time.Hour),
		RateLimitWindow:           envDur("RATE_LIMIT_WINDOW", time.Minute),
		GoPlusBaseURL:             envOr("GOPLUS_BASE_URL", defaultGoPlusBaseURL),
		GoPlusAPIKey:              os.Getenv("GOPLUS_API_KEY"),
		PublicAMLBaseURL:          envOr("PUBLICAML_BASE_URL", defaultPublicAMLBaseURL),
		PublicAMLAPIKey:           os.Getenv("PUBLICAML_API_KEY"),
		ChainabuseBaseURL:         envOr("CHAINABUSE_BASE_URL", defaultChainabuseBaseURL),
		ChainabuseAPIKey:          os.Getenv("CHAINABUSE_API_KEY"),
		EnrichmentCacheTTL:        envDur("ENRICHMENT_CACHE_TTL", 15*time.Minute),
		EnrichmentNegativeTTL:     envDur("ENRICHMENT_NEGATIVE_TTL", time.Minute),
		EnrichmentRefreshCooldown: envDur("ENRICHMENT_REFRESH_COOLDOWN", time.Minute),
		EnrichmentDeadline:        envDur("ENRICHMENT_DEADLINE", 25*time.Second),
		BootstrapAdminKey:         os.Getenv("API_BOOTSTRAP_ADMIN_KEY"),
		BootstrapReaderKey:        os.Getenv("API_BOOTSTRAP_READER_KEY"),
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}

	var err error
	if cfg.OFACDownloadMaxBytes, err = envInt64("OFAC_DOWNLOAD_MAX_BYTES", 512<<20); err != nil {
		return nil, err
	}
	if cfg.WattrDownloadMaxBytes, err = envInt64("WATTR_DOWNLOAD_MAX_BYTES", 32<<20); err != nil {
		return nil, err
	}
	if cfg.OFACDownloadTimeout, err = time.ParseDuration(envOr("OFAC_DOWNLOAD_TIMEOUT", "10m")); err != nil {
		return nil, fmt.Errorf("config: OFAC_DOWNLOAD_TIMEOUT: %w", err)
	}
	if cfg.RateLimitRequests, err = envInt("RATE_LIMIT_REQUESTS", 120); err != nil {
		return nil, err
	}
	if cfg.RateLimitAnon, err = envInt("RATE_LIMIT_ANON", 30); err != nil {
		return nil, err
	}
	if cfg.GoPlusRatePerMin, err = envInt("GOPLUS_RATE_PER_MIN", 30); err != nil {
		return nil, err
	}
	if cfg.PublicAMLRatePerMin, err = envInt("PUBLICAML_RATE_PER_MIN", 15); err != nil {
		return nil, err
	}
	if cfg.ChainabuseRatePerMin, err = envInt("CHAINABUSE_RATE_PER_MIN", 1); err != nil {
		return nil, err
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDur(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envInt64(key string, fallback int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return n, nil
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return n, nil
}
