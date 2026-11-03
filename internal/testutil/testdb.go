// Package testutil provides the dedicated-test-database harness.
//
// Safety contract (P-020): integration tests MUST run on a database whose
// name ends in "_test". The guard runs before any connection is opened and
// fails the test (or the process in helpers that cannot take *testing.T)
// rather than risk mutating a dev/app database.
package testutil

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/platform/database"
)

const defaultTestDSN = "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_test?sslmode=disable"

// TestDSN returns the integration test DSN (env overridable).
func TestDSN() string {
	if v := getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return defaultTestDSN
}

// assertTestDatabase enforces the _test suffix on the database name.
func assertTestDatabase(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("testutil: parse dsn: %w", err)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" || !strings.HasSuffix(name, "_test") {
		return fmt.Errorf("testutil: refusing to use database %q: integration tests require a database name ending in _test", name)
	}
	return nil
}

// TestRedisURL returns the integration test Redis URL.
func TestRedisURL() string {
	if v := getenv("TEST_REDIS_URL"); v != "" {
		return v
	}
	return "redis://127.0.0.1:6380/9"
}

// RequireTestRedis connects to the dedicated test Redis (separate logical
// DB), flushes it, and returns a client closed on cleanup.
func RequireTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL(TestRedisURL())
	require.NoError(t, err)
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, rdb.Ping(ctx).Err())
	require.NoError(t, rdb.FlushDB(ctx).Err())
	return rdb
}

// RequireTestPool connects to the dedicated test database, creating it and
// applying the full migration manifest when needed (P-036), truncates all
// domain tables, and returns a pool that is closed on test cleanup.
func RequireTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := TestDSN()
	require.NoError(t, assertTestDatabase(dsn))
	ensureTestDatabase(t, dsn)
	require.NoError(t, database.Migrate(dsn))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	TruncateAll(t, pool)
	return pool
}

// staticTables are reference tables owned by migration seeds; truncation
// keeps them so tests always see a fully-seeded, migrated schema.
var staticTables = map[string]bool{
	"schema_migrations":   true,
	"blockchain_networks": true,
	"data_sources":        true,
	"risk_categories":     true, // migration-seeded taxonomy
}

// TruncateAll clears every mutable domain table so tests start from a clean,
// migrated schema with reference seeds intact.
func TruncateAll(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	require.NoError(t, err)
	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		if !staticTables[name] {
			tables = append(tables, name)
		}
	}
	require.NoError(t, rows.Err())
	if len(tables) == 0 {
		return
	}
	stmt := fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", strings.Join(tables, ", "))
	_, err = pool.Exec(ctx, stmt)
	require.NoError(t, err)
}

// ensureTestDatabase creates the test database via the maintenance
// "postgres" database when it does not already exist.
func ensureTestDatabase(t *testing.T, dsn string) {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	dbName := strings.TrimPrefix(u.Path, "/")

	adminURL := *u
	adminURL.Path = "/postgres"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, adminURL.String())
	if err != nil {
		// Fall back to the base app database as maintenance db.
		adminURL.Path = "/check_wallet"
		conn, err = pgx.Connect(ctx, adminURL.String())
	}
	require.NoError(t, err, "connect to maintenance database")
	defer conn.Close(ctx)

	var exists bool
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)", dbName).Scan(&exists))
	if !exists {
		_, err = conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{dbName}.Sanitize()))
		require.NoError(t, err)
	}
}
