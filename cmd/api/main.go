// Command api runs the REST server: config → db → redis → router, with
// graceful shutdown on SIGTERM/SIGINT.
package main

import (
	"context"
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
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/alchemy"
	"checkwallet/internal/provider/ankr"
	"checkwallet/internal/provider/bitquery"
	"checkwallet/internal/provider/blockscout"
	"checkwallet/internal/provider/chainabuse"
	"checkwallet/internal/provider/chainscan"
	"checkwallet/internal/provider/covalent"
	"checkwallet/internal/provider/goplus"
	"checkwallet/internal/provider/moralis"
	"checkwallet/internal/provider/nodereal"
	"checkwallet/internal/provider/ofac"
	"checkwallet/internal/provider/oklink"
	"checkwallet/internal/provider/publicaml"
	"checkwallet/internal/provider/resilience"
	"checkwallet/internal/provider/tatum"
	"checkwallet/internal/provider/walletattribution"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/screening"
	"checkwallet/internal/tracing"
	"checkwallet/internal/transaction"
	"checkwallet/internal/wallet"
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

	resolver, err := loadResolver(ctx, queries)
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
	txSvc, tracingSvc, err := buildTransactions(ctx, cfg, pool, queries,
		wallet.NewService(queries), sanctionsSvc, resolver, m, log)
	if err != nil {
		return err
	}

	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb.R(), Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: queries,
		Admin:        ingestion.NewAdminHandler(ingestSvc, queries, log),
		Sanctions:    sanctionsSvc,
		Labels:       labelSvc,
		Enrichment:   enrichSvc,
		Screening:    screeningSvc,
		Metrics:      m,
		Transactions: txSvc,
		Tracing:      tracingSvc,
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
	resolver *network.Resolver, m *metrics.Metrics, log *slog.Logger) (*transaction.Service, *tracing.Service, error) {

	hooksFor := func(source string) resilience.Hooks {
		onR, onE, onRL, onC := m.ProviderHooks(source)
		return resilience.Hooks{OnResult: onR, OnError: onE,
			OnRateLimited: onRL, OnCircuit: onC}
	}
	cs, err := chainscan.New(cfg.ChainscanBaseURL, cfg.EtherscanAPIKey,
		strings.Split(cfg.ChainscanEnabledChains, ","),
		chainscan.WithHooks(hooksFor(chainscan.SourceCode)),
		chainscan.WithDurationFunc(m.ProviderDurationHook(chainscan.SourceCode)))
	if err != nil {
		return nil, nil, err
	}
	nr, err := nodereal.New(cfg.NoderealBaseURL, cfg.NoderealAPIKey,
		nodereal.WithHooks(hooksFor(nodereal.SourceCode)),
		nodereal.WithDurationFunc(m.ProviderDurationHook(nodereal.SourceCode)))
	if err != nil {
		return nil, nil, err
	}
	al, err := alchemy.New(cfg.AlchemyBaseURL, cfg.AlchemyAPIKey,
		strings.Split(cfg.AlchemyEnabledChains, ","),
		alchemy.WithHooks(hooksFor(alchemy.SourceCode)),
		alchemy.WithDurationFunc(m.ProviderDurationHook(alchemy.SourceCode)))
	if err != nil {
		return nil, nil, err
	}
	if cfg.EtherscanAPIKey == "" {
		log.Info("chainscan disabled: ETHERSCAN_API_KEY not configured")
	}
	if cfg.NoderealAPIKey == "" {
		log.Info("nodereal disabled: NODEREAL_API_KEY not configured")
	}
	bs, err := blockscout.New(cfg.BlockscoutBaseURL,
		strings.Split(cfg.BlockscoutEnabledChains, ","),
		blockscout.WithHooks(hooksFor(blockscout.SourceCode)),
		blockscout.WithDurationFunc(m.ProviderDurationHook(blockscout.SourceCode)))
	if err != nil {
		return nil, nil, err
	}
	an, err := ankr.New(cfg.AnkrBaseURL, cfg.AnkrAPIKey,
		strings.Split(cfg.AnkrEnabledChains, ","),
		ankr.WithHooks(hooksFor(ankr.SourceCode)),
		ankr.WithDurationFunc(m.ProviderDurationHook(ankr.SourceCode)))
	if err != nil {
		return nil, nil, err
	}
	if cfg.AlchemyAPIKey == "" {
		log.Info("alchemy disabled: ALCHEMY_API_KEY not configured")
	}
	cv, err := covalent.New(cfg.CovalentBaseURL, cfg.CovalentAPIKey,
		strings.Split(cfg.CovalentEnabledChains, ","),
		covalent.WithHooks(hooksFor(covalent.SourceCode)),
		covalent.WithDurationFunc(m.ProviderDurationHook(covalent.SourceCode)))
	if err != nil {
		return nil, nil, err
	}
	if cfg.AnkrAPIKey == "" {
		log.Info("ankr disabled: ANKR_API_KEY not configured")
	}
	mo, err := moralis.New(cfg.MoralisBaseURL, cfg.MoralisAPIKey,
		strings.Split(cfg.MoralisEnabledChains, ","),
		moralis.WithHooks(hooksFor(moralis.SourceCode)),
		moralis.WithDurationFunc(m.ProviderDurationHook(moralis.SourceCode)))
	if err != nil {
		return nil, nil, err
	}
	if cfg.CovalentAPIKey == "" {
		log.Info("covalent disabled: COVALENT_API_KEY not configured")
	}
	if cfg.MoralisAPIKey == "" {
		log.Info("moralis disabled: MORALIS_API_KEY not configured")
	}
	ta, err := tatum.New(cfg.TatumBaseURL, cfg.TatumAPIKey,
		strings.Split(cfg.TatumEnabledChains, ","),
		tatum.WithHooks(hooksFor(tatum.SourceCode)),
		tatum.WithDurationFunc(m.ProviderDurationHook(tatum.SourceCode)))
	if err != nil {
		return nil, nil, err
	}
	if cfg.TatumAPIKey == "" {
		log.Info("tatum disabled: TATUM_API_KEY not configured")
	}
	bq, err := bitquery.New(cfg.BitqueryBaseURL, cfg.BitqueryAPIKey,
		strings.Split(cfg.BitqueryEnabledChains, ","),
		bitquery.WithHooks(hooksFor(bitquery.SourceCode)),
		bitquery.WithDurationFunc(m.ProviderDurationHook(bitquery.SourceCode)))
	if err != nil {
		return nil, nil, err
	}
	if cfg.BitqueryAPIKey == "" {
		log.Info("bitquery disabled: BITQUERY_API_KEY not configured")
	}
	ok, err := oklink.New(cfg.OklinkBaseURL, cfg.OklinkAPIKey,
		strings.Split(cfg.OklinkEnabledChains, ","),
		oklink.WithHooks(hooksFor(oklink.SourceCode)),
		oklink.WithDurationFunc(m.ProviderDurationHook(oklink.SourceCode)))
	if err != nil {
		return nil, nil, err
	}
	if cfg.OklinkAPIKey == "" {
		log.Info("oklink disabled: OKLINK_API_KEY not configured")
	}

	// Registry of every selectable provider; TX_PROVIDER_<NET> picks the
	// serving adapter per network (first entry — failover lands phase-12).
	registry := map[string]provider.TxProvider{
		chainscan.SourceCode:  cs,
		nodereal.SourceCode:   nr,
		alchemy.SourceCode:    al,
		blockscout.SourceCode: bs,
		ankr.SourceCode:       an,
		covalent.SourceCode:   cv,
		moralis.SourceCode:    mo,
		tatum.SourceCode:      ta,
		bitquery.SourceCode:   bq,
		oklink.SourceCode:     ok,
	}
	ethAdapter, err := selectTxProvider("TX_PROVIDER_ETH", cfg.TxProviderETH,
		chainscan.SourceCode, registry, log)
	if err != nil {
		return nil, nil, err
	}
	bscAdapter, err := selectTxProvider("TX_PROVIDER_BSC", cfg.TxProviderBSC,
		nodereal.SourceCode, registry, log)
	if err != nil {
		return nil, nil, err
	}

	budget := map[string]int{
		chainscan.SourceCode:  cfg.IndexingBudgetETH,
		nodereal.SourceCode:   cfg.IndexingBudgetBSC,
		alchemy.SourceCode:    cfg.IndexingBudgetAlchemy,
		blockscout.SourceCode: cfg.IndexingBudgetBlockscout,
		ankr.SourceCode:       cfg.IndexingBudgetAnkr,
		covalent.SourceCode:   cfg.IndexingBudgetCovalent,
		moralis.SourceCode:    cfg.IndexingBudgetMoralis,
		tatum.SourceCode:      cfg.IndexingBudgetTatum,
		bitquery.SourceCode:   cfg.IndexingBudgetBitquery,
		oklink.SourceCode:     cfg.IndexingBudgetOklink,
	}
	// Boot checks (plan invariants): every registered provider must resolve
	// a data_sources row — a typo'd Name() silently disables the
	// provider-switch reset — and must carry a positive budget — a typo'd
	// TXINDEX_BUDGET_* env would leave the request pool unlimited.
	for _, p := range registry {
		if _, err := q.GetSourceByCode(ctx, p.Name()); err != nil {
			return nil, nil, fmt.Errorf("tx provider %s: data_sources row missing: %w", p.Name(), err)
		}
		if budget[p.Name()] <= 0 {
			return nil, nil, fmt.Errorf("tx provider %s: budget must be > 0", p.Name())
		}
	}

	txSvc := transaction.New(pool, q,
		map[string]provider.TxProvider{"ethereum": ethAdapter, "bsc": bscAdapter},
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
	return txSvc, tracingSvc, nil
}

// selectTxProvider resolves TX_PROVIDER_<NET>: unset → the historical
// default adapter; set → the first comma-separated entry must name a
// registered provider (unknown names are rejected at boot, never silently
// misrouted). Extra entries are validated then ignored until phase-12
// failover — warned, not dropped silently.
func selectTxProvider(envName, raw, def string,
	registry map[string]provider.TxProvider, log *slog.Logger) (provider.TxProvider, error) {
	if strings.TrimSpace(raw) == "" {
		return registry[def], nil
	}
	var first provider.TxProvider
	var extras int
	for i, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("%s: empty provider entry", envName)
		}
		p, ok := registry[name]
		if !ok {
			return nil, fmt.Errorf("%s: unknown tx provider %q", envName, name)
		}
		if i == 0 {
			first = p
		} else {
			extras++
		}
	}
	if extras > 0 {
		log.Warn("tx provider selector: extra entries validated but ignored until failover lands",
			"env", envName, "serving", first.Name(), "ignored", extras)
	}
	return first, nil
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
