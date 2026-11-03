package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/platform/database"
	"checkwallet/internal/testutil"
)

// testWalletAndSource resolves the seeded ethereum network + a provider
// data source and creates the wallet FK targets.
func testWalletAndSource(t *testing.T, q *db.Queries, sourceCode, addr string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	src, err := q.GetSourceByCode(ctx, sourceCode)
	require.NoError(t, err, "data_sources seed %q must exist", sourceCode)
	nets, err := q.ListActiveNetworks(ctx)
	require.NoError(t, err)
	var netID int64
	for _, n := range nets {
		if n.Code == "ethereum" {
			netID = n.ID
		}
	}
	require.NotZero(t, netID, "ethereum network seed must exist")
	w, err := q.GetOrCreateWallet(ctx, db.GetOrCreateWalletParams{
		NetworkID: netID, Address: addr, NormalizedAddress: addr, AddressType: "evm",
	})
	require.NoError(t, err)
	return src.ID, w.ID
}

func insertObservation(t *testing.T, q *db.Queries, pool txBeginner, srcID, walletID int64,
	status string, score *float64, fetchedAt time.Time) db.ProviderObservation {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	qtx := q.WithTx(tx)
	params := db.InsertProviderObservationParams{
		SourceID:       srcID,
		WalletID:       walletID,
		RequestedChain: "ETH",
		Status:         status,
		HttpStatus:     pgtype.Int4{Int32: 200, Valid: true},
		LatencyMs:      pgtype.Int4{Int32: 42, Valid: true},
		Payload:        []byte(`{"echo":true}`),
		Error:          nil,
		FetchedAt:      pgtype.Timestamptz{Time: fetchedAt, Valid: true},
	}
	if score != nil {
		params.ProviderScore = pgtype.Float8{Float64: *score, Valid: true}
	}
	row, err := qtx.InsertProviderObservation(ctx, params)
	require.NoError(t, err)
	// Retention prune rides inside the insert transaction (F5).
	require.NoError(t, qtx.PruneProviderObservations(ctx, db.PruneProviderObservationsParams{
		SourceID: srcID, WalletID: walletID, Limit: 25,
	}))
	require.NoError(t, tx.Commit(ctx))
	return row
}

type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

func TestProviderObservations_InsertAndSelectLatest(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	srcID, walletID := testWalletAndSource(t, q, "goplus",
		"0x0000000000000000000000000000000000000b01")

	score := 87.5
	older := insertObservation(t, q, pool, srcID, walletID, "OK", &score,
		time.Now().Add(-time.Hour))
	newer := insertObservation(t, q, pool, srcID, walletID, "RATE_LIMITED", nil,
		time.Now())

	rows, err := q.ListLatestProviderObservations(context.Background(),
		db.ListLatestProviderObservationsParams{
			SourceID: srcID, WalletID: walletID, Limit: 10,
		})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	// Newest first.
	assert.Equal(t, newer.ID, rows[0].ID)
	assert.Equal(t, older.ID, rows[1].ID)
	assert.Equal(t, "RATE_LIMITED", rows[0].Status)
	assert.Equal(t, "ETH", rows[0].RequestedChain)
	assert.Equal(t, int32(200), rows[0].HttpStatus.Int32)
	assert.Equal(t, int32(42), rows[0].LatencyMs.Int32)
	assert.True(t, rows[0].FetchedAt.Valid)

	// provider_score round-trips and is NULL when absent — a missing score
	// can never be read as a present zero.
	assert.True(t, rows[1].ProviderScore.Valid)
	assert.InDelta(t, 87.5, rows[1].ProviderScore.Float64, 0.001)
	assert.False(t, rows[0].ProviderScore.Valid)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rows[1].Payload, &payload))
	assert.Equal(t, true, payload["echo"])
}

func TestProviderObservations_RetentionPrunesToNewest25(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	srcID, walletID := testWalletAndSource(t, q, "publicaml",
		"0x0000000000000000000000000000000000000b02")

	base := time.Now().Add(-2 * time.Hour)
	var oldestKept time.Time
	for i := 0; i < 26; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		if i == 1 { // 26 inserts → oldest dropped, ids 2..26 kept
			oldestKept = ts
		}
		insertObservation(t, q, pool, srcID, walletID, "OK", nil, ts)
	}

	rows, err := q.ListLatestProviderObservations(context.Background(),
		db.ListLatestProviderObservationsParams{
			SourceID: srcID, WalletID: walletID, Limit: 50,
		})
	require.NoError(t, err)
	require.Len(t, rows, 25, "retention keeps newest 25 per (source, wallet)")
	assert.Equal(t, oldestKept.UTC().Truncate(time.Microsecond),
		rows[len(rows)-1].FetchedAt.Time.UTC().Truncate(time.Microsecond),
		"newest win: the oldest row was pruned")
}

func TestProviderObservations_StatusCheckRejectsUnknown(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	srcID, walletID := testWalletAndSource(t, q, "goplus",
		"0x0000000000000000000000000000000000000b03")

	_, err := pool.Exec(context.Background(),
		`INSERT INTO provider_observations
		 (source_id, wallet_id, requested_chain, status, payload)
		 VALUES ($1,$2,'ETH','BOGUS','{}'::jsonb)`, srcID, walletID)
	require.Error(t, err, "CHECK constraint must reject unknown status")
	assert.Contains(t, err.Error(), "provider_observations_status_check")
}

func TestProviderObservations_SourceSeeds(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	for _, code := range []string{"goplus", "publicaml", "chainabuse"} {
		src, err := q.GetSourceByCode(context.Background(), code)
		require.NoError(t, err, "seed %s", code)
		assert.Equal(t, "api", src.SourceType,
			fmt.Sprintf("%s registered as api source", code))
		assert.True(t, src.IsActive)
	}
}

// TestProviderObservations_MigrationRollback proves the down migration
// removes the table + seeds and re-up restores them (P-007: file + runner).
// It runs on a DEDICATED scratch _test database — a schema-mutating test
// on shared check_wallet_test races with every parallel package's
// RequireTestPool re-migration (down→v5 mid-flight would flake siblings
// and itself).
func TestProviderObservations_MigrationRollback(t *testing.T) {
	dsn := strings.Replace(testutil.TestDSN(), "check_wallet_test",
		"check_wallet_rollback_test", 1)
	// If TEST_DATABASE_URL names a different database the replace is a
	// no-op and the DSN stays pointed at a shared DB — refuse to run a
	// schema-mutating test there.
	require.NotEqual(t, testutil.TestDSN(), dsn,
		"scratch DSN derivation failed — would mutate the shared test DB")
	pool := testutil.RequireTestPoolDSN(t, dsn)
	ctx := context.Background()

	require.NoError(t, database.MigrateTo(dsn, 5))
	var exists bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT FROM pg_tables WHERE schemaname='public' AND tablename='provider_observations')`).Scan(&exists))
	assert.False(t, exists, "down migration drops provider_observations")
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT FROM data_sources WHERE code='goplus')`).Scan(&exists))
	assert.False(t, exists, "down migration removes provider seeds")

	require.NoError(t, database.Migrate(dsn))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT FROM pg_tables WHERE schemaname='public' AND tablename='provider_observations')`).Scan(&exists))
	assert.True(t, exists, "re-up restores provider_observations")
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT FROM data_sources WHERE code='chainabuse')`).Scan(&exists))
	assert.True(t, exists, "re-up restores provider seeds")
}
