// Command worker runs a single snapshot ingestion synchronously:
//
//	go run ./cmd/worker -source ofac_sdn              # download + ingest
//	go run ./cmd/worker -source ofac_sdn -file f.xml  # ingest a local file
//
//	or, in warmdrain mode, polls the warm index queue; in warmseed mode,
//	runs a single proactive seed scan and exits:
//
//	go run ./cmd/worker -mode warmdrain               # WARM_INDEX_ENABLED=1
//	go run ./cmd/worker -mode warmseed                # WARM_INDEX_ENABLED=1
//
// Ingest exits non-zero unless the run reaches COMPLETED; warmdrain runs
// until SIGINT/SIGTERM and always exits 0 on clean shutdown; warmseed is
// a cron-scheduled one-shot — exit 0 on a finished scan, non-zero on a
// DB-level failure.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
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
	"checkwallet/internal/provider/stablefreeze"
	"checkwallet/internal/provider/walletattribution"
	"checkwallet/internal/providerconfig"
	"checkwallet/internal/wallet"
	"checkwallet/internal/warmindex"
)

func main() {
	mode := flag.String("mode", "ingest", "worker mode: ingest|warmdrain|warmseed")
	source := flag.String("source", "ofac_sdn", "data source code to ingest")
	file := flag.String("file", "", "local snapshot file (skips download)")
	untilCaughtUp := flag.Bool("until-caught-up", false,
		"stablecoin_freeze: repeat runs until all cursors reach head-confirmations")
	flag.Parse()

	var err error
	switch *mode {
	case "ingest":
		err = run(*source, *file, *untilCaughtUp)
	case "warmdrain":
		err = runWarmDrain()
	case "warmseed":
		err = runWarmSeed()
	default:
		err = fmt.Errorf("unknown -mode %q", *mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run(source, file string, untilCaughtUp bool) error {
	if untilCaughtUp && file != "" {
		return fmt.Errorf("-until-caught-up requires chain-scan mode (no -file); " +
			"a file-mode artifact cannot advance cursors past its own toBlock")
	}
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

		// stablecoin_freeze: resolve the RPC provider's stored/env config,
		// then build the scanner. Any gap (missing key, inactive row,
		// ethereum disabled) fails CLOSED — the fetcher is simply absent
		// so a triggered run lands FAILED ("no downloader configured")
		// rather than emitting a fabricated artifact.
		pcKeys, err := providerconfig.NewKeystore(cfg.ProviderConfigKEK)
		if err != nil {
			return err
		}
		pcRes, err := providerconfig.ResolveAll(ctx, cfg, queries, pcKeys,
			providerconfig.WithLogger(log))
		if err != nil {
			return err
		}
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
	}

	svc := ingestion.NewService(pool, resolver, fetchers, log)
	svc.SweepStaleRuns(ctx)

	var runID uuid.UUID
	for pass := 0; ; pass++ {
		runID, err = svc.RunSync(ctx, ingestion.RunRequest{
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
		if !untilCaughtUp || source != stablefreeze.SourceCode {
			return nil
		}
		caught, err := freezeCaughtUp(ctx, queries, cfg.FreezeScanConfirmations)
		if err != nil {
			return err
		}
		if caught {
			fmt.Println("all freeze cursors caught up to head-confirmations")
			return nil
		}
		log.Info("freeze cursors behind head — next pass", "pass", pass+1)
	}
}

// freezeCaughtUp reports whether every WATCHED contract cursor has
// reached its run's head-confirmations floor — the honest exit for
// -until-caught-up. Seeded rows for contracts the registry does not
// scan are ignored: their cursor can never advance, so counting them
// would loop forever.
func freezeCaughtUp(ctx context.Context, q *db.Queries, conf int64) (bool, error) {
	states, err := q.GetFreezeScanStates(ctx)
	if err != nil {
		return false, err
	}
	watched := stablefreeze.RegistryByAddress()
	seen := 0
	for _, st := range states {
		if _, ok := watched[strings.ToLower(st.ContractAddress)]; !ok {
			continue
		}
		seen++
		if st.LastScannedBlock < st.LastHeadBlock-conf {
			return false, nil
		}
	}
	return seen > 0, nil
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
			Workers:      cfg.WarmIndexWorkers,
		}, log)
	drainer.Run(ctx)
	return nil
}

// runWarmSeed is the one-shot proactive seeder (warm-seed-worker). It
// mirrors runWarmDrain's disabled-flag + signal-ctx shape but skips
// everything the seeder doesn't need — no Redis, no provider
// keystore/resolver, no lanes. The network resolver is deliberately NOT
// used: classification reads is_active/namespace straight from the JOIN
// so inactive/non-EVM rows stay visible to the counters.
func runWarmSeed() error {
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
	queue := warmindex.NewQueue(queries, cfg.WarmIndexEnabled, log)
	seeder := warmindex.NewSeeder(queries, queue, cfg.WarmSeedBatchSize, log)
	return seeder.Run(ctx)
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
