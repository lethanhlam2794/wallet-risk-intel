// Command api runs the REST server: config → db → redis → router, with
// graceful shutdown on SIGTERM/SIGINT.
package main

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

	"checkwallet/internal/apiclient"
	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/enrichment"
	"checkwallet/internal/httpserver"
	"checkwallet/internal/ingestion"
	"checkwallet/internal/label"
	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/platform/cache"
	"checkwallet/internal/platform/database"
	"checkwallet/internal/platform/logger"
	"checkwallet/internal/platform/metrics"
	"checkwallet/internal/probetest"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/alchemy"
	"checkwallet/internal/provider/ankr"
	"checkwallet/internal/provider/bitquery"
	"checkwallet/internal/provider/blockscout"
	"checkwallet/internal/provider/blockscoutpro"
	"checkwallet/internal/provider/chainabuse"
	"checkwallet/internal/provider/chainbase"
	"checkwallet/internal/provider/chainscan"
	"checkwallet/internal/provider/covalent"
	"checkwallet/internal/provider/goplus"
	"checkwallet/internal/provider/moralis"
	"checkwallet/internal/provider/nodereal"
	"checkwallet/internal/provider/ofac"
	"checkwallet/internal/provider/oklink"
	"checkwallet/internal/provider/publicaml"
	"checkwallet/internal/provider/resilience"
	"checkwallet/internal/provider/stablefreeze"
	"checkwallet/internal/provider/tatum"
	"checkwallet/internal/provider/tokenview"
	"checkwallet/internal/provider/walletattribution"
	"checkwallet/internal/providerconfig"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/screening"
	"checkwallet/internal/systemuser"
	"checkwallet/internal/tracing"
	"checkwallet/internal/transaction"
	"checkwallet/internal/wallet"
	"checkwallet/internal/warmindex"
)

func main() {
	if err := run(); err != nil {
		// Startup failures go to stderr; slog is not yet guaranteed.
		os.Stderr.WriteString("api: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load() // .env optional; real env always wins
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logger.New(cfg.LogLevel)
	ctx := context.Background()

	if err := database.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}
	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	rdb, err := cache.Connect(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	queries := db.New(pool)
	bootstrapAPIClients(ctx, cfg, queries, log)

	jwtSecret := []byte(cfg.AdminJWTSecret)
	if len(jwtSecret) == 0 {
		jwtSecret = randomSecret(32)
		log.Warn("ADMIN_JWT_SECRET unset — generated ephemeral secret; issued sessions die on restart")
	}
	userSvc := systemuser.New(queries, log)
	userSvc.EnsureSuperAdmin(ctx, randomPassword)

	resolver, err := loadResolver(ctx, queries)
	if err != nil {
		return err
	}

	// DB-managed provider config: any resolution error is boot-fatal and
	// names provider+field; an absent row reproduces the env-only path
	// byte-for-byte, which is also the built-in rollback for the feature.
	pcKeys, err := providerconfig.NewKeystore(cfg.ProviderConfigKEK)
	if err != nil {
		return err
	}
	pcRes, err := providerconfig.ResolveAll(ctx, cfg, queries, pcKeys,
		providerconfig.WithLogger(log))
	if err != nil {
		return err
	}

	ofacDl, err := ofac.NewDownloader(cfg.OFACSnapshotURL,
		ofac.WithMaxBytes(cfg.OFACDownloadMaxBytes))
	if err != nil {
		return err
	}
	wattrDl, err := walletattribution.NewDownloader(cfg.WattrDataBaseURL,
		wattrFiles(resolver), walletattribution.WithMaxBytes(cfg.WattrDownloadMaxBytes))
	if err != nil {
		return err
	}
	fetchers := map[string]provider.SnapshotSource{
		"ofac_sdn":                   ofacDl,
		walletattribution.SourceCode: wattrDl,
	}
	// stablecoin_freeze: wire the chain-scan fetcher off the resolved RPC
	// provider config. A gap (missing key, inactive row, ethereum
	// disabled) fails CLOSED — the admin trigger still lands a run, but
	// it lands FAILED ("no downloader configured"), never an empty
	// artifact that reads as a clean scan.
	fsc, err := stablefreeze.BuildScanner(queries, pcRes, cfg.FreezeScanRPCProvider,
		stablefreeze.Config{
			BlockRange:      cfg.FreezeScanBlockRange,
			MaxBlocksPerRun: cfg.FreezeScanMaxBlocksPerRun,
			Confirmations:   cfg.FreezeScanConfirmations,
		})
	if err != nil {
		log.Warn("stablefreeze scanner unavailable — runs will fail closed",
			"error", err)
	} else {
		fetchers[stablefreeze.SourceCode] = fsc
	}
	ingestSvc := ingestion.NewService(pool, resolver, fetchers, log)
	ingestSvc.SweepStaleRuns(ctx) // crashed workers leave no zombie RUNNING rows
	sanctionsSvc := sanctions.NewService(queries, rdb.R(), resolver, cfg.SanctionsStaleAfter)
	labelSvc := label.NewService(queries, rdb.R(), cfg.LabelsStaleAfter)

	m := metrics.New(metrics.LatestSnapshotQuery(queries), metrics.LabelsActiveQuery(queries))
	ingestSvc.SetMetrics(m)
	sanctionsSvc.SetMetrics(m)

	// Live-enrichment providers (spec §19 phase 4). goplus + publicaml are
	// always on (keyless tiers); chainabuse is constructed disabled without
	// CHAINABUSE_API_KEY — Enabled() gates traffic, the adapter still
	// reports its DISABLED status to consumers. Construction failures are
	// config errors → fatal at boot, never silently dropped.
	enrichSvc, err := buildEnrichment(cfg, pool, queries, rdb.R(), m, log)
	if err != nil {
		return err
	}

	screeningSvc := screening.New(screening.Deps{
		Pool: pool, Queries: queries,
		Sanctions:  sanctionsSvc,
		Labels:     labelSvc,
		Enrichment: enrichSvc,
		Wallets:    wallet.NewService(queries),
		Logger:     log,
		Metrics:    m,
	})

	// Transaction-indexing providers (spec §19 phase 6). Both adapters are
	// always constructed — a missing key yields Enabled()=false ⇒
	// DISABLED coverage, never a boot crash.
	txSvc, tracingSvc, txChains, err := buildTransactions(ctx, cfg, pool, queries,
		wallet.NewService(queries), sanctionsSvc, resolver, pcRes, m, log)
	if err != nil {
		return err
	}
	// Warm index producer (warm-index-queue v1): always wired — the Queue
	// no-ops when WARM_INDEX_ENABLED is off, so the interactive path is
	// byte-identical with the feature dark.
	tracingSvc.SetEnqueuer(warmindex.NewQueue(queries, cfg.WarmIndexEnabled, log), log)

	// Admin provider-config surface (db-provider-config phase 4): masked
	// reads, whitelisted writes with per-field audit rows.
	netCodes := make([]string, 0, len(resolver.List()))
	for _, n := range resolver.List() {
		netCodes = append(netCodes, n.Code)
	}
	pcAdmin := providerconfig.NewService(pool, queries, pcKeys, pcRes,
		chainCodes(txChains), netCodes)

	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb.R(), Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: queries,
		Admin:           ingestion.NewAdminHandler(ingestSvc, queries, log),
		Sanctions:       sanctionsSvc,
		Labels:          labelSvc,
		Enrichment:      enrichSvc,
		Screening:       screeningSvc,
		Metrics:         m,
		Transactions:    txSvc,
		Tracing:         tracingSvc,
		ProviderConfigs: pcAdmin,
		Clients:         apiclient.New(pool, queries, bootstrapKeyHashes(cfg), log),
		SystemUsers:     userSvc,
		AdminJWTSecret:  jwtSecret,
		AdminJWTTTL:     cfg.AdminJWTTTL,
		Probe:           probetest.NewService(cfg, queries, pcKeys),
	})

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: engine, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("api listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("api serve failed", "error", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// buildEnrichment constructs the three phase-4 providers with resilience
// metrics hooks and wraps them in the cache-first fan-out service. Provider
// keys are used for requests only — never logged.
func buildEnrichment(cfg *config.Config, pool *pgxpool.Pool, q *db.Queries,
	rdb *redis.Client, m *metrics.Metrics, log *slog.Logger) (*enrichment.Service, error) {

	hooksFor := func(source string) resilience.Hooks {
		onR, onE, onRL, onC := m.ProviderHooks(source)
		return resilience.Hooks{OnResult: onR, OnError: onE,
			OnRateLimited: onRL, OnCircuit: onC}
	}
	gp, err := goplus.New(cfg.GoPlusBaseURL, cfg.GoPlusAPIKey,
		cfg.GoPlusRatePerMin, goplus.WithHooks(hooksFor(goplus.SourceCode)))
	if err != nil {
		return nil, err
	}
	pa, err := publicaml.New(cfg.PublicAMLBaseURL, cfg.PublicAMLAPIKey,
		cfg.PublicAMLRatePerMin, publicaml.WithHooks(hooksFor(publicaml.SourceCode)))
	if err != nil {
		return nil, err
	}
	ca, err := chainabuse.New(cfg.ChainabuseBaseURL, cfg.ChainabuseAPIKey,
		cfg.ChainabuseRatePerMin, chainabuse.WithHooks(hooksFor(chainabuse.SourceCode)))
	if err != nil {
		return nil, err
	}
	if !ca.Enabled() {
		log.Info("chainabuse disabled: CHAINABUSE_API_KEY not configured")
	}
	return enrichment.New(
		[]provider.Provider{gp, pa, ca},
		pool, q, rdb, wallet.NewService(q), log, enrichment.Config{
			PositiveTTL:     cfg.EnrichmentCacheTTL,
			NegativeTTL:     cfg.EnrichmentNegativeTTL,
			RefreshCooldown: cfg.EnrichmentRefreshCooldown,
			Deadline:        cfg.EnrichmentDeadline,
			Retain:          25,
		}), nil
}

// buildTransactions constructs the chainscan + nodereal TxProviders and
// the indexing/tracing services. Missing API keys produce
// Enabled()=false — the adapters report DISABLED honestly (plan §3.2).
func buildTransactions(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, q *db.Queries,
	wallets *wallet.Service, sanctionsSvc *sanctions.Service,
	resolver *network.Resolver, pcRes *providerconfig.Resolver,
	m *metrics.Metrics, log *slog.Logger) (*transaction.Service, *tracing.Service,
	map[string][]provider.TxProvider, error) {

	hooksFor := func(source string) resilience.Hooks {
		onR, onE, onRL, onC := m.ProviderHooks(source)
		return resilience.Hooks{OnResult: onR, OnError: onE,
			OnRateLimited: onRL, OnCircuit: onC}
	}
	// resolved is the DB-over-env effective config for a provider —
	// adapters are built from it instead of the raw env fields.
	resolved := func(code string) *providerconfig.Resolved { return pcRes.Get(code) }
	cs, err := chainscan.New(resolved(chainscan.SourceCode).BaseURL,
		resolved(chainscan.SourceCode).APIKey,
		resolved(chainscan.SourceCode).EnabledChains,
		chainscan.WithHooks(hooksFor(chainscan.SourceCode)),
		chainscan.WithDurationFunc(m.ProviderDurationHook(chainscan.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	nr, err := nodereal.New(resolved(nodereal.SourceCode).BaseURL,
		resolved(nodereal.SourceCode).APIKey,
		nodereal.WithHooks(hooksFor(nodereal.SourceCode)),
		nodereal.WithDurationFunc(m.ProviderDurationHook(nodereal.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	alchemyAdapters := map[string]provider.TxProvider{}
	for _, code := range []string{alchemy.SourceCodeEth, alchemy.SourceCodeBsc} {
		a, err := alchemy.New(resolved(code).BaseURL,
			resolved(code).APIKey,
			resolved(code).EnabledChains,
			alchemy.WithSourceCode(code),
			alchemy.WithHooks(hooksFor(code)),
			alchemy.WithDurationFunc(m.ProviderDurationHook(code)))
		if err != nil {
			return nil, nil, nil, err
		}
		if resolved(code).APIKey == "" {
			log.Info(code + " disabled: no api key configured")
		}
		alchemyAdapters[code] = a
	}
	if resolved(chainscan.SourceCode).APIKey == "" {
		log.Info("chainscan disabled: no api key configured")
	}
	if resolved(nodereal.SourceCode).APIKey == "" {
		log.Info("nodereal disabled: no api key configured")
	}
	bs, err := blockscout.New(resolved(blockscout.SourceCode).BaseURL,
		resolved(blockscout.SourceCode).EnabledChains,
		blockscout.WithHooks(hooksFor(blockscout.SourceCode)),
		blockscout.WithDurationFunc(m.ProviderDurationHook(blockscout.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	bsp, err := blockscoutpro.New(resolved(blockscoutpro.SourceCode).BaseURL,
		resolved(blockscoutpro.SourceCode).APIKey,
		resolved(blockscoutpro.SourceCode).EnabledChains,
		blockscoutpro.WithHooks(hooksFor(blockscoutpro.SourceCode)),
		blockscoutpro.WithDurationFunc(m.ProviderDurationHook(blockscoutpro.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	if resolved(blockscoutpro.SourceCode).APIKey == "" {
		log.Info("blockscout_pro disabled: no api key configured")
	}
	an, err := ankr.New(resolved(ankr.SourceCode).BaseURL,
		resolved(ankr.SourceCode).APIKey,
		resolved(ankr.SourceCode).EnabledChains,
		ankr.WithHooks(hooksFor(ankr.SourceCode)),
		ankr.WithDurationFunc(m.ProviderDurationHook(ankr.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	cv, err := covalent.New(resolved(covalent.SourceCode).BaseURL,
		resolved(covalent.SourceCode).APIKey,
		resolved(covalent.SourceCode).EnabledChains,
		covalent.WithHooks(hooksFor(covalent.SourceCode)),
		covalent.WithDurationFunc(m.ProviderDurationHook(covalent.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	if resolved(ankr.SourceCode).APIKey == "" {
		log.Info("ankr disabled: no api key configured")
	}
	mo, err := moralis.New(resolved(moralis.SourceCode).BaseURL,
		resolved(moralis.SourceCode).APIKey,
		resolved(moralis.SourceCode).EnabledChains,
		moralis.WithHooks(hooksFor(moralis.SourceCode)),
		moralis.WithDurationFunc(m.ProviderDurationHook(moralis.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	if resolved(covalent.SourceCode).APIKey == "" {
		log.Info("covalent disabled: no api key configured")
	}
	if resolved(moralis.SourceCode).APIKey == "" {
		log.Info("moralis disabled: no api key configured")
	}
	ta, err := tatum.New(resolved(tatum.SourceCode).BaseURL,
		resolved(tatum.SourceCode).APIKey,
		resolved(tatum.SourceCode).EnabledChains,
		tatum.WithHooks(hooksFor(tatum.SourceCode)),
		tatum.WithDurationFunc(m.ProviderDurationHook(tatum.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	if resolved(tatum.SourceCode).APIKey == "" {
		log.Info("tatum disabled: no api key configured")
	}
	bq, err := bitquery.New(resolved(bitquery.SourceCode).BaseURL,
		resolved(bitquery.SourceCode).APIKey,
		resolved(bitquery.SourceCode).EnabledChains,
		bitquery.WithHooks(hooksFor(bitquery.SourceCode)),
		bitquery.WithDurationFunc(m.ProviderDurationHook(bitquery.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	if resolved(bitquery.SourceCode).APIKey == "" {
		log.Info("bitquery disabled: no api key configured")
	}
	ok, err := oklink.New(resolved(oklink.SourceCode).BaseURL,
		resolved(oklink.SourceCode).APIKey,
		resolved(oklink.SourceCode).EnabledChains,
		oklink.WithHooks(hooksFor(oklink.SourceCode)),
		oklink.WithDurationFunc(m.ProviderDurationHook(oklink.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	if resolved(oklink.SourceCode).APIKey == "" {
		log.Info("oklink disabled: no api key configured")
	}
	tv, err := tokenview.New(resolved(tokenview.SourceCode).BaseURL,
		resolved(tokenview.SourceCode).APIKey,
		resolved(tokenview.SourceCode).EnabledChains,
		tokenview.WithHooks(hooksFor(tokenview.SourceCode)),
		tokenview.WithDurationFunc(m.ProviderDurationHook(tokenview.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	if resolved(tokenview.SourceCode).APIKey == "" {
		log.Info("tokenview disabled: no api key configured")
	}
	cb, err := chainbase.New(resolved(chainbase.SourceCode).BaseURL,
		resolved(chainbase.SourceCode).APIKey,
		resolved(chainbase.SourceCode).EnabledChains,
		chainbase.WithHooks(hooksFor(chainbase.SourceCode)),
		chainbase.WithDurationFunc(m.ProviderDurationHook(chainbase.SourceCode)))
	if err != nil {
		return nil, nil, nil, err
	}
	if resolved(chainbase.SourceCode).APIKey == "" {
		log.Info("chainbase disabled: no api key configured")
	}

	// Registry of every selectable provider; TX_PROVIDER_<NET> resolves the
	// ordered failover chain per network.
	registry := map[string]provider.TxProvider{
		chainscan.SourceCode:     cs,
		nodereal.SourceCode:      nr,
		alchemy.SourceCodeEth:    alchemyAdapters[alchemy.SourceCodeEth],
		alchemy.SourceCodeBsc:    alchemyAdapters[alchemy.SourceCodeBsc],
		blockscout.SourceCode:    bs,
		blockscoutpro.SourceCode: bsp,
		ankr.SourceCode:          an,
		covalent.SourceCode:      cv,
		moralis.SourceCode:       mo,
		tatum.SourceCode:         ta,
		bitquery.SourceCode:      bq,
		oklink.SourceCode:        ok,
		tokenview.SourceCode:     tv,
		chainbase.SourceCode:     cb,
	}
	ethChain, err := selectTxProviderChain("TX_PROVIDER_ETH", cfg.TxProviderETH,
		chainscan.SourceCode, registry, log)
	if err != nil {
		return nil, nil, nil, err
	}
	bscChain, err := selectTxProviderChain("TX_PROVIDER_BSC", cfg.TxProviderBSC,
		nodereal.SourceCode, registry, log)
	if err != nil {
		return nil, nil, nil, err
	}

	budget := map[string]int{
		chainscan.SourceCode:     cfg.IndexingBudgetETH,
		nodereal.SourceCode:      cfg.IndexingBudgetBSC,
		alchemy.SourceCodeEth:    cfg.IndexingBudgetAlchemy,
		alchemy.SourceCodeBsc:    cfg.IndexingBudgetAlchemy,
		blockscout.SourceCode:    cfg.IndexingBudgetBlockscout,
		blockscoutpro.SourceCode: cfg.IndexingBudgetBlockscoutPro,
		ankr.SourceCode:          cfg.IndexingBudgetAnkr,
		covalent.SourceCode:      cfg.IndexingBudgetCovalent,
		moralis.SourceCode:       cfg.IndexingBudgetMoralis,
		tatum.SourceCode:         cfg.IndexingBudgetTatum,
		bitquery.SourceCode:      cfg.IndexingBudgetBitquery,
		oklink.SourceCode:        cfg.IndexingBudgetOklink,
		tokenview.SourceCode:     cfg.IndexingBudgetTokenview,
		chainbase.SourceCode:     cfg.IndexingBudgetChainbase,
	}
	// Boot checks (plan invariants): every registered provider must resolve
	// a data_sources row — a typo'd Name() silently disables the
	// provider-switch reset — and must carry a positive budget — a typo'd
	// TXINDEX_BUDGET_* env would leave the request pool unlimited.
	for _, p := range registry {
		if _, err := q.GetSourceByCode(ctx, p.Name()); err != nil {
			return nil, nil, nil, fmt.Errorf("tx provider %s: data_sources row missing: %w", p.Name(), err)
		}
		if budget[p.Name()] <= 0 {
			return nil, nil, nil, fmt.Errorf("tx provider %s: budget must be > 0", p.Name())
		}
	}

	txSvc := transaction.New(pool, q,
		map[string][]provider.TxProvider{"ethereum": ethChain, "bsc": bscChain},
		wallets, transaction.Config{
			FreshTTL:     cfg.IndexingFreshTTL,
			StaleAfter:   cfg.IndexingStaleAfter,
			NegativeTTL:  cfg.EnrichmentNegativeTTL,
			PageSize:     cfg.IndexingPageSize,
			MaxPagesHop0: cfg.IndexingMaxPagesHop0,
			MaxPagesHopN: cfg.IndexingMaxPagesHopN,
			MaxPagesHop3: cfg.IndexingMaxPagesHop3,
			ReorgTail: map[string]int64{
				"ethereum": cfg.IndexingReorgTailETH,
				"bsc":      cfg.IndexingReorgTailBSC,
			},
			WindowCap: map[string]int64{"bsc": cfg.IndexingWindowCapBSC},
			Budget:    budget,
		}, log)
	// Runtime kill-switch (db-provider-config phase 3): provider_configs
	// .status AND'd with data_sources.is_active, TTL-cached (~30s) — an
	// inactive member is skipped exactly like a disabled adapter.
	txSvc.SetStatusChecker(providerconfig.NewStatusCache(q, 30*time.Second,
		providerconfig.WithLogger(log)))
	tracingSvc := tracing.New(q, txSvc, sanctionsSvc, resolver, tracing.Config{
		MaxNodes: cfg.TracingMaxNodes,
		MaxNodesPerDepth: [4]int{
			cfg.TracingMaxNodes2, cfg.TracingMaxNodes3,
			cfg.TracingMaxNodes4, cfg.TracingMaxNodes5,
		},
		MaxEdges:    cfg.TracingMaxEdges,
		Deadline:    cfg.TracingDeadline,
		ResponseCap: cfg.TracingResponseCap,
	})
	tracingSvc.SetMetrics(m)
	chains := map[string][]provider.TxProvider{"ethereum": ethChain, "bsc": bscChain}
	return txSvc, tracingSvc, chains, nil
}

// chainCodes flattens the resolved failover chains to provider-code lists
// for the admin last-active-member warning.
func chainCodes(chains map[string][]provider.TxProvider) map[string][]string {
	out := make(map[string][]string, len(chains))
	for net, chain := range chains {
		for _, p := range chain {
			out[net] = append(out[net], p.Name())
		}
	}
	return out
}

// selectTxProviderChain resolves TX_PROVIDER_<NET> into the ordered
// failover chain: unset → a single-entry chain at the historical default
// (byte-identical single-adapter behavior); set → every comma-separated
// entry must name a registered provider, in the listed failover order
// (unknown names are rejected at boot, never silently misrouted).
func selectTxProviderChain(envName, raw, def string,
	registry map[string]provider.TxProvider, log *slog.Logger) ([]provider.TxProvider, error) {
	if strings.TrimSpace(raw) == "" {
		return []provider.TxProvider{registry[def]}, nil
	}
	var chain []provider.TxProvider
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("%s: empty provider entry", envName)
		}
		p, ok := registry[name]
		if !ok {
			return nil, fmt.Errorf("%s: unknown tx provider %q", envName, name)
		}
		chain = append(chain, p)
	}
	if len(chain) > 1 {
		names := make([]string, 0, len(chain))
		for _, p := range chain {
			names = append(names, p.Name())
		}
		log.Info("tx provider failover chain resolved", "env", envName, "order", names)
	}
	return chain, nil
}

// bootstrapAPIClients upserts dev bootstrap keys from env. Removing the env
// var does NOT deactivate a previously seeded client — rotation requires
// explicit deactivation (documented in README). Keys are hashed at rest and
// never logged.
func bootstrapAPIClients(ctx context.Context, cfg *config.Config, q *db.Queries, log *slog.Logger) {
	seed := func(raw, name, role string) {
		if raw == "" {
			return
		}
		if _, err := q.UpsertAPIClient(ctx, db.UpsertAPIClientParams{
			Name: name, KeyHash: middleware.HashAPIKey(raw), Role: role,
		}); err != nil {
			log.Error("bootstrap api client failed", "name", name, "error", err)
		}
	}
	seed(cfg.BootstrapAdminKey, "bootstrap-admin", "admin")
	seed(cfg.BootstrapReaderKey, "bootstrap-reader", "reader")
}

// bootstrapKeyHashes returns the SHA-256 digests of configured bootstrap
// keys. Rows seeded by API_BOOTSTRAP_* are resurrected by the boot
// upsert, so PATCHes against them carry the env_managed warning.
func bootstrapKeyHashes(cfg *config.Config) []string {
	var out []string
	for _, raw := range []string{cfg.BootstrapAdminKey, cfg.BootstrapReaderKey} {
		if raw != "" {
			out = append(out, middleware.HashAPIKey(raw))
		}
	}
	return out
}

const passwordAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randomPassword returns a 24-char base62 password from crypto/rand.
func randomPassword() string {
	return string(randomSecret(24))
}

func randomSecret(n int) []byte {
	buf := make([]byte, n)
	raw := make([]byte, n)
	if _, err := crand.Read(raw); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	for i := range buf {
		buf[i] = passwordAlphabet[int(raw[i])%len(passwordAlphabet)]
	}
	return buf
}

func loadResolver(ctx context.Context, q *db.Queries) (*network.Resolver, error) {
	rows, err := q.ListActiveNetworks(ctx)
	if err != nil {
		return nil, err
	}
	nets := make([]network.Network, 0, len(rows))
	for _, r := range rows {
		nets = append(nets, network.Network{
			ID: r.ID, Namespace: r.Namespace, ChainReference: r.ChainReference, Code: r.Code, Name: r.Name,
		})
	}
	return network.NewResolver(nets), nil
}

// wattrFiles maps each registered network to its upstream CSV artifact
// (data/{code}.csv) — the dataset layout is one file per network.
func wattrFiles(resolver *network.Resolver) []string {
	files := []string{}
	for _, n := range resolver.List() {
		files = append(files, n.Code+".csv")
	}
	return files
}
