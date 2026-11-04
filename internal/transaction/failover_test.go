package transaction

// Phase-12 failover-chain tests: ordered walk, per-source watermarks,
// restricted failure kinds, disabled skip, terminal empty pages,
// shared+per-provider budget accounting.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/testutil"
	"checkwallet/internal/wallet"
)

func newSvcChain(t *testing.T, pool *pgxpool.Pool, chain []*stubTx,
	budgets map[string]int) (*Service, *db.Queries, network.Network) {
	t.Helper()
	q := db.New(pool)
	net := testNet(t, pool)
	adapters := make([]provider.TxProvider, 0, len(chain))
	for _, s := range chain {
		adapters = append(adapters, s)
	}
	svc := New(pool, q, map[string][]provider.TxProvider{net.Code: adapters},
		wallet.NewService(q), Config{
			FreshTTL: 10 * time.Minute, NegativeTTL: time.Minute,
			StaleAfter: 15 * time.Minute, PageSize: 100,
			MaxPagesHop0: 5, MaxPagesHopN: 2,
			ReorgTail: map[string]int64{net.Code: 4},
			Budget:    budgets,
		}, nil)
	return svc, q, net
}

func getStateSrc(t *testing.T, pool *pgxpool.Pool, netID, walletID int64,
	source string) db.WalletIndexState {
	t.Helper()
	var s db.WalletIndexState
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT network_id, wallet_id, source_id, indexed_until_block,
		        indexed_at, status, error, rows_indexed,
		        rows_skipped_missing_contract, rows_skipped_invalid_address,
		        history_truncated, updated_at
		 FROM wallet_index_state
		 WHERE network_id=$1 AND wallet_id=$2
		   AND source_id = (SELECT id FROM data_sources WHERE code=$3)`,
		netID, walletID, source).Scan(&s.NetworkID, &s.WalletID, &s.SourceID,
		&s.IndexedUntilBlock, &s.IndexedAt, &s.Status, &s.Error,
		&s.RowsIndexed, &s.RowsSkippedMissingContract,
		&s.RowsSkippedInvalidAddress, &s.HistoryTruncated, &s.UpdatedAt))
	return s
}

func walletID(t *testing.T, pool *pgxpool.Pool, netID int64, addr string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT id FROM wallets WHERE network_id=$1 AND normalized_address=$2`,
		netID, addr).Scan(&id))
	return id
}

// Primary healthy → secondary sees ZERO traffic AND zero state usage.
func TestFailover_PrimaryHealthyNoSecondaryTraffic(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	pri.head = 100
	pri.pages[addrRoot] = [][]provider.Transfer{{
		tf("txlist", provider.TransferKindNative, "0xfa1", addrA, addrRoot, "", "3", 90, false),
	}}
	sec := newStub("nodereal")
	sec.head = 500
	svc, _, net := newSvcChain(t, pool, []*stubTx{pri, sec},
		map[string]int{"chainscan": 8, "nodereal": 8})

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	assert.Equal(t, int64(1), pri.headCalls.Load())
	assert.Equal(t, int64(0), sec.headCalls.Load())
	assert.Equal(t, int64(0), sec.lookCalls.Load())
	assert.Equal(t, int64(2), out.ProviderCalls, "head + one real page")
}

// Failover-kind error on primary → secondary serves from ITS OWN
// watermark; primary's row records the typed failure.
func TestFailover_PrimaryRateLimitedSecondaryServes(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	pri.headErr = provider.Errorf(provider.ErrRateLimited, "429")
	sec := newStub("nodereal")
	sec.head = 500
	sec.pages[addrRoot] = [][]provider.Transfer{{
		tf("tx", provider.TransferKindNative, "0xfb1", addrA, addrRoot, "", "7", 495, false),
	}}
	svc, _, net := newSvcChain(t, pool, []*stubTx{pri, sec},
		map[string]int{"chainscan": 8, "nodereal": 8})

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	require.NotNil(t, out.IndexedUntilBlock)
	assert.Equal(t, int64(500), *out.IndexedUntilBlock,
		"secondary's own watermark serves — not the primary's")
	require.NotNil(t, out.ProviderStatus)
	assert.Equal(t, "ok", *out.ProviderStatus)
	wid := walletID(t, pool, net.ID, addrRoot)
	ps := getStateSrc(t, pool, net.ID, wid, "chainscan")
	assert.Equal(t, StatusFailed, ps.Status)
	assert.Equal(t, "rate_limited", ps.Error.String)
	ss := getStateSrc(t, pool, net.ID, wid, "nodereal")
	assert.Equal(t, StatusFresh, ss.Status)
	// Secondary's earlier coverage is not wiped by the primary's row —
	// and the primary's failure doesn't touch the secondary row.
	assert.Equal(t, int64(1), pri.headCalls.Load())
	assert.Equal(t, int64(0), pri.lookCalls.Load())
	assert.Equal(t, int64(1), sec.headCalls.Load())
}

// Mid-flight failover failure discards the partial batch — no rows
// upserted for the failed attempt; secondary's batch alone persists.
func TestFailover_PartialBatchDiscarded(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	pri.head = 300
	pri.pages[addrRoot] = [][]provider.Transfer{
		{tf("txlist", provider.TransferKindNative, "0xfc1", addrA, addrRoot, "", "1", 290, false)},
	}
	pri.lookEr[addrRoot] = provider.Errorf(provider.ErrDegraded, "degraded payload")
	sec := newStub("nodereal")
	sec.head = 300
	sec.pages[addrRoot] = [][]provider.Transfer{{
		tf("tx", provider.TransferKindNative, "0xfc2", addrA, addrRoot, "", "1", 295, false),
	}}
	svc, _, net := newSvcChain(t, pool, []*stubTx{pri, sec},
		map[string]int{"chainscan": 8, "nodereal": 8})

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	assert.Equal(t, 1, countRows(t, pool, net.ID),
		"only the winning attempt's batch persists — primary's page discarded")
	wid := walletID(t, pool, net.ID, addrRoot)
	ps := getStateSrc(t, pool, net.ID, wid, "chainscan")
	assert.Equal(t, StatusFailed, ps.Status)
	assert.Equal(t, "degraded", ps.Error.String)
}

// Non-failover kinds (client_4xx, malformed) are terminal — the chain
// is never walked on them.
func TestFailover_Client4xxNeverWalks(t *testing.T) {
	for _, kind := range []provider.ErrKind{
		provider.ErrClient4xx, provider.ErrMalformed,
		provider.ErrTimeout, provider.ErrInternal,
	} {
		t.Run(string(kind), func(t *testing.T) {
			pool := testutil.RequireTestPoolDSN(t, testDSN)
			pri := newStub("chainscan")
			pri.headErr = provider.Errorf(kind, "nope")
			sec := newStub("nodereal")
			sec.head = 500
			svc, _, net := newSvcChain(t, pool, []*stubTx{pri, sec},
				map[string]int{"chainscan": 8, "nodereal": 8})

			out, err := svc.EnsureIndexed(context.Background(), net, addrRoot,
				EnsureOptions{})
			require.NoError(t, err)
			assert.Equal(t, StatusFailed, out.IndexStatus)
			require.NotNil(t, out.ProviderStatus)
			assert.Equal(t, string(kind), *out.ProviderStatus)
			assert.Equal(t, int64(0), sec.headCalls.Load(),
				"%s must not fail over", kind)
		})
	}
}

// Disabled middle member is skipped with zero traffic — not a failure.
func TestFailover_DisabledMemberSkipped(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	pri.headErr = provider.Errorf(provider.ErrTransport, "dial")
	mid := newStub("alchemy")
	mid.enabled = false
	sec := newStub("nodereal")
	sec.head = 200
	svc, _, net := newSvcChain(t, pool, []*stubTx{pri, mid, sec},
		map[string]int{"chainscan": 8, "alchemy": 8, "nodereal": 8})

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	assert.Equal(t, int64(0), mid.headCalls.Load(), "disabled member emits nothing")
	assert.Equal(t, int64(0), mid.lookCalls.Load())
	assert.Equal(t, int64(1), sec.headCalls.Load())
}

// All-disabled chain → one disabled verdict under chain[0]'s source.
func TestFailover_AllDisabledTerminal(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	pri.enabled = false
	sec := newStub("nodereal")
	sec.enabled = false
	svc, _, net := newSvcChain(t, pool, []*stubTx{pri, sec},
		map[string]int{"chainscan": 8, "nodereal": 8})

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusDisabled, out.IndexStatus)
	require.NotNil(t, out.ProviderStatus)
	assert.Equal(t, "disabled", *out.ProviderStatus)
	wid := walletID(t, pool, net.ID, addrRoot)
	ps := getStateSrc(t, pool, net.ID, wid, "chainscan")
	assert.Equal(t, StatusDisabled, ps.Status,
		"disabled verdict persisted under the configured primary source")
}

// Every attempted adapter failed → serve the LAST attempt's verdict.
func TestFailover_AllFailServesLastKind(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	pri.headErr = provider.Errorf(provider.ErrRateLimited, "429")
	sec := newStub("nodereal")
	sec.headErr = provider.Errorf(provider.ErrUpstream5xx, "500")
	svc, _, net := newSvcChain(t, pool, []*stubTx{pri, sec},
		map[string]int{"chainscan": 8, "nodereal": 8})

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, out.IndexStatus)
	require.NotNil(t, out.ProviderStatus)
	assert.Equal(t, "upstream_5xx", *out.ProviderStatus,
		"the freshest attempt's kind surfaces")
	wid := walletID(t, pool, net.ID, addrRoot)
	assert.Equal(t, "rate_limited",
		getStateSrc(t, pool, net.ID, wid, "chainscan").Error.String)
	assert.Equal(t, "upstream_5xx",
		getStateSrc(t, pool, net.ID, wid, "nodereal").Error.String)
}

// Empty successful page = terminal success — no chain walk.
func TestFailover_EmptyPageTerminal(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	pri.head = 100
	pri.pages[addrRoot] = [][]provider.Transfer{{}} // empty, finished
	sec := newStub("nodereal")
	sec.head = 500
	svc, _, net := newSvcChain(t, pool, []*stubTx{pri, sec},
		map[string]int{"chainscan": 8, "nodereal": 8})

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	assert.Equal(t, int64(0), sec.headCalls.Load(),
		"successful empty page never walks the chain")
}

// Per-source watermark: secondary resumes from ITS OWN coverage row
// (seeded earlier), not the primary's watermark or zero.
func TestFailover_PerSourceWatermarkResume(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	pri.headErr = provider.Errorf(provider.ErrCircuitOpen, "open")
	sec := newStub("nodereal")
	sec.head = 600
	sec.pages[addrRoot] = [][]provider.Transfer{{
		tf("tx", provider.TransferKindNative, "0xfd1", addrA, addrRoot, "", "1", 590, false),
	}}
	svc, q, net := newSvcChain(t, pool, []*stubTx{pri, sec},
		map[string]int{"chainscan": 8, "nodereal": 8})

	// Seed the secondary's own coverage row: watermark 400.
	w, err := wallet.NewService(q).GetOrCreate(context.Background(),
		net.ID, addrRoot, addrRoot)
	require.NoError(t, err)
	wid := w.ID
	src, err := q.GetSourceByCode(context.Background(), "nodereal")
	require.NoError(t, err)
	_, err = q.UpsertWalletIndexState(context.Background(),
		db.UpsertWalletIndexStateParams{
			NetworkID: net.ID, WalletID: wid, SourceID: src.ID,
			IndexedUntilBlock: pgtype.Int8{Int64: 400, Valid: true},
			IndexedAt:         pgtype.Timestamptz{Time: time.Now(), Valid: true},
			Status:            StatusPartial, HistoryTruncated: true,
		})
	require.NoError(t, err)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	// ReorgTail=4 → fetchFrom = 400-4+1 = 397; the secondary covered
	// through head 600 → its own watermark advanced past its own floor.
	require.NotNil(t, out.IndexedUntilBlock)
	assert.Equal(t, int64(600), *out.IndexedUntilBlock)
	ss := getStateSrc(t, pool, net.ID, wid, "nodereal")
	assert.Equal(t, int64(600), ss.IndexedUntilBlock.Int64)
}

// Budget: shared pool sized by max(chain budgets); a member's own cap
// still bounds ITS calls — a cap stop is partial, never failover.
func TestFailover_BudgetSemantics(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	sec := newStub("nodereal")
	svc, _, net := newSvcChain(t, pool, []*stubTx{pri, sec},
		map[string]int{"chainscan": 2, "nodereal": 9})
	assert.Equal(t, 9, svc.BudgetFor(net), "pool = max(chain budgets)")

	// Primary cap 1: head consumes it → page 1 can't run → partial,
	// NOT a failover (bounds are ours, not provider failures).
	pri2 := newStub("chainscan")
	pri2.head = 100
	pri2.pages[addrRoot] = [][]provider.Transfer{
		{tf("txlist", provider.TransferKindNative, "0xfe1", addrA, addrRoot, "", "1", 90, false)},
	}
	sec2 := newStub("nodereal")
	sec2.head = 100
	svc2, _, net2 := newSvcChain(t, pool, []*stubTx{pri2, sec2},
		map[string]int{"chainscan": 1, "nodereal": 9})
	out, err := svc2.EnsureIndexed(context.Background(), net2, addrRoot,
		EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusPartial, out.IndexStatus)
	assert.Equal(t, int64(0), sec2.headCalls.Load(),
		"per-provider cap is a boundary stop, not a failover")
}
