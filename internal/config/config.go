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

	// Transaction indexing providers (spec §11, phase 6).
	EtherscanAPIKey        string
	ChainscanBaseURL       string
	ChainscanEnabledChains string // comma list; bsc is paid-tier → off by default
	NoderealAPIKey         string
	NoderealBaseURL        string

	// Per-network adapter selection (tx-provider expansion): comma list of
	// registered provider codes; first entry serves. Unset → current default.
	TxProviderETH string
	TxProviderBSC string

	// Expansion adapters (phases 01–11). Keyless providers (blockscout)
	// have no *_API_KEY — gating is by enabled-chains only.
	AlchemyAPIKey         string
	AlchemyBaseURL        string // empty → documented per-network endpoints
	AlchemyEnabledChains  string
	IndexingBudgetAlchemy int

	// Blockscout: keyless public REST — no *_API_KEY; enabled-chains only.
	BlockscoutBaseURL        string
	BlockscoutEnabledChains  string
	IndexingBudgetBlockscout int

	AnkrAPIKey         string
	AnkrBaseURL        string
	AnkrEnabledChains  string
	IndexingBudgetAnkr int

	IndexingFreshTTL     time.Duration // fresh-serve horizon for index state
	IndexingStaleAfter   time.Duration // stale 'indexing' claim retry horizon
	IndexingPageSize     int
	IndexingMaxPagesHop0 int   // stream-pair steps for the queried wallet
	IndexingMaxPagesHopN int   // stream-pair steps for frontier-2 nodes
	IndexingMaxPagesHop3 int   // stream-pair steps for frontier ≥3 nodes
	IndexingReorgTailETH int64 // tail overlap revalidating mutable columns
	IndexingReorgTailBSC int64
	IndexingWindowCapBSC int64 // upstream hard span cap (nodereal 100k)
	IndexingBudgetETH    int   // max_provider_requests pool (chainscan)
	IndexingBudgetBSC    int   // max_provider_requests pool (nodereal)

	TracingMaxNodes    int // legacy depth-2 fallback (TRACING_MAX_NODES)
	TracingMaxNodes2   int // frontier-2 expansion cap
	TracingMaxNodes3   int
	TracingMaxNodes4   int
	TracingMaxNodes5   int
	TracingMaxEdges    int
	TracingDeadline    time.Duration
	TracingResponseCap int

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
		EtherscanAPIKey:           os.Getenv("ETHERSCAN_API_KEY"),
		ChainscanBaseURL:          envOr("CHAINSCAN_BASE_URL", "https://api.etherscan.io/v2/api"),
		ChainscanEnabledChains:    envOr("CHAINSCAN_ENABLED_CHAINS", "ethereum"),
		NoderealAPIKey:            os.Getenv("NODEREAL_API_KEY"),
		NoderealBaseURL:           envOr("NODEREAL_BASE_URL", "https://bsc-mainnet.nodereal.io/v1"),
		TxProviderETH:             os.Getenv("TX_PROVIDER_ETH"),
		TxProviderBSC:             os.Getenv("TX_PROVIDER_BSC"),
		AlchemyAPIKey:             os.Getenv("ALCHEMY_API_KEY"),
		AlchemyBaseURL:            os.Getenv("ALCHEMY_BASE_URL"),
		AlchemyEnabledChains:      envOr("ALCHEMY_ENABLED_CHAINS", "ethereum,bsc"),
		BlockscoutBaseURL:         envOr("BLOCKSCOUT_BASE_URL", "https://eth.blockscout.com"),
		BlockscoutEnabledChains:   envOr("BLOCKSCOUT_ENABLED_CHAINS", "ethereum"),
		AnkrAPIKey:                os.Getenv("ANKR_API_KEY"),
		AnkrBaseURL:               envOr("ANKR_BASE_URL", "https://rpc.ankr.com/multichain"),
		AnkrEnabledChains:         envOr("ANKR_ENABLED_CHAINS", "ethereum,bsc"),
		IndexingFreshTTL:          envDur("TXINDEX_FRESH_TTL", 10*time.Minute),
		IndexingStaleAfter:        envDur("TXINDEX_STALE_AFTER", 15*time.Minute),
		IndexingWindowCapBSC:      envInt64Default("TXINDEX_WINDOW_CAP_BSC", 100000),
		TracingDeadline:           envDur("TRACING_DEADLINE", 60*time.Second),
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
	if cfg.IndexingPageSize, err = envInt("TXINDEX_PAGE_SIZE", 1000); err != nil {
		return nil, err
	}
	if cfg.IndexingMaxPagesHop0, err = envInt("TXINDEX_MAX_PAGES_HOP0", 5); err != nil {
		return nil, err
	}
	if cfg.IndexingMaxPagesHopN, err = envInt("TXINDEX_MAX_PAGES_HOPN", 2); err != nil {
		return nil, err
	}
	if cfg.IndexingMaxPagesHop3, err = envInt("TXINDEX_MAX_PAGES_HOP3", 1); err != nil {
		return nil, err
	}
	if cfg.IndexingReorgTailETH, err = envInt64("TXINDEX_REORG_TAIL_ETH", 64); err != nil {
		return nil, err
	}
	if cfg.IndexingReorgTailBSC, err = envInt64("TXINDEX_REORG_TAIL_BSC", 100); err != nil {
		return nil, err
	}
	if cfg.IndexingBudgetETH, err = envInt("TXINDEX_BUDGET_ETH", 32); err != nil {
		return nil, err
	}
	if cfg.IndexingBudgetBSC, err = envInt("TXINDEX_BUDGET_BSC", 8); err != nil {
		return nil, err
	}
	if cfg.IndexingBudgetAlchemy, err = envInt("TXINDEX_BUDGET_ALCHEMY", 8); err != nil {
		return nil, err
	}
	if cfg.IndexingBudgetBlockscout, err = envInt("TXINDEX_BUDGET_BLOCKSCOUT", 8); err != nil {
		return nil, err
	}
	if cfg.IndexingBudgetAnkr, err = envInt("TXINDEX_BUDGET_ANKR", 8); err != nil {
		return nil, err
	}
	if cfg.TracingMaxNodes, err = envInt("TRACING_MAX_NODES", 50); err != nil {
		return nil, err
	}
	// Per-depth expansion caps (phase 7): TRACING_MAX_NODES_2 falls back
	// to the legacy TRACING_MAX_NODES when unset.
	if cfg.TracingMaxNodes2, err = envInt("TRACING_MAX_NODES_2", cfg.TracingMaxNodes); err != nil {
		return nil, err
	}
	if cfg.TracingMaxNodes3, err = envInt("TRACING_MAX_NODES_3", 25); err != nil {
		return nil, err
	}
	if cfg.TracingMaxNodes4, err = envInt("TRACING_MAX_NODES_4", 12); err != nil {
		return nil, err
	}
	if cfg.TracingMaxNodes5, err = envInt("TRACING_MAX_NODES_5", 6); err != nil {
		return nil, err
	}
	if cfg.TracingMaxEdges, err = envInt("TRACING_MAX_EDGES", 5000); err != nil {
		return nil, err
	}
	if cfg.TracingResponseCap, err = envInt("TRACING_RESPONSE_CAP", 256<<10); err != nil {
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

// envInt64Default is the non-fatal variant of envInt64 for bounded-size
// knobs — a malformed value falls back rather than blocking boot.
func envInt64Default(key string, fallback int64) int64 {
	n, err := envInt64(key, fallback)
	if err != nil {
		return fallback
	}
	return n
}
