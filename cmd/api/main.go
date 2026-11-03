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

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/httpserver"
	"checkwallet/internal/ingestion"
	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/platform/cache"
	"checkwallet/internal/platform/database"
	"checkwallet/internal/platform/logger"
	"checkwallet/internal/platform/metrics"
	"checkwallet/internal/provider/ofac"
	"checkwallet/internal/sanctions"
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

	dl, err := ofac.NewDownloader(cfg.OFACSnapshotURL,
		ofac.WithMaxBytes(cfg.OFACDownloadMaxBytes))
	if err != nil {
		return err
	}
	ingestSvc := ingestion.NewService(pool, rdb.R(), resolver, dl, log)
	ingestSvc.SweepStaleRuns(ctx) // crashed workers leave no zombie RUNNING rows
	sanctionsSvc := sanctions.NewService(queries, rdb.R(), resolver, cfg.SanctionsStaleAfter)

	m := metrics.New(metrics.LatestSnapshotQuery(queries))
	ingestSvc.SetMetrics(m)
	sanctionsSvc.SetMetrics(m)

	engine := httpserver.New(httpserver.Deps{
		Pool: pool, RDB: rdb.R(), Cfg: cfg, Logger: log,
		Resolver: resolver, Queries: queries,
		Admin:     ingestion.NewAdminHandler(ingestSvc, queries, log),
		Sanctions: sanctionsSvc,
		Metrics:   m,
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
