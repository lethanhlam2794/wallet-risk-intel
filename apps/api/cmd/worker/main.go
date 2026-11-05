// Command worker runs a single snapshot ingestion synchronously:
//
//	go run ./cmd/worker -source ofac_sdn              # download + ingest
//	go run ./cmd/worker -source ofac_sdn -file f.xml  # ingest a local file
//
//	or, in warmdrain mode, polls the warm index queue:
//
//	go run ./cmd/worker -mode warmdrain               # WARM_INDEX_ENABLED=1
//
// Ingest exits non-zero unless the run reaches COMPLETED; warmdrain runs
// until SIGINT/SIGTERM and always exits 0 on clean shutdown.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/joho/godotenv"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/ingestion"
	"checkwallet/internal/network"
	"checkwallet/internal/platform/cache"
	"checkwallet/internal/platform/database"
	"checkwallet/internal/platform/logger"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/ofac"
	"checkwallet/internal/provider/walletattribution"
	"checkwallet/internal/providerconfig"
	"checkwallet/internal/wallet"
	"checkwallet/internal/warmindex"
)

func main() {
	mode := flag.String("mode", "ingest", "worker mode: ingest|warmdrain")
	source := flag.String("source", "ofac_sdn", "data source code to ingest")
	file := flag.String("file", "", "local snapshot file (skips download)")
	flag.Parse()

	var err error
	switch *mode {
	case "ingest":
		err = run(*source, *file)
	case "warmdrain":
		err = runWarmDrain()
	default:
		err = fmt.Errorf("unknown -mode %q", *mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run(source, file string) error {
	_ = godotenv.Load() // .env optional; real env always wins
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logger.New(cfg.LogLevel)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.OFACDownloadTimeout+30*time.Minute)
	defer cancel()

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
	resolver, err := loadResolver(ctx, queries)
	if err != nil {
		return err
	}

	fetchers := map[string]provider.SnapshotSource{}
	if file == "" {
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
		fetchers["ofac_sdn"] = ofacDl
		fetchers[walletattribution.SourceCode] = wattrDl
	}

	svc := ingestion.NewService(pool, resolver, fetchers, log)
	svc.SweepStaleRuns(ctx)
	runID, err := svc.RunSync(ctx, ingestion.RunRequest{
		SourceCode: source, TriggeredBy: "worker", FilePath: file,
	})
	if err != nil {
		return err
	}

	run, err := queries.GetIngestionRun(context.Background(),
		pgtype.UUID{Bytes: runID, Valid: true})
	if err != nil {
		return err
	}
	fmt.Printf("run_id=%s status=%s version=%s\n",
		runID, run.Status, run.SnapshotVersion.String)
	if run.Status != "COMPLETED" {
		return fmt.Errorf("run %s: %s", run.Status, run.ErrorMessage.String)
	}
	return nil
}

// runWarmDrain is the warm-index drain loop (warm-index-queue v1). It
// owns its signal context — the ingest ctx's OFAC timeout must never
// bound a long-lived poll loop — and skips every dependency the lane
// doesn't need (Redis, downloaders, sanctions/labels services). A
// disabled flag logs and exits 0 so the mode is safe to run always-on.
func runWarmDrain() error {
	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logger.New(cfg.LogLevel)
	if !cfg.WarmIndexEnabled {
		log.Info("warm index disabled (WARM_INDEX_ENABLED); exiting")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := database.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}
	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	queries := db.New(pool)
	resolver, err := loadResolver(ctx, queries)
	if err != nil {
		return err
	}
	pcKeys, err := providerconfig.NewKeystore(cfg.ProviderConfigKEK)
	if err != nil {
		return err
	}
	pcRes, err := providerconfig.ResolveAll(ctx, cfg, queries, pcKeys,
		providerconfig.WithLogger(log))
	if err != nil {
		return err
	}
	// Killswitch parity with the API path: provider_configs.status AND'd
	// with data_sources.is_active, TTL-cached (~30s).
	checker := providerconfig.NewStatusCache(queries, 30*time.Second,
		providerconfig.WithLogger(log))
	lanes, err := buildWarmLanes(ctx, cfg, pool, queries, resolver,
		wallet.NewService(queries), pcRes, checker,
		cfg.WarmIndexProviders, log)
	if err != nil {
		return err
	}
	drainer := warmindex.NewDrainer(queries, resolver, lanes, checker,
		warmindex.Config{
			Enabled:      cfg.WarmIndexEnabled,
			Providers:    cfg.WarmIndexProviders,
			DailyCap:     cfg.WarmIndexDailyCap,
			PollInterval: cfg.WarmIndexPollInterval,
			ItemBudget:   cfg.WarmIndexItemBudget,
			MaxPages:     cfg.WarmIndexMaxPages,
		}, log)
	drainer.Run(ctx)
	return nil
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
