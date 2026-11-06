package tests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/ingestion"
	"checkwallet/internal/provider/stablefreeze"
	"checkwallet/internal/testutil"
)

const (
	usdtContract = "0xdac17f958d2ee523a2206206994597c13d831ec7"
	usdcContract = "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	freezeWallet = "0x00000000000000000000000000000000000000aa"
	freezeTx     = "0x1111111111111111111111111111111111111111111111111111111111111111"
	unfreezeTx   = "0x2222222222222222222222222222222222222222222222222222222222222222"
	destroyTx    = "0x3333333333333333333333333333333333333333333333333333333333333333"
)

// freezeArtifact writes a JSONL artifact in the scanner's format: header
// line then event lines; returns the file path.
func freezeArtifact(t *testing.T, windows []map[string]any, events []map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "freeze.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	hdr, _ := json.Marshal(map[string]any{"version": 1, "contracts": windows})
	_, err = f.Write(append(hdr, '\n'))
	require.NoError(t, err)
	for _, e := range events {
		b, _ := json.Marshal(e)
		_, err = f.Write(append(b, '\n'))
		require.NoError(t, err)
	}
	return path
}

// windows builds the standard two-contract header covering the given
// event range; head applies to both.
func freezeWindows(usdtFrom, usdtTo, usdcFrom, usdcTo, head int64) []map[string]any {
	return []map[string]any{
		{"address": usdtContract, "from_block": usdtFrom, "to_block": usdtTo,
			"head": head, "empty": usdtTo < usdtFrom},
		{"address": usdcContract, "from_block": usdcFrom, "to_block": usdcTo,
			"head": head, "empty": usdcTo < usdcFrom},
	}
}

func freezeEv(contract, event, typ, wallet, tx string, logIdx, block, ts int64) map[string]any {
	return map[string]any{
		"contract": contract, "event": event, "type": typ, "wallet": wallet,
		"tx_hash": tx, "log_index": logIdx, "block_number": block, "block_ts": ts,
	}
}

func runFreeze(t *testing.T, svc *ingestion.Service, path string) uuid.UUID {
	t.Helper()
	runID, err := svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: "stablecoin_freeze", TriggeredBy: "test", FilePath: path,
	})
	require.NoError(t, err)
	return runID
}

func newFreezeService(t *testing.T) (*ingestion.Service, *db.Queries) {
	t.Helper()
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := ingestion.NewService(pool, testResolver(t, q), nil, log)
	// freeze_scan_state is a static seed table — restore cursors so
	// sibling tests see pristine values.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`UPDATE freeze_scan_state SET last_scanned_block = genesis_block - 1, last_head_block = 0`)
	})
	return svc, q
}

// labelRow reads the wallet_labels row for a fingerprint.
func labelRow(t *testing.T, q *db.Queries, fp string) db.WalletLabel {
	t.Helper()
	row, err := q.GetWalletLabelByFingerprint(context.Background(), fp)
	require.NoError(t, err)
	return row
}

// Freeze → active verified/high FROZEN label with full provenance, and
// the cursor + snapshot + run summary all reflect the window.
func TestFreeze_PublishFreezeCreatesLabel(t *testing.T) {
	svc, q := newFreezeService(t)
	pool := testutil.RequireTestPool(t)
	ctx := context.Background()

	path := freezeArtifact(t,
		freezeWindows(4634748, 4635000, 6082465, 6082700, 10000000),
		[]map[string]any{
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
				freezeTx, 3, 4634800, 1700000000),
		})
	runID := runFreeze(t, svc, path)
	run := runStatus(t, q, runID)
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)
	require.True(t, run.SnapshotVersion.Valid)
	assert.Contains(t, run.SnapshotVersion.String, "freeze-10000000-")
	require.True(t, run.CursorValue.Valid)
	assert.Contains(t, run.CursorValue.String, usdtContract+":4635000")

	fp := stablefreeze.Fingerprint("ethereum", freezeWallet, "FROZEN", "usdt_blacklist")
	lbl := labelRow(t, q, fp)
	assert.Equal(t, "usdt_blacklist", lbl.Label)
	assert.Equal(t, "verified", lbl.VerificationStatus)
	assert.Equal(t, "high", lbl.Confidence)
	assert.Equal(t, "FROZEN", lbl.UpstreamCategory.String)
	assert.False(t, lbl.EntityID.Valid) // never invent an issuer entity
	assert.False(t, lbl.RevokedAt.Valid)
	assert.Equal(t, "https://etherscan.io/tx/"+freezeTx, lbl.SourceUrl.String)
	require.True(t, lbl.ObservedAt.Valid)
	assert.Equal(t, time.Unix(1700000000, 0).UTC(), lbl.ObservedAt.Time.UTC())

	var ev map[string]any
	require.NoError(t, json.Unmarshal(lbl.Evidence, &ev))
	for _, k := range []string{"contract", "event", "tx_hash", "block_number", "block_ts", "topic"} {
		assert.Contains(t, ev, k)
	}
	assert.Equal(t, "AddedBlackList", ev["event"])
	assert.Equal(t, usdtContract, ev["contract"])

	// Category is the seeded FROZEN taxonomy row; source is the new one.
	assert.Equal(t, int64(1), count(t, pool,
		`SELECT count(*) FROM wallet_labels wl JOIN risk_categories rc ON rc.id=wl.category_id
		 JOIN data_sources ds ON ds.id=wl.source_id
		 WHERE wl.fingerprint=$1 AND rc.code='FROZEN' AND ds.code='stablecoin_freeze'`, fp))
	// Wallet row was ensured as evm on ethereum.
	assert.Equal(t, int64(1), count(t, pool,
		`SELECT count(*) FROM wallets w JOIN blockchain_networks bn ON bn.id=w.network_id
		 WHERE w.normalized_address=$1 AND bn.code='ethereum' AND w.address_type='evm'`, freezeWallet))

	// Cursors advanced to the window bounds.
	states, err := q.GetFreezeScanStates(ctx)
	require.NoError(t, err)
	for _, st := range states {
		switch st.ContractAddress {
		case usdtContract:
			assert.Equal(t, int64(4635000), st.LastScannedBlock)
			assert.Equal(t, int64(10000000), st.LastHeadBlock)
		case usdcContract:
			assert.Equal(t, int64(6082700), st.LastScannedBlock)
		}
	}
	// Snapshot row exists under the source.
	assert.Equal(t, int64(1), count(t, pool,
		`SELECT count(*) FROM data_source_snapshots dss JOIN data_sources ds ON ds.id=dss.source_id
		 WHERE ds.code='stablecoin_freeze'`))
}

// Unfreeze revokes ONLY the matching label and merges the revoking tx as
// unfreeze_event evidence.
func TestFreeze_UnfreezeRevokes(t *testing.T) {
	svc, q := newFreezeService(t)
	pool := testutil.RequireTestPool(t)

	p1 := freezeArtifact(t,
		freezeWindows(4634748, 4635000, 6082465, 6082700, 10000000),
		[]map[string]any{
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
				freezeTx, 3, 4634800, 1700000000),
		})
	require.Equal(t, "COMPLETED", runStatus(t, q, runFreeze(t, svc, p1)).Status)

	p2 := freezeArtifact(t,
		freezeWindows(4635001, 4636000, 6082701, 6083000, 10000100),
		[]map[string]any{
			freezeEv(usdtContract, "RemovedBlackList", "unfreeze", freezeWallet,
				unfreezeTx, 1, 4635500, 1700001000),
		})
	run := runStatus(t, q, runFreeze(t, svc, p2))
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)

	fp := stablefreeze.Fingerprint("ethereum", freezeWallet, "FROZEN", "usdt_blacklist")
	lbl := labelRow(t, q, fp)
	assert.True(t, lbl.RevokedAt.Valid, "unfreeze must revoke")
	var ev map[string]any
	require.NoError(t, json.Unmarshal(lbl.Evidence, &ev))
	uf, ok := ev["unfreeze_event"].(map[string]any)
	require.True(t, ok, "unfreeze_event evidence must merge")
	assert.Equal(t, unfreezeTx, uf["tx_hash"])
	// Original freeze evidence survives the merge.
	assert.Equal(t, "AddedBlackList", ev["event"])
	assert.Equal(t, int64(1), count(t, pool,
		`SELECT count(*) FROM wallet_labels WHERE fingerprint=$1`, fp))
}

// Destroy appends lifecycle evidence and NEVER revokes — even when it is
// the latest event for the fingerprint.
func TestFreeze_DestroyNeverRevokes(t *testing.T) {
	svc, q := newFreezeService(t)

	p1 := freezeArtifact(t,
		freezeWindows(4634748, 4635000, 6082465, 6082700, 10000000),
		[]map[string]any{
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
				freezeTx, 3, 4634800, 1700000000),
		})
	require.Equal(t, "COMPLETED", runStatus(t, q, runFreeze(t, svc, p1)).Status)

	p2 := freezeArtifact(t,
		freezeWindows(4635001, 4636000, 6082701, 6083000, 10000100),
		[]map[string]any{
			freezeEv(usdtContract, "DestroyedBlackFunds", "destroy", freezeWallet,
				destroyTx, 2, 4635500, 1700001000),
		})
	run := runStatus(t, q, runFreeze(t, svc, p2))
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)

	fp := stablefreeze.Fingerprint("ethereum", freezeWallet, "FROZEN", "usdt_blacklist")
	lbl := labelRow(t, q, fp)
	assert.False(t, lbl.RevokedAt.Valid, "destroy must never revoke")
	var ev map[string]any
	require.NoError(t, json.Unmarshal(lbl.Evidence, &ev))
	de, ok := ev["destroy_events"].(map[string]any)
	require.True(t, ok, "destroy_events must be appended")
	marker, ok := de[destroyTx+":2"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "DestroyedBlackFunds", marker["event"])
	assert.Equal(t, destroyTx, marker["tx_hash"])
}

// A destroy with no prior active label still materializes FROZEN — the
// event itself proves the address was blacklisted.
func TestFreeze_OrphanDestroyCreatesLabel(t *testing.T) {
	svc, q := newFreezeService(t)

	path := freezeArtifact(t,
		freezeWindows(4634748, 4635000, 6082465, 6082700, 10000000),
		[]map[string]any{
			freezeEv(usdtContract, "DestroyedBlackFunds", "destroy", freezeWallet,
				destroyTx, 2, 4634800, 1700000000),
		})
	run := runStatus(t, q, runFreeze(t, svc, path))
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)

	fp := stablefreeze.Fingerprint("ethereum", freezeWallet, "FROZEN", "usdt_blacklist")
	lbl := labelRow(t, q, fp)
	assert.False(t, lbl.RevokedAt.Valid)
	assert.Equal(t, "DestroyedBlackFunds", mustJSONMap(t, lbl.Evidence)["event"])
}

// An unfreeze for a fingerprint with no label materializes an
// already-revoked label — the event proves the address WAS blacklisted.
func TestFreeze_OrphanUnfreezeCreatesRevokedLabel(t *testing.T) {
	svc, q := newFreezeService(t)

	path := freezeArtifact(t,
		freezeWindows(4634748, 4635000, 6082465, 6082700, 10000000),
		[]map[string]any{
			freezeEv(usdtContract, "RemovedBlackList", "unfreeze", freezeWallet,
				unfreezeTx, 1, 4634800, 1700000000),
		})
	run := runStatus(t, q, runFreeze(t, svc, path))
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)

	fp := stablefreeze.Fingerprint("ethereum", freezeWallet, "FROZEN", "usdt_blacklist")
	lbl := labelRow(t, q, fp)
	assert.True(t, lbl.RevokedAt.Valid, "orphan unfreeze lands already-revoked")
	assert.Equal(t, "RemovedBlackList", mustJSONMap(t, lbl.Evidence)["event"])
}

// Absence from a later window is NOT a revoke — a run that scans forward
// with no events for the wallet leaves the label active.
func TestFreeze_AbsenceIsNotRevoke(t *testing.T) {
	svc, q := newFreezeService(t)

	p1 := freezeArtifact(t,
		freezeWindows(4634748, 4635000, 6082465, 6082700, 10000000),
		[]map[string]any{
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
				freezeTx, 3, 4634800, 1700000000),
		})
	require.Equal(t, "COMPLETED", runStatus(t, q, runFreeze(t, svc, p1)).Status)

	// Second run: windows advance, zero events anywhere.
	p2 := freezeArtifact(t,
		freezeWindows(4635001, 4636000, 6082701, 6083000, 10000100),
		nil)
	run := runStatus(t, q, runFreeze(t, svc, p2))
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)

	fp := stablefreeze.Fingerprint("ethereum", freezeWallet, "FROZEN", "usdt_blacklist")
	lbl := labelRow(t, q, fp)
	assert.False(t, lbl.RevokedAt.Valid, "absence must never revoke")
}

// Reprocessing the same events under a different head (new checksum, same
// fingerprints) must not duplicate labels — the fingerprint upsert merges.
func TestFreeze_IdempotentReprocess(t *testing.T) {
	svc, q := newFreezeService(t)
	pool := testutil.RequireTestPool(t)

	ev := freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
		freezeTx, 3, 4634800, 1700000000)
	p1 := freezeArtifact(t,
		freezeWindows(4634748, 4635000, 6082465, 6082700, 10000000),
		[]map[string]any{ev})
	require.Equal(t, "COMPLETED", runStatus(t, q, runFreeze(t, svc, p1)).Status)

	// Same event, new window/head → different checksum → publish re-runs.
	p2 := freezeArtifact(t,
		freezeWindows(4634748, 4635001, 6082465, 6082701, 10000200),
		[]map[string]any{ev})
	run := runStatus(t, q, runFreeze(t, svc, p2))
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)

	fp := stablefreeze.Fingerprint("ethereum", freezeWallet, "FROZEN", "usdt_blacklist")
	assert.Equal(t, int64(1), count(t, pool,
		`SELECT count(*) FROM wallet_labels WHERE fingerprint=$1`, fp))
	lbl := labelRow(t, q, fp)
	assert.False(t, lbl.RevokedAt.Valid)
}

// Re-freeze after unfreeze revives the SAME label row (fingerprint
// stable, evidence merged, revoked_at cleared) — never a duplicate.
func TestFreeze_RefreezeRevives(t *testing.T) {
	svc, q := newFreezeService(t)
	pool := testutil.RequireTestPool(t)

	mk := func(from, to, head int64, ev map[string]any) string {
		return freezeArtifact(t,
			freezeWindows(from, to, 6082465, 6082700, head),
			[]map[string]any{ev})
	}
	require.Equal(t, "COMPLETED", runStatus(t, q, runFreeze(t, svc,
		mk(4634748, 4635000, 10000000,
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet, freezeTx, 3, 4634800, 1700000000)))).Status)
	require.Equal(t, "COMPLETED", runStatus(t, q, runFreeze(t, svc,
		mk(4635001, 4636000, 10000100,
			freezeEv(usdtContract, "RemovedBlackList", "unfreeze", freezeWallet, unfreezeTx, 1, 4635500, 1700001000)))).Status)
	run := runStatus(t, q, runFreeze(t, svc,
		mk(4636001, 4637000, 10000200,
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
				"0x4444444444444444444444444444444444444444444444444444444444444444", 7, 4636600, 1700002000))))
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)

	fp := stablefreeze.Fingerprint("ethereum", freezeWallet, "FROZEN", "usdt_blacklist")
	lbl := labelRow(t, q, fp)
	assert.False(t, lbl.RevokedAt.Valid, "refreeze revives the label")
	assert.Equal(t, int64(1), count(t, pool,
		`SELECT count(*) FROM wallet_labels WHERE fingerprint=$1`, fp))
	var ev map[string]any
	require.NoError(t, json.Unmarshal(lbl.Evidence, &ev))
	assert.Contains(t, ev, "unfreeze_event") // revoke history preserved
}

// Degenerate windows complete as honest no-ops: header-only, zero events,
// cursors untouched (GREATEST), snapshot row still lands.
func TestFreeze_EmptyWindowCompletes(t *testing.T) {
	svc, q := newFreezeService(t)
	pool := testutil.RequireTestPool(t)
	ctx := context.Background()

	path := freezeArtifact(t,
		[]map[string]any{
			{"address": usdtContract, "from_block": 4634748, "to_block": 4634747,
				"head": 4634750, "empty": true},
			{"address": usdcContract, "from_block": 6082465, "to_block": 6082464,
				"head": 6082466, "empty": true},
		}, nil)
	run := runStatus(t, q, runFreeze(t, svc, path))
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)
	assert.Equal(t, int64(0), count(t, pool,
		`SELECT count(*) FROM wallet_labels wl JOIN data_sources ds ON ds.id=wl.source_id WHERE ds.code='stablecoin_freeze'`))
	states, err := q.GetFreezeScanStates(ctx)
	require.NoError(t, err)
	for _, st := range states {
		assert.Equal(t, st.GenesisBlock-1, st.LastScannedBlock,
			"degenerate window must not move the cursor")
	}
	// Snapshot row exists — a completed (empty) publish is still a publish.
	assert.Equal(t, int64(1), count(t, pool,
		`SELECT count(*) FROM data_source_snapshots dss JOIN data_sources ds ON ds.id=dss.source_id WHERE ds.code='stablecoin_freeze'`))
}

// The cache-version query resolves the latest publish across BOTH
// label-producing sources — a freeze publish invalidates label caches.
func TestFreeze_CacheVersionCoversFreeze(t *testing.T) {
	svc, q := newFreezeService(t)
	ctx := context.Background()

	// Seed an older wallet_attribution snapshot row so the broadened query
	// must choose the newer freeze publish by published_at.
	src, err := q.GetSourceByCode(ctx, "wallet_attribution")
	require.NoError(t, err)
	wattrID := src.ID
	_, err = q.InsertDataSourceSnapshot(ctx, db.InsertDataSourceSnapshotParams{
		SourceID: wattrID, Version: "wattr-old-1", Checksum: "old", RowCount: 1,
	})
	require.NoError(t, err)
	// Push its published_at into the past so ordering is real.
	pool := testutil.RequireTestPool(t)
	_, err = pool.Exec(ctx,
		`UPDATE data_source_snapshots SET published_at = NOW() - interval '1 hour' WHERE version='wattr-old-1'`)
	require.NoError(t, err)

	path := freezeArtifact(t,
		freezeWindows(4634748, 4635000, 6082465, 6082700, 10000000),
		[]map[string]any{
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
				freezeTx, 3, 4634800, 1700000000),
		})
	run := runStatus(t, q, runFreeze(t, svc, path))
	require.Equal(t, "COMPLETED", run.Status)

	snap, err := q.LatestLabelSnapshotPublished(ctx)
	require.NoError(t, err)
	assert.Equal(t, run.SnapshotVersion.String, snap.Version,
		"cache version must follow the freeze publish, not the older wattr row")
	assert.Contains(t, snap.Version, "freeze-")
}

// Malformed artifacts fail the run AND leave cursors untouched — atomic
// boundaries are honest on both sides.
func TestFreeze_ValidateRejectsBadArtifact(t *testing.T) {
	svc, q := newFreezeService(t)
	ctx := context.Background()

	// Event sits above its window's toBlock — impossible on a real scan.
	path := freezeArtifact(t,
		freezeWindows(4634748, 4635000, 6082465, 6082700, 10000000),
		[]map[string]any{
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
				freezeTx, 3, 4639000, 1700000000),
		})
	run := runStatus(t, q, runFreeze(t, svc, path))
	require.Equal(t, "FAILED", run.Status)
	assert.Contains(t, run.ErrorMessage.String, "validate")

	states, err := q.GetFreezeScanStates(ctx)
	require.NoError(t, err)
	for _, st := range states {
		assert.Equal(t, st.GenesisBlock-1, st.LastScannedBlock,
			"failed run must not move cursors")
	}
	// Staging rows for the failed run are kept for diagnosis (or purged by
	// the next run's housekeeping) — either way no label landed.
	assert.Equal(t, int64(0), count(t, testutil.RequireTestPool(t),
		`SELECT count(*) FROM wallet_labels wl JOIN data_sources ds ON ds.id=wl.source_id WHERE ds.code='stablecoin_freeze'`))
}

// Header contract outside the registry is a validation failure.
func TestFreeze_ValidateRejectsUnknownContract(t *testing.T) {
	svc, q := newFreezeService(t)

	path := freezeArtifact(t,
		[]map[string]any{
			{"address": "0x9999999999999999999999999999999999999999",
				"from_block": 1, "to_block": 10, "head": 100, "empty": false},
		}, nil)
	run := runStatus(t, q, runFreeze(t, svc, path))
	require.Equal(t, "FAILED", run.Status)
	assert.Contains(t, run.ErrorMessage.String, "unknown contract")
}

// Malformed event lines land as Rejected — never abort the run.
func TestFreeze_MalformedLinesRejected(t *testing.T) {
	svc, q := newFreezeService(t)
	pool := testutil.RequireTestPool(t)

	path := freezeArtifact(t,
		freezeWindows(4634748, 4635000, 6082465, 6082700, 10000000),
		[]map[string]any{
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
				freezeTx, 3, 4634800, 1700000000),
			freezeEv(usdtContract, "AddedBlackList", "freeze", "not-an-address",
				freezeTx, 4, 4634801, 1700000000),
			freezeEv(usdtContract, "WrongName", "freeze", freezeWallet,
				freezeTx, 5, 4634802, 1700000000),
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
				"0xbad", 6, 4634803, 1700000000),
			freezeEv(usdtContract, "AddedBlackList", "unfreeze", freezeWallet, // type/name mismatch
				freezeTx, 7, 4634804, 1700000000),
		})
	run := runStatus(t, q, runFreeze(t, svc, path))
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)
	assert.Equal(t, int64(4), run.RecordsRejected)
	assert.Equal(t, int64(1), count(t, pool,
		`SELECT count(*) FROM wallet_labels wl JOIN data_sources ds ON ds.id=wl.source_id WHERE ds.code='stablecoin_freeze'`))
}

// Chronological ordering inside one run: freeze+unfreeze in the same
// window ends revoked; unfreeze+freeze ends active.
func TestFreeze_SameWindowOrdering(t *testing.T) {
	svc, q := newFreezeService(t)

	// freeze@b1 + unfreeze@b2 (latest=unfreeze) → revoked.
	p := freezeArtifact(t,
		freezeWindows(4634748, 4636000, 6082465, 6082700, 10000000),
		[]map[string]any{
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
				freezeTx, 3, 4634800, 1700000000),
			freezeEv(usdtContract, "RemovedBlackList", "unfreeze", freezeWallet,
				unfreezeTx, 1, 4635500, 1700001000),
		})
	require.Equal(t, "COMPLETED", runStatus(t, q, runFreeze(t, svc, p)).Status)
	fp := stablefreeze.Fingerprint("ethereum", freezeWallet, "FROZEN", "usdt_blacklist")
	lbl := labelRow(t, q, fp)
	assert.True(t, lbl.RevokedAt.Valid, "latest event unfreeze ⇒ revoked")

	// Next run adds a later freeze — latest=freeze ⇒ revived.
	p2 := freezeArtifact(t,
		freezeWindows(4636001, 4637000, 6082701, 6083000, 10000100),
		[]map[string]any{
			freezeEv(usdtContract, "RemovedBlackList", "unfreeze", freezeWallet,
				unfreezeTx, 2, 4636100, 1700001500),
			freezeEv(usdtContract, "AddedBlackList", "freeze", freezeWallet,
				"0x5555555555555555555555555555555555555555555555555555555555555555", 9, 4636600, 1700002000),
		})
	require.Equal(t, "COMPLETED", runStatus(t, q, runFreeze(t, svc, p2)).Status)
	lbl = labelRow(t, q, fp)
	assert.False(t, lbl.RevokedAt.Valid, "latest event freeze ⇒ active")
}

// USDC events route through topics[1] — same publish path, different label.
func TestFreeze_USDCEvent(t *testing.T) {
	svc, q := newFreezeService(t)

	path := freezeArtifact(t,
		freezeWindows(4634748, 4635000, 6082465, 6082700, 10000000),
		[]map[string]any{
			freezeEv(usdcContract, "Blacklisted", "freeze", freezeWallet,
				freezeTx, 0, 6082500, 1700000000),
		})
	run := runStatus(t, q, runFreeze(t, svc, path))
	require.Equal(t, "COMPLETED", run.Status, "run failed: %s", run.ErrorMessage.String)
	fp := stablefreeze.Fingerprint("ethereum", freezeWallet, "FROZEN", "usdc_blacklist")
	lbl := labelRow(t, q, fp)
	assert.Equal(t, "usdc_blacklist", lbl.Label)
	assert.False(t, lbl.RevokedAt.Valid)
}

// The freeze fingerprint can never collide with a wallet-attribution row
// for the same address+category — different namespace.
func TestFreeze_FingerprintNamespaceSeparation(t *testing.T) {
	wattr := func(networkCode, addr, cat, label string) string {
		// mirror walletattribution.Fingerprint's formula
		sum := sha256Hex("wallet_attribution|" + networkCode + "|" + addr + "|" + cat + "|" + label)
		return sum
	}
	fp := stablefreeze.Fingerprint("ethereum", freezeWallet, "FROZEN", "usdt_blacklist")
	assert.NotEqual(t, fp, wattr("ethereum", freezeWallet, "FROZEN", "usdt_blacklist"))
	assert.Equal(t, fp,
		sha256Hex("stablecoin_freeze|ethereum|"+freezeWallet+"|FROZEN|usdt_blacklist"))
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Admin trigger: the source is registered, so a POST lands a 202 and a
// real run row — and with no fetcher wired in the test harness the run
// lands FAILED ("no downloader configured"), never a fake COMPLETED.
func TestFreeze_AdminTrigger(t *testing.T) {
	srv, pool, _ := testAPIWithAdmin(t)
	adminKey := seedClient(t, pool, "admin", "admin")

	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/api/v1/admin/ingestion-runs",
		strings.NewReader(`{"source":"stablecoin_freeze"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", adminKey)
	w := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code, "body: %s", w.Body.String())

	require.Eventually(t, func() bool {
		return count(t, pool,
			`SELECT count(*) FROM ingestion_runs ir JOIN data_sources ds ON ds.id=ir.source_id
			 WHERE ds.code='stablecoin_freeze' AND ir.status='FAILED'`) == 1
	}, 5*time.Second, 50*time.Millisecond)
}

func mustJSONMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}
