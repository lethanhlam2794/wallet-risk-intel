package screening

import (
	"context"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/testutil"
)

// Phase-02 contract tests for the three analyzer queries — seeded
// transfers cover confirmed/pending/error/self/NULL-wallet cases.
//
// Wallet layout on the test network:
//
//	cand   the screened wallet (to_wallet of inbound rows)
//	srcA   confirmed inbound source
//	srcB   source whose only inbound is pending (must not surface)
//	srcC   source whose only inbound is is_error (must not surface)
//	outD   outbound counterparty (in scan must not surface it)
func seedExposureRows(t *testing.T, q *db.Queries, netID int64) (
	cand, srcA, srcB, srcC, outD db.Wallet) {
	t.Helper()
	ctx := context.Background()
	mk := func(addr string) db.Wallet {
		w, err := q.GetOrCreateWallet(ctx, db.GetOrCreateWalletParams{
			NetworkID: netID, Address: addr, NormalizedAddress: addr,
			AddressType: "evm"})
		require.NoError(t, err)
		return w
	}
	cand = mk("0x000000000000000000000000000000000000ca01")
	srcA = mk("0x0000000000000000000000000000000000000aa1")
	srcB = mk("0x0000000000000000000000000000000000000bb1")
	srcC = mk("0x0000000000000000000000000000000000000cc1")
	outD = mk("0x0000000000000000000000000000000000000dd1")

	type tx struct {
		hash, idx, typ string
		from, to       pgtype.Int8
		asset          pgtype.Text
		amount         string
		status         string
		isErr          bool
	}
	i8 := func(v int64) pgtype.Int8 { return pgtype.Int8{Int64: v, Valid: true} }
	rows := []tx{
		// Two confirmed inbound from srcA.
		{"0xa1", "0", "NATIVE", i8(srcA.ID), i8(cand.ID),
			pgtype.Text{}, "100", "confirmed", false},
		{"0xa2", "0", "EXTERNAL", i8(srcA.ID), i8(cand.ID),
			pgtype.Text{String: "0xtoken", Valid: true}, "50", "confirmed", false},
		// Pending + error inbound — never reach the analyzer.
		{"0xb1", "0", "NATIVE", i8(srcB.ID), i8(cand.ID),
			pgtype.Text{}, "9", "pending", false},
		{"0xc1", "0", "NATIVE", i8(srcC.ID), i8(cand.ID),
			pgtype.Text{}, "9", "confirmed", true},
		// Self-transfer — excluded even though confirmed.
		{"0xself", "0", "NATIVE", i8(cand.ID), i8(cand.ID),
			pgtype.Text{}, "5", "confirmed", false},
		// NULL counterparty wallet ids must not crash the scan.
		{"0xnull", "0", "NATIVE", pgtype.Int8{}, i8(cand.ID),
			pgtype.Text{}, "7", "confirmed", false},
		// Confirmed outbound to outD.
		{"0xd1", "0", "NATIVE", i8(cand.ID), i8(outD.ID),
			pgtype.Text{}, "3", "confirmed", false},
	}
	for _, r := range rows {
		err := q.UpsertTransactions(ctx, db.UpsertTransactionsParams{
			NetworkID: netID,
			Rows: []byte(`[{"tx_hash":"` + r.hash + `","transfer_index":"` + r.idx +
				`","transfer_type":"` + r.typ + `","from_wallet_id":` +
				jsonInt(r.from) + `,"to_wallet_id":` + jsonInt(r.to) +
				`,"asset_identifier":` + jsonStr(r.asset) +
				`,"amount_raw":"` + r.amount +
				`","confirmation_status":"` + r.status +
				`","is_error":` + boolStr(r.isErr) + `}]`),
		})
		require.NoError(t, err)
	}
	return cand, srcA, srcB, srcC, outD
}

func jsonInt(v pgtype.Int8) string {
	if !v.Valid {
		return "null"
	}
	return strconv.FormatInt(v.Int64, 10)
}
func jsonStr(v pgtype.Text) string {
	if !v.Valid {
		return "null"
	}
	return `"` + v.String + `"`
}
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestListConfirmedCounterpartyTransfers(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	q := db.New(pool)
	cand, srcA, _, _, outD := seedExposureRows(t, q, 1)
	ctx := context.Background()

	in, err := q.ListConfirmedCounterpartyTransfers(ctx,
		db.ListConfirmedCounterpartyTransfersParams{
			NetworkID: 1, WalletID: cand.ID, Direction: "in", Lim: 100})
	require.NoError(t, err)

	var cps []string
	var aCount int
	for _, r := range in {
		cps = append(cps, r.TxHash)
		if r.CounterpartyAddress == "0x0000000000000000000000000000000000000aa1" {
			aCount++
		}
	}
	assert.ElementsMatch(t, []string{"0xa1", "0xa2", "0xnull"}, cps,
		"only confirmed, non-error, non-self inbound rows may surface")
	assert.Equal(t, 2, aCount)
	assert.Equal(t, srcA.ID,
		in[0].CounterpartyWalletID)

	out, err := q.ListConfirmedCounterpartyTransfers(ctx,
		db.ListConfirmedCounterpartyTransfersParams{
			NetworkID: 1, WalletID: cand.ID, Direction: "out", Lim: 100})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "0xd1", out[0].TxHash)
	assert.Equal(t, "0x0000000000000000000000000000000000000dd1",
		out[0].CounterpartyAddress)
	assert.Equal(t, outD.ID, out[0].CounterpartyWalletID)

	// Bounded: LIMIT is honored.
	in1, err := q.ListConfirmedCounterpartyTransfers(ctx,
		db.ListConfirmedCounterpartyTransfersParams{
			NetworkID: 1, WalletID: cand.ID, Direction: "in", Lim: 1})
	require.NoError(t, err)
	assert.Len(t, in1, 1)
}

func TestListWalletIndexStates_Bulk(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	q := db.New(pool)
	ctx := context.Background()

	mk := func(addr string) db.Wallet {
		w, err := q.GetOrCreateWallet(ctx, db.GetOrCreateWalletParams{
			NetworkID: 1, Address: addr, NormalizedAddress: addr,
			AddressType: "evm"})
		require.NoError(t, err)
		return w
	}
	w1 := mk("0x00000000000000000000000000000000000000e1")
	w2 := mk("0x00000000000000000000000000000000000000e2")
	w3 := mk("0x00000000000000000000000000000000000000e3")

	_, err := q.UpsertWalletIndexState(ctx, db.UpsertWalletIndexStateParams{
		NetworkID: 1, WalletID: w1.ID, SourceID: 1,
		Status: "fresh", IndexedUntilBlock: pgtype.Int8{Int64: 10, Valid: true},
		IndexedAt: pgtype.Timestamptz{Valid: true}})
	require.NoError(t, err)
	_, err = q.UpsertWalletIndexState(ctx, db.UpsertWalletIndexStateParams{
		NetworkID: 1, WalletID: w2.ID, SourceID: 1,
		Status:           "partial",
		HistoryTruncated: true})
	require.NoError(t, err)

	rows, err := q.ListWalletIndexStates(ctx, db.ListWalletIndexStatesParams{
		NetworkID: 1, WalletIds: []int64{w1.ID, w2.ID, w3.ID}})
	require.NoError(t, err)
	require.Len(t, rows, 2, "unindexed w3 yields no row — caller marks missing")
	byWallet := map[int64]db.WalletIndexState{}
	for _, r := range rows {
		byWallet[r.WalletID] = r
	}
	assert.Equal(t, "fresh", byWallet[w1.ID].Status)
	assert.Equal(t, "partial", byWallet[w2.ID].Status)
	assert.True(t, byWallet[w2.ID].HistoryTruncated)
}

func TestBulkMatchSanctionedAddresses(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	q := db.New(pool)
	ctx := context.Background()

	// Seed a data_source + entity + live listing on network 1.
	var srcID int64
	err := pool.QueryRow(ctx,
		`INSERT INTO data_sources (code, name, source_type)
		 VALUES ('test_san', 'test', 'bulk')
		 ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id`).Scan(&srcID)
	require.NoError(t, err)
	var entID int64
	err = pool.QueryRow(ctx,
		`INSERT INTO sanctions_entities
		   (source_id, external_entity_id, primary_name)
		 VALUES ($1, 't-entity-1', 'Test Entity')
		 ON CONFLICT (source_id, external_entity_id)
		 DO UPDATE SET primary_name = EXCLUDED.primary_name
		 RETURNING id`, srcID).Scan(&entID)
	require.NoError(t, err)

	listed := "0x0000000000000000000000000000000000005d11"
	revoked := "0x0000000000000000000000000000000000005d12"
	otherNet := "0x0000000000000000000000000000000000005d13"
	for addr, rev := range map[string]bool{listed: false, revoked: true} {
		_, err = pool.Exec(ctx,
			`INSERT INTO sanctions_wallets
			   (sanctions_entity_id, listed_address, normalized_address,
			    currency_code, network_id, identifier_type, revoked_at)
			 VALUES ($1, $2, $2, 'ETH', 1, 'ETH',
			   CASE WHEN $3 THEN NOW() ELSE NULL END)`,
			entID, addr, rev)
		require.NoError(t, err)
	}
	// Same address pattern on a different network must not match.
	_, err = pool.Exec(ctx,
		`INSERT INTO blockchain_networks
		   (namespace, chain_reference, code, name)
		 VALUES ('eip155', '56', 'bsc', 'BNB Smart Chain')
		 ON CONFLICT (namespace, chain_reference) DO NOTHING`)
	require.NoError(t, err)
	var bscID int64
	err = pool.QueryRow(ctx,
		`SELECT id FROM blockchain_networks WHERE code = 'bsc'`).Scan(&bscID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO sanctions_wallets
		   (sanctions_entity_id, listed_address, normalized_address,
		    currency_code, network_id, identifier_type)
		 VALUES ($1, $2, $2, 'BNB', $3, 'BSC')`, entID, otherNet, bscID)
	require.NoError(t, err)

	rows, err := q.BulkMatchSanctionedAddresses(ctx,
		db.BulkMatchSanctionedAddressesParams{
			NetworkID: 1,
			Addresses: []string{listed, revoked, otherNet,
				"0x0000000000000000000000000000000000000000"},
		})
	require.NoError(t, err)
	require.Len(t, rows, 1, "only the live same-network listing matches")
	assert.Equal(t, listed, rows[0].NormalizedAddress)
	assert.Equal(t, "Test Entity", rows[0].PrimaryName)
}
