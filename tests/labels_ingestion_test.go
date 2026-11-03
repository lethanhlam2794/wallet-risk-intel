package tests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/ingestion"
	"checkwallet/internal/provider/walletattribution"
)

func runLabelsSync(t *testing.T, svc *ingestion.Service, files string) uuid.UUID {
	t.Helper()
	runID, err := svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: walletattribution.SourceCode, TriggeredBy: "test", FilePath: files,
	})
	require.NoError(t, err)
	return runID
}

func TestLabels_PublishFixture(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, false)

	runID := runLabelsSync(t, svc, "testdata/labels_eth.csv,testdata/labels_bsc.csv")
	run := runStatus(t, q, runID)
	require.Equal(t, "COMPLETED", run.Status, run.ErrorMessage.String)
	require.Contains(t, run.SnapshotVersion.String, "wattr-")
	assert.Equal(t, int64(7), run.RecordsFetched) // 5 eth + 2 bsc data rows
	assert.Equal(t, int64(7), run.RecordsInserted)

	// Live rows landed.
	assert.Equal(t, int64(7), count(t, pool, "SELECT count(*) FROM wallet_labels"))
	assert.Equal(t, int64(7), count(t, pool, "SELECT count(*) FROM wallets"))
	// Snapshot registered for the source (not in OFAC's registry).
	assert.Equal(t, int64(1), count(t, pool,
		"SELECT count(*) FROM data_source_snapshots ds JOIN data_sources s ON s.id=ds.source_id WHERE s.code='wallet_attribution'"))
	// No staging residue after success.
	assert.Equal(t, int64(0), count(t, pool,
		"SELECT count(*) FROM stg_wallet_labels WHERE run_id=$1", runID))
	// Categories resolve to seeded types (never invented).
	var catType string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT rc.category_type FROM wallet_labels wl
		 JOIN risk_categories rc ON rc.id = wl.category_id
		 WHERE wl.label = 'Phish Kit'`).Scan(&catType))
	assert.Equal(t, "MALICIOUS_ACTIVITY", catType)
	// 'Null' entity is literal evidence, not an empty link.
	var entName string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT e.name FROM wallet_labels wl JOIN entities e ON e.id=wl.entity_id
		 WHERE wl.label = 'Null Entity Label'`).Scan(&entName))
	assert.Equal(t, "Null", entName)
	// Empty entity → NULL link but raw kept in evidence.
	var hasEnt bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT wl.entity_id IS NOT NULL FROM wallet_labels wl WHERE wl.label='Mixer One'`).Scan(&hasEnt))
	assert.False(t, hasEnt)
}

func TestLabels_ChecksumNoOp(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, false)
	files := "testdata/labels_eth.csv,testdata/labels_bsc.csv"

	runLabelsSync(t, svc, files)
	v1 := runStatus(t, q, runLabelsSync(t, svc, files)).SnapshotVersion.String
	assert.Equal(t, int64(7), count(t, pool, "SELECT count(*) FROM wallet_labels"))
	assert.Equal(t, int64(1), count(t, pool, "SELECT count(*) FROM data_source_snapshots"))
	require.NotEmpty(t, v1)
}

func TestLabels_NetworkScopedRevoke(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, false)

	runLabelsSync(t, svc, "testdata/labels_eth.csv,testdata/labels_bsc.csv")
	// Eth-only republish with one dropped row → revokes only within eth scope.
	runID := runLabelsSync(t, svc, "testdata/labels_eth_reduced.csv")
	run := runStatus(t, q, runID)
	require.Equal(t, "COMPLETED", run.Status, run.ErrorMessage.String)

	// bsc labels untouched — the staged network set bounds revocation.
	assert.Equal(t, int64(2), count(t, pool,
		`SELECT count(*) FROM wallet_labels wl JOIN wallets w ON w.id=wl.wallet_id
		 JOIN blockchain_networks bn ON bn.id=w.network_id
		 WHERE bn.code='bsc' AND wl.revoked_at IS NULL`))
	// The one dropped eth row is soft-revoked; the other 4 stay live.
	assert.Equal(t, int64(4), count(t, pool,
		`SELECT count(*) FROM wallet_labels wl JOIN wallets w ON w.id=wl.wallet_id
		 JOIN blockchain_networks bn ON bn.id=w.network_id
		 WHERE bn.code='ethereum' AND wl.revoked_at IS NULL`))
	assert.Equal(t, int64(1), count(t, pool,
		`SELECT count(*) FROM wallet_labels WHERE label='Null Entity Label' AND revoked_at IS NOT NULL`))
}

func TestLabels_ReviveKeepsFirstSeen(t *testing.T) {
	svc, _, pool, _ := newIngestionService(t, false)

	runLabelsSync(t, svc, "testdata/labels_eth.csv,testdata/labels_bsc.csv")
	var firstSeen any
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT first_seen_at FROM wallet_labels WHERE label='Null Entity Label'`).Scan(&firstSeen))

	runLabelsSync(t, svc, "testdata/labels_eth_reduced.csv") // drops the row → revoked
	runLabelsSync(t, svc, "testdata/labels_eth.csv")         // re-adds → revive

	var revived, sameFirst bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT revoked_at IS NULL, first_seen_at = $1 FROM wallet_labels WHERE label='Null Entity Label'`,
		firstSeen).Scan(&revived, &sameFirst))
	assert.True(t, revived, "re-listed label must be revived")
	assert.True(t, sameFirst, "first_seen_at must survive revive")
}

func TestLabels_HeaderOnlyFileFails(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, false)

	runLabelsSync(t, svc, "testdata/labels_eth.csv,testdata/labels_bsc.csv")
	before := count(t, pool, "SELECT count(*) FROM wallet_labels WHERE revoked_at IS NULL")

	runID := runLabelsSync(t, svc, "testdata/labels_eth.csv,testdata/labels_header_only.csv")
	run := runStatus(t, q, runID)
	require.Equal(t, "FAILED", run.Status)
	assert.Contains(t, run.ErrorMessage.String, "0 staged rows")
	// Failed validation → zero revocations; live untouched.
	assert.Equal(t, before, count(t, pool,
		"SELECT count(*) FROM wallet_labels WHERE revoked_at IS NULL"))
	assert.Equal(t, int64(0), count(t, pool,
		"SELECT count(*) FROM wallet_labels WHERE revoked_at IS NOT NULL"))
}

func TestLabels_ConflictingLabelsCoexist(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, false)

	runID := runLabelsSync(t, svc, "testdata/labels_coexist.csv")
	require.Equal(t, "COMPLETED", runStatus(t, q, runID).Status)

	// Same wallet, two different categories → two coexisting labels.
	assert.Equal(t, int64(2), count(t, pool,
		`SELECT count(*) FROM wallet_labels wl JOIN wallets w ON w.id=wl.wallet_id
		 WHERE w.normalized_address='0x00000000000000000000000000000000000000c1'`))
}

func TestLabels_ConcurrentRejected(t *testing.T) {
	svc, _, pool, _ := newIngestionService(t, false)

	// Insert a RUNNING row to simulate an in-flight run, then try to start
	// another — the gate must reject.
	_, err := pool.Exec(context.Background(),
		`INSERT INTO ingestion_runs (id, source_id, status, triggered_by)
		 SELECT gen_random_uuid(), id, 'RUNNING', 'other' FROM data_sources WHERE code='wallet_attribution'`)
	require.NoError(t, err)
	_, err = svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: walletattribution.SourceCode, TriggeredBy: "test",
		FilePath: "testdata/labels_eth.csv"})
	require.ErrorIs(t, err, ingestion.ErrIngestionRunning)
}

func TestLabels_EntityDedupAndDynamicCategory(t *testing.T) {
	svc, q, pool, _ := newIngestionService(t, false)

	runID := runLabelsSync(t, svc, "testdata/labels_dedup.csv")
	require.Equal(t, "COMPLETED", runStatus(t, q, runID).Status)

	// 'Binance ' and 'Binance' collapse to ONE entity row shared by all
	// three label rows (whitespace canonicalized at write).
	assert.Equal(t, int64(1), count(t, pool,
		`SELECT count(*) FROM entities WHERE LOWER(TRIM(regexp_replace(name,'\s+',' ','g')))='binance'`))
	assert.Equal(t, int64(3), count(t, pool,
		`SELECT count(*) FROM wallet_labels wl JOIN entities e ON e.id=wl.entity_id WHERE e.name='Binance'`))
	// Empty entity → NULL link.
	var entNull bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT entity_id IS NULL FROM wallet_labels WHERE label='Empty Entity'`).Scan(&entNull))
	assert.True(t, entNull)
	// Unseen upstream category 'brandnewcat' → dynamic row, type OTHER.
	var catType string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT rc.category_type FROM wallet_labels wl JOIN risk_categories rc ON rc.id=wl.category_id
		 WHERE wl.upstream_category='brandnewcat'`).Scan(&catType))
	assert.Equal(t, "OTHER", catType)
	// Mixed-case address: verbatim in wallets.address, lowercased normalized.
	var addr, norm string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT address, normalized_address FROM wallets WHERE normalized_address='0x00000000000000000000000000000000000000d6'`).Scan(&addr, &norm))
	assert.Equal(t, "0x00000000000000000000000000000000000000D6", addr)
	assert.Equal(t, "0x00000000000000000000000000000000000000d6", norm)
}
