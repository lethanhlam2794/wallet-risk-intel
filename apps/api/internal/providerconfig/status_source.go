package providerconfig

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"checkwallet/internal/db"
)

// StatusStore is the effective-status read the cache needs — satisfied by
// *db.Queries.
type StatusStore interface {
	GetProviderEffectiveStatus(ctx context.Context, code string) (db.GetProviderEffectiveStatusRow, error)
}

// StatusCache is the runtime kill-switch read: provider_configs.status
// AND'd with data_sources.is_active, cached per code for the TTL. A
// missing data_sources/provider_configs row reports active (env parity —
// absent overrides never change behaviour). A read error fails closed to
// the last-known value; a cold-start error reports active (env parity)
// and logs. Goroutine-safe: refreshes serialize under the write lock, so
// concurrent ensures never stampede the DB.
type StatusCache struct {
	load func(ctx context.Context, code string) (bool, error)
	ttl  time.Duration
	log  *slog.Logger
	now  func() time.Time

	mu      sync.Mutex
	entries map[string]statusEntry
}

type statusEntry struct {
	active    bool
	fetchedAt time.Time
}

// WithStatusLoader overrides the status read (tests).
func WithStatusLoader(f func(ctx context.Context, code string) (bool, error)) Option {
	return func(d *resolverDeps) { d.statusLoader = f }
}

// NewStatusCache builds the cache over the effective-status query.
// ttl <= 0 means always re-read (tests); the chain walk wires ~30s.
func NewStatusCache(store StatusStore, ttl time.Duration, opts ...Option) *StatusCache {
	deps := &resolverDeps{
		log:    slog.New(slog.DiscardHandler),
		getenv: func(string) string { return "" },
	}
	for _, o := range opts {
		o(deps)
	}
	load := deps.statusLoader
	if load == nil {
		load = func(ctx context.Context, code string) (bool, error) {
			return loadStatus(ctx, store, code)
		}
	}
	return &StatusCache{
		load: load, ttl: ttl, log: deps.log,
		now: time.Now, entries: map[string]statusEntry{},
	}
}

// Active reports the effective-active verdict for a source code.
func (c *StatusCache) Active(ctx context.Context, code string) bool {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[code]; ok && now.Sub(e.fetchedAt) < c.ttl {
		return e.active
	}
	active, err := c.load(ctx, code)
	if err != nil {
		if e, ok := c.entries[code]; ok {
			c.log.Warn("providerconfig: status read failed; serving last-known",
				"source_code", code, "error", err)
			return e.active
		}
		c.log.Error("providerconfig: status read failed on cold cache; defaulting active",
			"source_code", code, "error", err)
		return true
	}
	c.entries[code] = statusEntry{active: active, fetchedAt: now}
	return active
}

// loadStatus implements the effective-active invariant (critic R1):
// data_sources.is_active AND (status='active' OR row absent). A missing
// row is active — env parity for providers with no overrides.
func loadStatus(ctx context.Context, store StatusStore, code string) (bool, error) {
	row, err := store.GetProviderEffectiveStatus(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return row.IsActive && row.ConfigStatus == "active", nil
}
