package tests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/testutil"
)

// Phase-01 gate: migration landed tables, the source row, and the two
// seeded ethereum contract cursors (verified deploy blocks).
func TestFreeze_SchemaAndSeeds(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	ctx := context.Background()

	// Source row registered as a chain_scan source.
	src, err := q.GetSourceByCode(ctx, "stablecoin_freeze")
	require.NoError(t, err)
	assert.Equal(t, "chain_scan", src.SourceType)

	// Exactly two seeded cursors on ethereum, at verified deploy blocks.
	states, err := q.GetFreezeScanStates(ctx)
	require.NoError(t, err)
	require.Len(t, states, 2)
	byContract := map[string]db.FreezeScanState{}
	for _, s := range states {
		byContract[s.ContractAddress] = s
	}
	usdt, ok := byContract["0xdac17f958d2ee523a2206206994597c13d831ec7"]
	require.True(t, ok, "usdt cursor seeded")
	usdc, ok := byContract["0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"]
	require.True(t, ok, "usdc cursor seeded")
	assert.Equal(t, int64(4634748), usdt.GenesisBlock)
	assert.Equal(t, int64(6082465), usdc.GenesisBlock)
	// last_scanned starts at genesis-1 so the first window covers the
	// deploy block itself.
	assert.Equal(t, usdt.GenesisBlock-1, usdt.LastScannedBlock)
	assert.Equal(t, usdc.GenesisBlock-1, usdc.LastScannedBlock)
	// Both rows are pinned to the ethereum network id.
	var ethID int64
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id FROM blockchain_networks WHERE code='ethereum'").Scan(&ethID))
	assert.Equal(t, ethID, usdt.NetworkID)
	assert.Equal(t, ethID, usdc.NetworkID)
}

// Cursor advance is GREATEST-bound: it moves forward, updates the head
// marker, and never regresses on a degenerate (empty/rewound) window.
func TestFreeze_CursorAdvanceNeverRegresses(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	ctx := context.Background()

	states, err := q.GetFreezeScanStates(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, states)
	st := states[0]
	orig := st.LastScannedBlock
	// freeze_scan_state is a static (non-truncated) seed table — restore
	// the cursor so later tests see pristine seeds.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"UPDATE freeze_scan_state SET last_scanned_block=$1, last_head_block=0 WHERE contract_address=$2",
			orig, st.ContractAddress)
	})

	// Forward advance lands.
	require.NoError(t, q.AdvanceFreezeScanState(ctx, db.AdvanceFreezeScanStateParams{
		NetworkID:        st.NetworkID,
		ContractAddress:  st.ContractAddress,
		LastScannedBlock: 8000000,
		LastHeadBlock:    8000100,
	}))
	after, err := q.GetFreezeScanStates(ctx)
	require.NoError(t, err)
	var cur db.FreezeScanState
	for _, s := range after {
		if s.ContractAddress == st.ContractAddress {
			cur = s
		}
	}
	assert.Equal(t, int64(8000000), cur.LastScannedBlock)
	assert.Equal(t, int64(8000100), cur.LastHeadBlock)

	// Degenerate window (toBlock < cursor) must not regress the cursor;
	// the head marker still refreshes.
	require.NoError(t, q.AdvanceFreezeScanState(ctx, db.AdvanceFreezeScanStateParams{
		NetworkID:        st.NetworkID,
		ContractAddress:  st.ContractAddress,
		LastScannedBlock: 7999999,
		LastHeadBlock:    8000200,
	}))
	after2, err := q.GetFreezeScanStates(ctx)
	require.NoError(t, err)
	for _, s := range after2 {
		if s.ContractAddress == st.ContractAddress {
			cur = s
		}
	}
	assert.Equal(t, int64(8000000), cur.LastScannedBlock)
	assert.Equal(t, int64(8000200), cur.LastHeadBlock)
}

// Staging inserts are idempotent on the (run, contract, tx, log_index) PK
// and the CHECK constraint rejects unknown event types.
func TestFreeze_StagingInsertIdempotent(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	ctx := context.Background()
	runID := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	row := db.StgInsertFreezeEventParams{
		RunID:           runID,
		ContractAddress: "0xdac17f958d2ee523a2206206994597c13d831ec7",
		EventType:       "freeze",
		WalletAddress:   "0x00000000000000000000000000000000000000aa",
		TxHash:          "0xab00000000000000000000000000000000000000000000000000000000000000",
		LogIndex:        3,
		BlockNumber:     19000000,
		Fingerprint:     "fp1",
		Label:           "usdt_blacklist",
		Evidence:        []byte(`{"event":"AddedBlackList"}`),
	}
	n, err := q.StgInsertFreezeEvent(ctx, row)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	// Replay: same PK → 0 rows, no error.
	n, err = q.StgInsertFreezeEvent(ctx, row)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	total, err := q.StgFreezeEventTotal(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)

	counts, err := q.StgFreezeEventCounts(ctx, runID)
	require.NoError(t, err)
	require.Len(t, counts, 1)
	assert.Equal(t, "freeze", counts[0].EventType)
	assert.Equal(t, int64(1), counts[0].N)

	maxes, err := q.StgFreezeEventMaxBlocks(ctx, runID)
	require.NoError(t, err)
	require.Len(t, maxes, 1)
	assert.Equal(t, int64(19000000), maxes[0].Mx)

	// CHECK rejects an unknown event type.
	bad := row
	bad.EventType = "explode"
	bad.LogIndex = 4
	_, err = q.StgInsertFreezeEvent(ctx, bad)
	require.Error(t, err)

	// Purge keeps only the listed run ids.
	require.NoError(t, q.StgPurgeFreezeEventsExceptRuns(ctx,
		[]pgtype.UUID{{Bytes: uuid.New(), Valid: true}}))
	total, err = q.StgFreezeEventTotal(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
}
