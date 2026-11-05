package providerconfig

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
)

type fakeStatusStore struct {
	rows map[string]db.GetProviderEffectiveStatusRow
	err  error
}

func (f *fakeStatusStore) GetProviderEffectiveStatus(_ context.Context, code string) (db.GetProviderEffectiveStatusRow, error) {
	if f.err != nil {
		return db.GetProviderEffectiveStatusRow{}, f.err
	}
	r, ok := f.rows[code]
	if !ok {
		return db.GetProviderEffectiveStatusRow{}, pgx.ErrNoRows
	}
	return r, nil
}

// Effective-active = data_sources.is_active AND
// (provider_configs.status='active' OR row absent).
func TestLoadStatus_EffectiveActiveFormula(t *testing.T) {
	for _, tc := range []struct {
		name     string
		row      *db.GetProviderEffectiveStatusRow
		err      error
		expected bool
	}{
		{"active + catalog on", &db.GetProviderEffectiveStatusRow{IsActive: true, ConfigStatus: "active"}, nil, true},
		{"inactive status", &db.GetProviderEffectiveStatusRow{IsActive: true, ConfigStatus: "inactive"}, nil, false},
		{"catalog off wins over active", &db.GetProviderEffectiveStatusRow{IsActive: false, ConfigStatus: "active"}, nil, false},
		{"catalog off + inactive", &db.GetProviderEffectiveStatusRow{IsActive: false, ConfigStatus: "inactive"}, nil, false},
		{"missing row is active (env parity)", nil, pgx.ErrNoRows, true},
		{"db error surfaces", nil, errors.New("conn reset"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStatusStore{err: tc.err}
			if tc.row != nil {
				store.rows = map[string]db.GetProviderEffectiveStatusRow{"x": *tc.row}
			}
			got, err := loadStatus(context.Background(), store, "x")
			if tc.err != nil && !errors.Is(tc.err, pgx.ErrNoRows) {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, got)
		})
	}
}

// TTL: one load per window; a flip mid-window is invisible until expiry.
func TestStatusCache_TTLWindow(t *testing.T) {
	var calls atomic.Int64
	active := true
	cache := NewStatusCache(nil, 30*time.Second,
		WithStatusLoader(func(context.Context, string) (bool, error) {
			calls.Add(1)
			return active, nil
		}))
	now := time.Now()
	cache.now = func() time.Time { return now }

	assert.True(t, cache.Active(context.Background(), "covalent"))
	assert.True(t, cache.Active(context.Background(), "covalent"))
	assert.Equal(t, int64(1), calls.Load(), "second call inside TTL is cached")

	active = false
	assert.True(t, cache.Active(context.Background(), "covalent"),
		"flip inside TTL still serves the cached value")

	cache.now = func() time.Time { return now.Add(31 * time.Second) }
	assert.False(t, cache.Active(context.Background(), "covalent"),
		"after TTL the flip is seen")
	assert.Equal(t, int64(2), calls.Load())
}

// DB read error ⇒ last-known value (never silently re-enable); cold-start
// error ⇒ active + log (env parity).
func TestStatusCache_DBErrorLastKnown(t *testing.T) {
	var fail atomic.Bool
	var active atomic.Bool
	active.Store(true)
	cache := NewStatusCache(nil, 30*time.Second,
		WithStatusLoader(func(context.Context, string) (bool, error) {
			if fail.Load() {
				return false, errors.New("db down")
			}
			return active.Load(), nil
		}),
		WithLogger(slog.New(slog.DiscardHandler)))
	now := time.Now()
	cache.now = func() time.Time { return now }

	assert.True(t, cache.Active(context.Background(), "covalent"))
	fail.Store(true)
	cache.now = func() time.Time { return now.Add(31 * time.Second) }
	assert.True(t, cache.Active(context.Background(), "covalent"),
		"error after a known value keeps last-known")

	// Cold-start error on another code ⇒ active (env parity), not panic.
	assert.True(t, cache.Active(context.Background(), "chainscan"))
}

// Goroutine-safety: concurrent ensures for different wallets share the
// cache (singleflight is per-wallet only) — race detector covers this.
func TestStatusCache_Concurrent(t *testing.T) {
	var calls atomic.Int64
	cache := NewStatusCache(nil, 30*time.Second,
		WithStatusLoader(func(context.Context, string) (bool, error) {
			calls.Add(1)
			return true, nil
		}))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.True(t, cache.Active(context.Background(), "covalent"))
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, calls.Load(), int64(2),
		"refresh is serialized — no stampede")
}
