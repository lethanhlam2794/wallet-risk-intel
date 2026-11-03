// Command worker runs a single snapshot ingestion synchronously:
//
//	go run ./cmd/worker -source ofac_sdn              # download + ingest
//	go run ./cmd/worker -source ofac_sdn -file f.xml  # ingest a local file
//
// Exit code is non-zero unless the run reaches COMPLETED.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

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
)

func main() {
	source := flag.String("source", "ofac_sdn", "data source code to ingest")
	file := flag.String("file", "", "local snapshot file (skips download)")
	flag.Parse()
	if err := run(*source, *file); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run(source, file string) error {
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
