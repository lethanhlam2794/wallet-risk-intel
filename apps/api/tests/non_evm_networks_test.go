package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/platform/database"
	"checkwallet/internal/testutil"
	"checkwallet/internal/wallet"
)

// Migration 000026 registers solana/tron as INACTIVE rows. These tests pin
// the groundwork contract: rows exist, the resolver still rejects both
// codes, staged labels for inactive codes stay orphan-rejected (D4b), and
// the down migration removes exactly the two guarded rows.

type networkSeed struct {
	code, namespace, chainRef, name string
	active                          bool
}

func networkSeeds(t *testing.T, pool *pgxpool.Pool) map[string]networkSeed {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT code, namespace, chain_reference, name, is_active FROM blockchain_networks")
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]networkSeed{}
	for rows.Next() {
		var s networkSeed
		require.NoError(t, rows.Scan(&s.code, &s.namespace, &s.chainRef, &s.name, &s.active))
		out[s.code] = s
	}
	require.NoError(t, rows.Err())
	return out
}

func TestNonEVM_NetworksRegisteredInactive(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	seeds := networkSeeds(t, pool)

	sol, ok := seeds["solana"]
	require.True(t, ok, "solana row must exist")
	assert.False(t, sol.active)
	assert.Equal(t, "solana", sol.namespace)
	assert.Equal(t, "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp", sol.chainRef)
	assert.Equal(t, "Solana Mainnet", sol.name)

	trx, ok := seeds["tron"]
	require.True(t, ok, "tron row must exist")
	assert.False(t, trx.active)
	assert.Equal(t, "tron", trx.namespace)
	assert.Equal(t, "0x2b6653dc", trx.chainRef)
	assert.Equal(t, "TRON Mainnet", trx.name)

	// Existing rows untouched.
	assert.True(t, seeds["ethereum"].active)
	assert.True(t, seeds["bsc"].active)
}

func TestNonEVM_ResolverStillRejects(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	resolver := testResolver(t, q)

	// Active listing is still exactly ethereum + bsc.
	var codes []string
	for _, n := range resolver.List() {
		codes = append(codes, n.Code)
	}
	assert.ElementsMatch(t, []string{"ethereum", "bsc"}, codes)

	_, ok := resolver.Resolve("solana")
	assert.False(t, ok)
	_, ok = resolver.Resolve("tron")
	assert.False(t, ok)

	// A perfectly valid solana address cannot reach validation — the
	// network code fails first with the unchanged error contract.
	_, _, err := wallet.ResolveAndNormalize(resolver, "solana",
		"So11111111111111111111111111111111111111112")
	assert.ErrorIs(t, err, wallet.ErrInvalidNetwork)
	_, _, err = wallet.ResolveAndNormalize(resolver, "tron",
		"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t")
	assert.ErrorIs(t, err, wallet.ErrInvalidNetwork)
}

// D4b regression pin: a staged label row whose network_code resolves only
// to an INACTIVE blockchain_networks row must stay an orphan — it must not
// join, publish, or mint a '0x'+lower(base58) garbage wallet (which would
// also FK-block the down migration).
func TestNonEVM_StagedInactiveLabelStaysOrphan(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	ctx := context.Background()
	runID := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	n, err := q.StgInsertWalletLabel(ctx, db.StgInsertWalletLabelParams{
		RunID:            runID,
		NetworkCode:      "solana",
		Address:          "So11111111111111111111111111111111111111112",
		Label:            "Injected Solana Label",
		UpstreamCategory: "scam",
		Confidence:       "high",
		Fingerprint:      "testfp-solana-orphan",
		Evidence:         []byte(`{"source":"d4b-test"}`),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	orphans, err := q.StgLabelOrphanNetworks(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), orphans,
		"staged row on an inactive network must count as orphan")

	made, err := q.PublishEnsureLabelWallets(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), made,
		"inactive-network staged rows must not create wallets")

	var solID int64
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id FROM blockchain_networks WHERE code='solana'").Scan(&solID))
	assert.Zero(t, count(t, pool,
		"SELECT count(*) FROM wallets WHERE network_id=$1", solID))
}

// Migration 000026 down/up on a private scratch DB: down removes exactly
// the two inactive rows; re-up restores them still inactive.
func TestNonEVM_MigrationRollback(t *testing.T) {
	dsn := strings.Replace(testutil.TestDSN(), "check_wallet_test",
		"check_wallet_nonevm_test", 1)
	require.NotEqual(t, testutil.TestDSN(), dsn,
		"scratch DSN derivation failed — would mutate the shared test DB")
	pool := testutil.RequireTestPoolDSN(t, dsn)
	ctx := context.Background()

	var solActive bool
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT is_active FROM blockchain_networks WHERE code='solana'").Scan(&solActive))
	require.False(t, solActive)

	// Down one: only the two rows go away; ethereum/bsc stay.
	require.NoError(t, database.MigrateTo(dsn, 25))
	seeds := networkSeeds(t, pool)
	assert.Len(t, seeds, 2)
	_, ok := seeds["solana"]
	assert.False(t, ok)
	_, ok = seeds["tron"]
	assert.False(t, ok)

	// Re-up restores both rows inactive.
	require.NoError(t, database.Migrate(dsn))
	seeds = networkSeeds(t, pool)
	require.Len(t, seeds, 4)
	assert.False(t, seeds["solana"].active)
	assert.False(t, seeds["tron"].active)
}

// The down migration is guarded on is_active=FALSE — it must never revert
// a future enablement. An activated solana row survives MigrateTo(25).
func TestNonEVM_DownGuardNeverDeletesActive(t *testing.T) {
	dsn := strings.Replace(testutil.TestDSN(), "check_wallet_test",
		"check_wallet_nonevm_test", 1)
	require.NotEqual(t, testutil.TestDSN(), dsn)
	pool := testutil.RequireTestPoolDSN(t, dsn)
	ctx := context.Background()

	// Simulate a (hypothetical, out-of-scope) enablement.
	_, err := pool.Exec(ctx,
		"UPDATE blockchain_networks SET is_active=TRUE WHERE code='solana'")
	require.NoError(t, err)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(c,
			"UPDATE blockchain_networks SET is_active=FALSE WHERE code IN ('solana','tron')")
		_ = database.Migrate(dsn)
	})

	require.NoError(t, database.MigrateTo(dsn, 25))
	var exists, active bool
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT TRUE, is_active FROM blockchain_networks WHERE code='solana'").Scan(&exists, &active))
	assert.True(t, exists, "activated row must survive the guarded down")
	assert.True(t, active)
	assert.Zero(t, count(t, pool,
		"SELECT count(*) FROM blockchain_networks WHERE code='tron'"),
		"inactive tron row is still removed by the same down")
}
