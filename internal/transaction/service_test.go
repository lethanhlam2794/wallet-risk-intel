package transaction

import (
	"context"
	"sync"
	"sync/atomic"
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

const testDSN = "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_transaction_test?sslmode=disable"

var (
	addrRoot = "0x00000000000000000000000000000000000000a1"
	addrA    = "0x00000000000000000000000000000000000000a2"
	addrB    = "0x00000000000000000000000000000000000000b1"
	tokenC   = "0x0000000000000000000000000000000000000c01"
	ts0      = time.Unix(1700000000, 0).UTC()
)

// stubTx is a scriptable TxProvider — pages consumed per LookupTransfers
// call per address; nil pages means finished.
type stubTx struct {
	name    string
	enabled bool
	head    int64
	headErr error

	headCalls atomic.Int64
	lookCalls atomic.Int64

	mu      sync.Mutex
	pages   map[string][][]provider.Transfer // addr → sequential pages
	idx     map[string]int
	lookEr  map[string]error
	streams []provider.StreamStatus
}

func newStub(name string) *stubTx {
	return &stubTx{name: name, enabled: true,
		pages: map[string][][]provider.Transfer{},
		idx:   map[string]int{}, lookEr: map[string]error{}}
}

func (s *stubTx) Name() string                   { return s.name }
func (s *stubTx) Enabled(n network.Network) bool { return s.enabled }
func (s *stubTx) HeadBlock(context.Context, network.Network) (int64, error) {
	s.headCalls.Add(1)
	return s.head, s.headErr
}

func (s *stubTx) LookupTransfers(_ context.Context, _ network.Network,
	addr string, _ provider.TransferLookupOpts) (*provider.TransferPage, error) {
	s.lookCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.lookEr[addr]; err != nil {
		return nil, err
	}
	pages := s.pages[addr]
	i := s.idx[addr]
	var tr []provider.Transfer
	if i < len(pages) {
		tr = pages[i]
	}
	s.idx[addr] = i + 1
	next := ""
	if i+1 < len(pages) {
		next = "more"
	}
	return &provider.TransferPage{
		Transfers: tr, NextPageToken: next,
		Streams: s.streams, UpstreamCalls: 1}, nil
}

func tf(stream, kind, hash, from, to, asset, amount string, blk int64, isErr bool) provider.Transfer {
	return provider.Transfer{
		TxHash: hash, Stream: stream, Type: kind, From: from, To: to,
		Asset: asset, AmountRaw: amount, BlockNumber: blk,
		BlockTimestamp: ts0.Add(time.Duration(blk) * time.Second),
		Confirmations:  5,
		IsError:        isErr,
	}
}

func testNet(t *testing.T, pool *pgxpool.Pool) network.Network {
	t.Helper()
	var net network.Network
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT id, code, name, chain_reference, namespace FROM blockchain_networks
		 WHERE code='ethereum'`).Scan(
		&net.ID, &net.Code, &net.Name, &net.ChainReference, &net.Namespace))
	return net
}

func newSvc(t *testing.T, pool *pgxpool.Pool, stub provider.TxProvider) (*Service, *db.Queries, network.Network) {
	t.Helper()
	q := db.New(pool)
	net := testNet(t, pool)
	svc := New(pool, q, map[string][]provider.TxProvider{net.Code: {stub}},
		wallet.NewService(q), Config{
			FreshTTL: 10 * time.Minute, NegativeTTL: time.Minute,
			StaleAfter: 15 * time.Minute, PageSize: 100,
			MaxPagesHop0: 5, MaxPagesHopN: 2,
			ReorgTail: map[string]int64{net.Code: 4},
			Budget:    map[string]int{stub.Name(): 32},
		}, nil)
	return svc, q, net
}

func countRows(t *testing.T, pool *pgxpool.Pool, netID int64) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM transactions WHERE network_id=$1`, netID).Scan(&n))
	return n
}

func getState(t *testing.T, pool *pgxpool.Pool, netID, walletID int64) db.WalletIndexState {
	t.Helper()
	var s db.WalletIndexState
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT network_id, wallet_id, source_id, indexed_until_block,
		        indexed_at, status, error, rows_indexed,
		        rows_skipped_missing_contract, rows_skipped_invalid_address,
		        history_truncated, updated_at
		 FROM wallet_index_state WHERE network_id=$1 AND wallet_id=$2`,
		netID, walletID).Scan(&s.NetworkID, &s.WalletID, &s.SourceID,
		&s.IndexedUntilBlock, &s.IndexedAt, &s.Status, &s.Error,
		&s.RowsIndexed, &s.RowsSkippedMissingContract,
		&s.RowsSkippedInvalidAddress, &s.HistoryTruncated, &s.UpdatedAt))
	return s
}

// Test 1/2: fetch → rows persisted; refetch → 0 dupes, identity stable.
func TestEnsureIndexed_BasicAndIdempotent(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[addrRoot] = [][]provider.Transfer{{
		tf("txlist", provider.TransferKindNative, "0xaaa", addrA, addrRoot, "", "1000", 90, false),
		tf("tokentx", provider.TransferKindToken, "0xbbb", addrRoot, addrB, tokenC, "77", 91, false),
	}}
	svc, q, net := newSvc(t, pool, stub)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	assert.Equal(t, 2, countRows(t, pool, net.ID))
	require.NotNil(t, out.IndexedUntilBlock)
	assert.Equal(t, int64(100), *out.IndexedUntilBlock)
	require.NotNil(t, out.ProviderStatus)
	assert.Equal(t, "ok", *out.ProviderStatus)

	res, err := svc.ListTransactions(context.Background(), net, addrRoot, TxFilters{})
	require.NoError(t, err)
	require.Len(t, res.Items, 2)
	assert.Equal(t, "out", res.Items[0].Direction)
	assert.Equal(t, "in", res.Items[1].Direction)
	assert.Equal(t, "confirmed", res.Items[0].ConfirmationStatus)

	// Refetch (refresh bypasses the fresh gate): same content →
	// same transfer_index values, zero duplicates.
	ids0 := transferIndexes(t, pool, net.ID)
	out2, err := svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out2.IndexStatus)
	assert.Equal(t, 2, countRows(t, pool, net.ID))
	assert.Equal(t, ids0, transferIndexes(t, pool, net.ID))
	_ = q
}

func transferIndexes(t *testing.T, pool *pgxpool.Pool, netID int64) map[string]bool {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT tx_hash||'|'||transfer_index FROM transactions WHERE network_id=$1`, netID)
	require.NoError(t, err)
	defer rows.Close()
	m := map[string]bool{}
	for rows.Next() {
		var k string
		require.NoError(t, rows.Scan(&k))
		m[k] = true
	}
	return m
}

// Test 3: DISABLED provider → honest coverage, zero calls.
func TestEnsureIndexed_Disabled(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.enabled = false
	svc, _, net := newSvc(t, pool, stub)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusDisabled, out.IndexStatus)
	require.NotNil(t, out.ProviderStatus)
	assert.Equal(t, string(provider.ErrDisabled), *out.ProviderStatus)
	assert.Equal(t, int64(0), stub.headCalls.Load())
	assert.Equal(t, int64(0), stub.lookCalls.Load())
}

// Test 7: freshness gate — second call inside TTL makes no fetch; stale refetches.
func TestEnsureIndexed_FreshGate(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 50
	svc, _, net := newSvc(t, pool, stub)

	_, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), stub.headCalls.Load())

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), stub.headCalls.Load(), "fresh row must not refetch")
	assert.False(t, out.Fetched)

	// Advance the clock past FreshTTL → stale row refetches.
	now := time.Now()
	svc.now = func() time.Time { return now.Add(11 * time.Minute) }
	out, err = svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), stub.headCalls.Load())
	assert.True(t, out.Fetched)
}

// Tests 8/17/18c: provider failure → failed + typed error; cooldown holds
// under refresh; stale 'indexing' claims recover.
func TestEnsureIndexed_FailureAndCooldown(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.headErr = provider.Errorf(provider.ErrRateLimited, "429")
	svc, q, net := newSvc(t, pool, stub)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, out.IndexStatus)
	require.NotNil(t, out.ProviderStatus)
	assert.Equal(t, string(provider.ErrRateLimited), *out.ProviderStatus)
	w, _ := wallet.NewService(q).GetOrCreate(context.Background(), net.ID, addrRoot, addrRoot)
	st := getState(t, pool, net.ID, w.ID)
	assert.Equal(t, "rate_limited", st.Error.String)

	// refresh=1 inside the cooldown does NOT retry (plan §3.2).
	out, err = svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, out.IndexStatus)
	assert.Equal(t, int64(1), stub.headCalls.Load())

	// Cooldown expired → retry proceeds.
	now := time.Now()
	svc.now = func() time.Time { return now.Add(61 * time.Second) }
	stub.headErr = nil
	stub.head = 60
	out, err = svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	assert.Equal(t, int64(2), stub.headCalls.Load())

	// A stale 'indexing' claim (> StaleAfter) recovers to retryable.
	_, err = pool.Exec(context.Background(),
		`UPDATE wallet_index_state SET status='indexing',
		 updated_at = NOW() - interval '20 minutes'
		 WHERE network_id=$1 AND wallet_id=$2`, net.ID, w.ID)
	require.NoError(t, err)
	svc.now = time.Now
	out, err = svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
}

// Test 14: singleflight — K concurrent cold calls run one fetch sequence.
func TestEnsureIndexed_Singleflight(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 40
	svc, _, net := newSvc(t, pool, stub)

	const k = 8
	var wg sync.WaitGroup
	outs := make([]*IndexOutcome, k)
	errs := make([]error, k)
	for i := 0; i < k; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], errs[i] = svc.EnsureIndexed(
				context.Background(), net, addrRoot, EnsureOptions{})
		}(i)
	}
	wg.Wait()
	for i := range errs {
		require.NoError(t, errs[i])
		assert.Equal(t, StatusFresh, outs[i].IndexStatus)
	}
	assert.Equal(t, int64(1), stub.headCalls.Load())
}

// Test 15: request-global budget — an exhausted pool never even makes
// the head call; the outcome reports the persisted facts honestly.
func TestEnsureIndexed_BudgetZero(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 40
	svc, _, net := newSvc(t, pool, stub)

	b := NewBudget(1)
	require.True(t, b.tryTake(1)) // pool spent before this wallet runs
	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Budget: b})
	require.NoError(t, err)
	assert.True(t, out.BudgetExhausted)
	assert.Equal(t, StatusNotIndexed, out.IndexStatus)
	assert.Equal(t, int64(0), stub.headCalls.Load())
	// The pool is ours — never attributed as a provider verdict (m6).
	assert.Nil(t, out.ProviderStatus)
}

// Test 18: max_pages bound → partial + history_truncated persisted and
// reported on the later fresh-serve.
func TestEnsureIndexed_MaxPagesTruncated(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 1000
	pages := [][]provider.Transfer{}
	for i := 0; i < 10; i++ {
		pages = append(pages, []provider.Transfer{
			tf("txlist", provider.TransferKindNative,
				"0x"+string(rune('a'+i))+"00", addrA, addrRoot, "", "1", int64(100+i), false)})
	}
	stub.pages[addrRoot] = pages
	svc, q, net := newSvc(t, pool, stub)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusPartial, out.IndexStatus)
	assert.True(t, out.HistoryTruncated)
	assert.Equal(t, int64(5), stub.lookCalls.Load(), "max_pages=5 bound")

	w, _ := wallet.NewService(q).GetOrCreate(context.Background(), net.ID, addrRoot, addrRoot)
	st := getState(t, pool, net.ID, w.ID)
	assert.True(t, st.HistoryTruncated)
	assert.Equal(t, StatusPartial, st.Status)

	// Fresh-serve reports the persisted truncation honestly.
	out2, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.True(t, out2.HistoryTruncated)
	assert.Equal(t, int64(5), stub.lookCalls.Load())
}

// Test 18b: provider switch — the new adapter owns its OWN coverage row
// (post-rekey): it indexes from its own cold start, the old source's
// watermark left untouched.
func TestEnsureIndexed_ProviderSwitchReset(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stubA := newStub("chainscan")
	stubA.head = 200
	svc, _, net := newSvc(t, pool, stubA)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(200), *out.IndexedUntilBlock)

	stubB := newStub("nodereal")
	stubB.head = 500
	svc.adapters[net.Code] = []provider.TxProvider{stubB}
	out, err = svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	require.NotNil(t, out.IndexedUntilBlock)
	assert.Equal(t, int64(500), *out.IndexedUntilBlock)
	assert.Equal(t, int64(1), stubB.headCalls.Load())
}

// Head-lag: head < fetch_from → no transfer call (declared condition).
func TestEnsureIndexed_HeadLag(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 50
	svc, q, net := newSvc(t, pool, stub)

	_, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)

	// Second run at a lower head (< watermark−tail) → head call only.
	stub.head = 10
	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Refresh: true})
	require.NoError(t, err)
	require.NotNil(t, out.HeadBlock)
	assert.Equal(t, int64(10), *out.HeadBlock)
	assert.Equal(t, int64(2), stub.headCalls.Load())
	assert.Equal(t, int64(1), stub.lookCalls.Load())
	_ = q
}

// Tests: invalid rows counted, missing contract counted, is_error kept
// for listing but excluded from edges; self-row listed once.
func TestEnsureIndexed_RowHandling(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[addrRoot] = [][]provider.Transfer{{
		tf("txlist", provider.TransferKindNative, "0xcc1", addrA, addrRoot, "", "5", 90, false),
		// is_error transfer: listed, never an edge.
		tf("txlist", provider.TransferKindNative, "0xcc2", addrB, addrRoot, "", "9", 91, true),
		// missing contract → skipped missing_contract.
		tf("tokentx", provider.TransferKindToken, "0xcc3", addrA, addrRoot, "", "3", 92, false),
		// invalid address → skipped invalid_address.
		tf("txlist", provider.TransferKindNative, "0xcc4", "not-an-address", addrRoot, "", "3", 93, false),
		// W→W self-transfer → exactly one row, direction self.
		tf("from", provider.TransferKindNative, "0xcc5", addrRoot, addrRoot, "", "1", 94, false),
		tf("to", provider.TransferKindNative, "0xcc5", addrRoot, addrRoot, "", "1", 94, false),
	}}
	svc, q, net := newSvc(t, pool, stub)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	assert.Equal(t, int64(1), out.RowsSkipped.MissingContract)
	assert.Equal(t, int64(1), out.RowsSkipped.InvalidAddress)
	assert.Equal(t, 3, countRows(t, pool, net.ID)) // ok + is_error + self

	res, err := svc.ListTransactions(context.Background(), net, addrRoot, TxFilters{})
	require.NoError(t, err)
	var self, errRow int
	for _, it := range res.Items {
		if it.Direction == "self" {
			self++
		}
		if it.IsError {
			errRow++
		}
	}
	assert.Equal(t, 1, self, "W→W emitted exactly once as self")
	assert.Equal(t, 1, errRow, "is_error row retained on listing")

	// direction=in and direction=out both match the self-row.
	for _, d := range []string{"in", "out"} {
		r, err := svc.ListTransactions(context.Background(), net, addrRoot,
			TxFilters{Direction: d})
		require.NoError(t, err)
		var hasSelf bool
		for _, it := range r.Items {
			if it.Direction == "self" {
				hasSelf = true
			}
		}
		assert.True(t, hasSelf, "direction=%s must match self row", d)
	}

	// is_error rows produce no edges.
	w, _ := wallet.NewService(q).GetOrCreate(context.Background(), net.ID, addrRoot, addrRoot)
	edges, err := q.ListWalletEdges(context.Background(), db.ListWalletEdgesParams{
		NetworkID: net.ID, FromWalletID: pgtype.Int8{Int64: w.ID, Valid: true}, Limit: 100})
	require.NoError(t, err)
	for _, e := range edges {
		assert.NotEqual(t, "0xcc2", e.TxHash)
	}
}

// Test 11: cursor pagination is honest — no dupes across pages, bad
// cursor → ErrInvalidInput.
func TestListTransactions_Pagination(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	var page []provider.Transfer
	for i := 0; i < 5; i++ {
		page = append(page, tf("txlist", provider.TransferKindNative,
			"0xd"+string(rune('0'+i))+"0", addrA, addrRoot, "", "1", int64(50+i), false))
	}
	stub.pages[addrRoot] = [][]provider.Transfer{page}
	svc, _, net := newSvc(t, pool, stub)

	res, err := svc.ListTransactions(context.Background(), net, addrRoot,
		TxFilters{Limit: 2})
	require.NoError(t, err)
	require.Len(t, res.Items, 2)
	require.NotEmpty(t, res.NextCursor)
	assert.True(t, res.Items[0].BlockTimestamp.After(*res.Items[1].BlockTimestamp) ||
		res.Items[0].BlockTimestamp.Equal(*res.Items[1].BlockTimestamp))

	res2, err := svc.ListTransactions(context.Background(), net, addrRoot,
		TxFilters{Limit: 2, Cursor: res.NextCursor})
	require.NoError(t, err)
	require.Len(t, res2.Items, 2)
	seen := map[string]bool{}
	for _, it := range res.Items {
		seen[it.TxHash+it.TransferIndex] = true
	}
	for _, it := range res2.Items {
		assert.False(t, seen[it.TxHash+it.TransferIndex], "no duplicate across pages")
	}

	_, err = svc.ListTransactions(context.Background(), net, addrRoot,
		TxFilters{Cursor: "garbage!!!"})
	require.ErrorIs(t, err, ErrInvalidCursor)
}

// Identical-content duplicates within one tx get .{k} suffixes; two
// identical W→W copies → two rows.
func TestEnsureIndexed_DuplicateIdentity(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("nodereal")
	stub.head = 100
	dup := tf("from", provider.TransferKindNative, "0xee1", addrA, addrB, "", "42", 90, false)
	dup2 := dup
	dup2.Stream = "to"
	self := tf("from", provider.TransferKindNative, "0xee2", addrRoot, addrRoot, "", "7", 91, false)
	self2 := self
	self2.Stream = "to"
	stub.pages[addrRoot] = [][]provider.Transfer{{dup, dup, dup2, self, self2}}
	svc, _, net := newSvc(t, pool, stub)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	require.Equal(t, StatusFresh, out.IndexStatus)
	// dup group: max(per-stream counts) = max(2,1)=2 → two rows .1/.2.
	// self group: max(1,1)=1 → one row. Total 3.
	assert.Equal(t, 3, countRows(t, pool, net.ID))
}

// Review B1 regression: per-stream coverage. txlist finishes at block
// 95000 while tokentx is unfinished at block 400 — the watermark must
// stop at the UNFINISHED stream's own boundary, never the global max,
// and an unfinished stream never earns 'fresh'.
func TestEnsureIndexed_PerStreamWatermark(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100000
	stub.streams = []provider.StreamStatus{
		{Name: "txlist", Finished: true},
		{Name: "tokentx", Finished: false},
	}
	stub.pages[addrRoot] = [][]provider.Transfer{{
		tf("txlist", provider.TransferKindNative, "0xaa1", addrA, addrRoot, "", "1", 95000, false),
		tf("tokentx", provider.TransferKindToken, "0xbb1", addrB, addrRoot, tokenC, "2", 400, false),
	}}
	svc, q, net := newSvc(t, pool, stub)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	require.Equal(t, StatusPartial, out.IndexStatus)
	require.NotNil(t, out.IndexedUntilBlock)
	assert.Equal(t, int64(399), *out.IndexedUntilBlock,
		"watermark = unfinished stream's own max-1, not global max-1")
	st := getState(t, pool, net.ID, out.WalletID)
	assert.True(t, st.HistoryTruncated)
	_ = q
}

// Review M3 regression: a live 'indexing' claim within StaleAfter is
// served (no duplicate fetch); past the horizon it is retried.
func TestEnsureIndexed_StaleClaimHorizon(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[addrRoot] = [][]provider.Transfer{{
		tf("txlist", provider.TransferKindNative, "0xcc1", addrA, addrRoot, "", "5", 90, false),
	}}
	svc, q, net := newSvc(t, pool, stub)
	ctx := context.Background()

	out0, err := svc.EnsureIndexed(ctx, net, addrRoot, EnsureOptions{})
	require.NoError(t, err)

	// Simulate a live claim from another process: indexing + fresh updated_at.
	_, err = pool.Exec(ctx, `UPDATE wallet_index_state SET status='indexing',
		updated_at = NOW() WHERE network_id=$1 AND wallet_id=$2`,
		net.ID, out0.WalletID)
	require.NoError(t, err)
	calls := stub.lookCalls.Load()
	out, err := svc.EnsureIndexed(ctx, net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, calls, stub.lookCalls.Load(),
		"live claim within horizon — no duplicate fetch")
	assert.Equal(t, StatusIndexing, out.IndexStatus)

	// Past the horizon the dead claim is retried.
	_, err = pool.Exec(ctx, `UPDATE wallet_index_state SET status='indexing',
		updated_at = NOW() - interval '1 hour' WHERE network_id=$1 AND wallet_id=$2`,
		net.ID, out0.WalletID)
	require.NoError(t, err)
	out, err = svc.EnsureIndexed(ctx, net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Greater(t, stub.lookCalls.Load(), calls,
		"stale claim past horizon — retried")
	_ = q
}

// Review m10 regression: a claim clears a persisted error so concurrent
// readers never see 'indexing' with a stale ErrKind.
func TestMarkWalletIndexing_ClearsError(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	q := db.New(pool)
	net := testNet(t, pool)
	var walletID int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO wallets (network_id, address, normalized_address, address_type)
		 VALUES ($1, $2, $2, 'evm') RETURNING id`,
		net.ID, addrRoot).Scan(&walletID))
	src, err := q.GetSourceByCode(context.Background(), "chainscan")
	require.NoError(t, err)
	_, err = q.UpsertWalletIndexState(context.Background(),
		db.UpsertWalletIndexStateParams{
			NetworkID: net.ID, WalletID: walletID, SourceID: src.ID,
			IndexedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
			Status:    StatusFailed,
			Error:     pgtype.Text{String: "timeout", Valid: true},
		})
	require.NoError(t, err)
	require.NoError(t, q.MarkWalletIndexing(context.Background(),
		db.MarkWalletIndexingParams{NetworkID: net.ID, WalletID: walletID,
			SourceID: src.ID}))
	st := getState(t, pool, net.ID, walletID)
	assert.Equal(t, StatusIndexing, st.Status)
	assert.False(t, st.Error.Valid, "claim clears stale error")
}

// Review r3-O1 regression: a budget stop before page 1 (head spent the
// last request) must NOT claim the window — coverage stays at the
// prior watermark, never fetchTo.
func TestEnsureIndexed_ZeroPageBudgetStop(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 1000
	svc, _, net := newSvc(t, pool, stub)

	// Pool of exactly 1: the head call consumes it; page 1 can never run.
	b := NewBudget(1)
	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Budget: b})
	require.NoError(t, err)
	require.Equal(t, StatusPartial, out.IndexStatus,
		"head fetched but window unproven — partial, not fresh")
	if out.IndexedUntilBlock != nil {
		assert.Less(t, *out.IndexedUntilBlock, stub.head,
			"watermark must not leap to fetchTo on a zero-page stop")
	}
	assert.True(t, out.HistoryTruncated)
	assert.Equal(t, 0, countRows(t, pool, net.ID),
		"no rows fetched on a zero-page stop")
	assert.Equal(t, int64(1), stub.headCalls.Load())
	assert.Equal(t, int64(0), stub.lookCalls.Load())
}

// Phase-7 §6-16: MaxPagesFor(frontierIdx) — 0/root → Hop0, 2 → HopN,
// ≥3 → the shared Hop3 tier. The deeper tier applies through
// EnsureIndexed call counts (paging stub, pages > cap).
func TestMaxPagesFor_Tiers(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	svc, _, _ := newSvc(t, pool, stub)
	assert.Equal(t, 5, svc.MaxPagesFor(0), "root → MaxPagesHop0")
	assert.Equal(t, 2, svc.MaxPagesFor(2), "frontier-2 → MaxPagesHopN")
	assert.Equal(t, 1, svc.MaxPagesFor(3), "frontier-3 → MaxPagesHop3")
	assert.Equal(t, 1, svc.MaxPagesFor(5), "frontier-5 shares Hop3")
}

func TestEnsureIndexed_Hop3TierPages(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[addrA] = [][]provider.Transfer{
		{tf("txlist", provider.TransferKindNative, "0zt1", addrB, addrA, "", "1", 90, false)},
		{}, {}, // 3 pages — Hop3 caps at 1 call
	}
	svc, _, net := newSvc(t, pool, stub)

	out, err := svc.EnsureIndexed(context.Background(), net, addrA,
		EnsureOptions{MaxPages: svc.MaxPagesFor(3)})
	require.NoError(t, err)
	assert.Equal(t, StatusPartial, out.IndexStatus,
		"capped mid-pagination → partial, honestly")
	stub.mu.Lock()
	calls := stub.idx[addrA]
	stub.mu.Unlock()
	assert.Equal(t, 1, calls,
		"frontier≥3 uses the shared Hop3 tier (default 1)")
}
