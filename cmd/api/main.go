// Command api runs the REST server: config → db → redis → router, with
// graceful shutdown on SIGTERM/SIGINT.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	"checkwallet/internal/provider/chainabuse"
	"checkwallet/internal/provider/goplus"
	"checkwallet/internal/provider/ofac"
	"checkwallet/internal/provider/publicaml"
	"checkwallet/internal/provider/resilience"
	"checkwallet/internal/provider/walletattribution"
	"checkwallet/internal/sanctions"
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

	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb.R(), Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: queries,
		Admin:      ingestion.NewAdminHandler(ingestSvc, queries, log),
		Sanctions:  sanctionsSvc,
		Labels:     labelSvc,
		Enrichment: enrichSvc,
		Metrics:    m,
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
