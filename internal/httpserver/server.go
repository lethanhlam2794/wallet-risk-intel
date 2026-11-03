// Package httpserver wires the Gin engine: middleware chain, route groups,
// and handlers for the phase 1-2 surface.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/ingestion"
	"checkwallet/internal/label"
	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/platform/metrics"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/wallet"
)

const maxRequestBodyBytes = 1 << 20 // 1MB (spec §16)

// FreshnessProvider reports sanctions-data freshness for API responses.
// Phase 5 provides the snapshot-backed implementation.
type FreshnessProvider interface {
	DataStatus(ctx context.Context) (status string, lastSyncedAt *time.Time)
}

// Deps carries every collaborator the router needs.
type Deps struct {
	Pool      *pgxpool.Pool
	RDB       *redis.Client
	Cfg       *config.Config
	Logger    *slog.Logger
	Resolver  *network.Resolver
	Queries   *db.Queries
	Freshness FreshnessProvider
	// Admin wires /api/v1/admin/* when set; nil leaves admin routes off.
	Admin *ingestion.AdminHandler
	// Sanctions wires /api/v1/sanctions/* when set; it also serves as the
	// FreshnessProvider for wallet responses when Freshness is nil.
	Sanctions *sanctions.Service
	// Labels wires /api/v1/wallets/:network/:address/labels and the wallet
	// endpoint's label summary when set.
	Labels *label.Service
	// Metrics wires request instrumentation and /metrics when set.
	Metrics *metrics.Metrics
}

// New builds the Gin engine with the full middleware chain and routes.
func New(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.RequestID(), middleware.Recovery(d.Logger), middleware.MaxBodyBytes(maxRequestBodyBytes))
	r.Use(middleware.AccessLog(d.Logger, d.Metrics))

	limiter := middleware.NewRateLimiter(d.RDB,
		int64(d.Cfg.RateLimitRequests), int64(d.Cfg.RateLimitAnon), d.Cfg.RateLimitWindow,
		func() {
			d.Logger.Warn("rate limiter store error; failing open",
				"component", "rate_limiter")
			if d.Metrics != nil {
				d.Metrics.IncRateLimitStoreError()
			}
		})

	// Public probes (spec: healthz/readyz public; degraded ≠ dead).
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/readyz", readyzHandler(d.Pool, d.RDB))

	// Authenticated API surface: identify → rate-limit → enforce role.
	api := r.Group("/api/v1",
		middleware.IdentifyClient(d.Queries),
		limiter.Middleware(),
		middleware.RequireRole("reader", "admin"),
	)

	fresh := d.Freshness
	if fresh == nil && d.Sanctions != nil {
		fresh = d.Sanctions // sanctions.Service implements FreshnessProvider
	}
	walletSvc := wallet.NewService(d.Queries)
	api.GET("/networks", networksHandler(d.Resolver))
	api.GET("/wallets/:network/:address", walletHandler(d.Resolver, walletSvc, fresh, d.Labels))

	if d.Labels != nil {
		lh := label.NewHandler(d.Labels, d.Resolver)
		api.GET("/wallets/:network/:address/labels", lh.WalletLabels)
	}

	if d.Sanctions != nil {
		sc := sanctions.NewHandler(d.Sanctions, d.Resolver)
		api.GET("/sanctions/wallets/:network/:address", sc.WalletLookup)
		api.GET("/sanctions/entities/:id", sc.Entity)
		api.GET("/sanctions/programs/:code", sc.Program)
	}

	// Metrics scrape endpoint: reader-authenticated (phase 02 posture).
	if d.Metrics != nil {
		r.GET("/metrics",
			middleware.IdentifyClient(d.Queries),
			middleware.RequireRole("reader", "admin"),
			gin.WrapH(d.Metrics.Handler()))
	}

	// Admin surface: same auth chain, admin role only.
	if d.Admin != nil {
		adm := r.Group("/api/v1/admin",
			middleware.IdentifyClient(d.Queries),
			limiter.Middleware(),
			middleware.RequireRole("admin"),
		)
		adm.POST("/ingestion-runs", d.Admin.TriggerRun)
		adm.GET("/ingestion-runs/:id", d.Admin.GetRun)
	}

	// Unmatched routes/methods still answer the stable envelope.
	r.NoRoute(func(c *gin.Context) {
		middleware.RespondError(c, http.StatusNotFound, middleware.CodeNotFound, "not found")
	})
	r.NoMethod(func(c *gin.Context) {
		middleware.RespondError(c, http.StatusMethodNotAllowed, middleware.CodeNotFound, "method not allowed")
	})

	return r
}

func readyzHandler(pool *pgxpool.Pool, rdb *redis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		pgErr := pool.Ping(ctx)
		rdErr := rdb.Ping(ctx).Err()
		status := http.StatusOK
		if pgErr != nil || rdErr != nil {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{
			"postgres": okOrDegraded(pgErr == nil),
			"redis":    okOrDegraded(rdErr == nil),
		})
	}
}

func okOrDegraded(ok bool) string {
	if ok {
		return "ok"
	}
	return "down"
}

func networksHandler(resolver *network.Resolver) gin.HandlerFunc {
	type netDTO struct {
		Code      string `json:"code"`
		Name      string `json:"name"`
		ChainID   string `json:"chain_id"`
		Namespace string `json:"namespace"`
	}
	return func(c *gin.Context) {
		out := make([]netDTO, 0)
		for _, n := range resolver.List() {
			out = append(out, netDTO{Code: n.Code, Name: n.Name, ChainID: n.CAIP2(), Namespace: n.Namespace})
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}

func walletHandler(resolver *network.Resolver, svc *wallet.Service,
	fresh FreshnessProvider, labels *label.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		net, normalized, err := wallet.ResolveAndNormalize(resolver, c.Param("network"), c.Param("address"))
		if err != nil {
			switch {
			case errors.Is(err, wallet.ErrInvalidNetwork):
				middleware.RespondError(c, http.StatusBadRequest, middleware.CodeInvalidNetwork, "unsupported network")
			default:
				middleware.RespondError(c, http.StatusBadRequest, middleware.CodeInvalidAddress, "invalid address")
			}
			return
		}
		w, err := svc.GetOrCreate(c.Request.Context(), net.ID, c.Param("address"), normalized)
		if err != nil {
			middleware.InternalError(c)
			return
		}
		// data_status here stays SANCTIONS-scoped (F10) — label freshness
		// lives on the /labels endpoint response only.
		dataStatus := "UNAVAILABLE"
		var lastSynced *time.Time
		if fresh != nil {
			dataStatus, lastSynced = fresh.DataStatus(c.Request.Context())
		}
		labelsOut := []any{}
		var labelsCount int64
		if labels != nil {
			if items, count, err := labels.Summary(c.Request.Context(), w.ID); err == nil {
				labelsCount = count
				for _, it := range items {
					labelsOut = append(labelsOut, it)
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"network":            net.Code,
			"address":            w.Address,
			"normalized_address": w.NormalizedAddress,
			"address_type":       w.AddressType,
			"labels":             labelsOut,
			"labels_count":       labelsCount,
			"data_status":        dataStatus,
			"last_synced_at":     lastSynced,
			"first_seen_at":      w.CreatedAt.Time.UTC(),
		}})
	}
}
