package tracing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/testutil"
	"checkwallet/internal/transaction"
	"checkwallet/internal/wallet"
)

const testDSN = "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_tracing_test?sslmode=disable"

var (
	root = "0x0000000000000000000000000000000000000001"
	cpA  = "0x00000000000000000000000000000000000000a2"
	cpB  = "0x00000000000000000000000000000000000000b2"
	cpC  = "0x00000000000000000000000000000000000000c2"
	cpD  = "0x00000000000000000000000000000000000000d2"
	cpE  = "0x00000000000000000000000000000000000000e2"
	dead = "0x000000000000000000000000000000000000dead"
	ts0  = time.Unix(1700000000, 0).UTC()
)

// itemByCp indexes items by counterparty for set assertions.
func itemByCp(items []Item) map[string]Item {
	m := map[string]Item{}
	for _, it := range items {
		m[it.Counterparty] = it
	}
	return m
}

type stubTx struct {
	name    string
	enabled bool
	head    int64

	headCalls atomic.Int64
	lookCalls atomic.Int64

	mu    sync.Mutex
	pages map[string][]provider.Transfer
	seq   map[string][][]provider.Transfer // per-addr sequential pages (tier tests)
	sidx  map[string]int
	calls map[string]int // per-addr LookupTransfers count
	errs  map[string]error
	hook  func(addr string) // runs inside LookupTransfers — fault injection
}

func newStub(name string) *stubTx {
	return &stubTx{name: name, enabled: true,
		pages: map[string][]provider.Transfer{},
		seq:   map[string][][]provider.Transfer{},
		sidx:  map[string]int{},
		calls: map[string]int{},
		errs:  map[string]error{}}
}
func (s *stubTx) Name() string                 { return s.name }
func (s *stubTx) Enabled(network.Network) bool { return s.enabled }
func (s *stubTx) HeadBlock(context.Context, network.Network) (int64, error) {
	s.headCalls.Add(1)
	return s.head, nil
}
func (s *stubTx) LookupTransfers(_ context.Context, _ network.Network,
	addr string, _ provider.TransferLookupOpts) (*provider.TransferPage, error) {
	s.lookCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[addr]++
	if s.hook != nil {
		s.hook(addr)
	}
	if err := s.errs[addr]; err != nil {
		return nil, err
	}
	if seq := s.seq[addr]; len(seq) > 0 {
		i := s.sidx[addr]
		var tr []provider.Transfer
		if i < len(seq) {
			tr = seq[i]
		}
		s.sidx[addr] = i + 1
		next := ""
		if i+1 < len(seq) {
			next = "more"
		}
		return &provider.TransferPage{Transfers: tr, NextPageToken: next,
			UpstreamCalls: 1}, nil
	}
	return &provider.TransferPage{Transfers: s.pages[addr], UpstreamCalls: 1}, nil
}
func (s *stubTx) called(addr string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[addr]
}

func tf(stream, hash, from, to, amount string, blk int64) provider.Transfer {
	return provider.Transfer{
		TxHash: hash, Stream: stream, Type: provider.TransferKindNative,
		From: from, To: to, AmountRaw: amount, BlockNumber: blk,
		BlockTimestamp: ts0.Add(time.Duration(blk) * time.Second),
		Confirmations:  5,
	}
}

func testNet(t *testing.T, pool *pgxpool.Pool) network.Network {
	t.Helper()
	return netByCode(t, pool, "ethereum")
}

func netByCode(t *testing.T, pool *pgxpool.Pool, code string) network.Network {
	t.Helper()
	var net network.Network
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT id, code, name, chain_reference, namespace FROM blockchain_networks
		 WHERE code=$1`, code).Scan(
		&net.ID, &net.Code, &net.Name, &net.ChainReference, &net.Namespace))
	return net
}

func newStack(t *testing.T, pool *pgxpool.Pool, stub provider.TxProvider,
	cfg Config) (*Service, *db.Queries, network.Network) {
	t.Helper()
	return newStackNets(t, pool, stub, cfg,
		[]network.Network{testNet(t, pool)})
}

// newStackNets is newStack with an explicit resolver network set — the
// cross-chain tests need ethereum + bsc in the resolver while the BFS
// stays ethereum-scoped.
func newStackNets(t *testing.T, pool *pgxpool.Pool, stub provider.TxProvider,
	cfg Config, nets []network.Network) (*Service, *db.Queries, network.Network) {
	t.Helper()
	q := db.New(pool)
	net := testNet(t, pool)
	txSvc := transaction.New(pool, q,
		map[string]provider.TxProvider{net.Code: stub},
		wallet.NewService(q), transaction.Config{
			FreshTTL: time.Minute, NegativeTTL: time.Minute,
			StaleAfter: 15 * time.Minute, PageSize: 100,
			MaxPagesHop0: 5, MaxPagesHopN: 2,
			Budget: map[string]int{stub.Name(): 64},
		}, nil)
	rdb := testutil.RequireTestRedis(t)
	resolver := network.NewResolver(nets)
	sanSvc := sanctions.NewService(q, rdb, resolver, 48*time.Hour)
	return New(q, txSvc, sanSvc, resolver, cfg), q, net
}

func seedLabel(t *testing.T, pool *pgxpool.Pool, netID int64, addr, cat string) {
	t.Helper()
	seedLabelV(t, pool, netID, addr, cat, "reported", "high", "seeded", "")
}

// seedLabelV seeds a label with explicit verification/confidence — the
// exposure-v1 arithmetic tests need the full factor space. address
// (original representation) may differ from the normalized key — the
// foreign-label test relies on listed_address ≠ normalized_address.
func seedLabelV(t *testing.T, pool *pgxpool.Pool, netID int64, addr, cat,
	verification, confidence, label, origAddr string) {
	t.Helper()
	if origAddr == "" {
		origAddr = addr
	}
	var walletID int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO wallets (network_id, address, normalized_address, address_type)
		VALUES ($1, $2, $3, 'evm')
		ON CONFLICT (network_id, normalized_address)
		DO UPDATE SET address = EXCLUDED.address
		RETURNING id`, netID, origAddr, addr).Scan(&walletID))
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wallet_labels (wallet_id, category_id, source_id, fingerprint,
			label, confidence, verification_status)
		VALUES ($1, (SELECT id FROM risk_categories WHERE code=$2),
			(SELECT id FROM data_sources WHERE code='wallet_attribution'),
			$3, $4, $5, $6)`,
		walletID, cat, "fp-"+addr+cat+verification+label,
		label, confidence, verification)
	require.NoError(t, err)
}

// Test 4: 1-hop exposure on a labeled counterparty → direct_counterparty
// + evidence edge carrying the tx.
func TestExposures_Hop1Labeled(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0x1aa", cpA, root, "500", 90),
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpA, "SCAM")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	it := res.Items[0]
	assert.Equal(t, cpA, it.Counterparty)
	assert.Equal(t, "SCAM", it.Category)
	assert.Equal(t, RelDirect, it.Relationship)
	assert.Equal(t, 1, it.Hops)
	require.Len(t, it.Paths, 1)
	require.Len(t, it.Paths[0].Edges, 1)
	assert.Equal(t, "0x1aa", it.Paths[0].Edges[0].TxHash)
	assert.False(t, it.Paths[0].PathVerified)
	assert.Equal(t, "bounded_bfs", res.TracingMethod)
	assert.Nil(t, res.Truncated)
}

// Test 5: 2-hop → indirect_hop, path of 2 edges, temporal_order_valid.
func TestExposures_Hop2(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 200
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0x2a1", cpA, root, "100", 100),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0x2a2", cpB, cpA, "60", 110),
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpB, "HACK")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.NoError(t, err)
	var indirect *Item
	for i := range res.Items {
		if res.Items[i].Hops == 2 {
			indirect = &res.Items[i]
		}
	}
	require.NotNil(t, indirect, "cpB must emit a hop-2 item")
	assert.Equal(t, cpB, indirect.Counterparty)
	assert.Equal(t, RelIndirect, indirect.Relationship)
	assert.Equal(t, "HACK", indirect.Category)
	require.Len(t, indirect.Paths, 1)
	require.Len(t, indirect.Paths[0].Edges, 2)
	assert.True(t, indirect.Paths[0].TemporalOrderValid)
	assert.Equal(t, "0x2a1", indirect.Paths[0].Edges[0].TxHash)
	assert.Equal(t, "0x2a2", indirect.Paths[0].Edges[1].TxHash)
	assert.Equal(t, 1, res.Coverage.NodesExamined)
	assert.Equal(t, 1, res.Coverage.PositionsEmitted)
}

// Test 20: self-edge + root-returning edge excluded; sentinel emits an
// item but is never expanded (counterparties_total excludes it).
func TestExposures_EdgeRules(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0x3a1", root, root, "5", 90), // self-edge
		tf("txlist", "0x3a2", dead, root, "5", 91), // sentinel in
		tf("txlist", "0x3a3", cpA, root, "5", 92),  // real cp
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0x3a4", cpB, cpA, "5", 95),  // hop-2 candidate
		tf("txlist", "0x3a5", cpA, root, "5", 96), // root-returning
	}
	seedLabel(t, pool, testNet(t, pool).ID, dead, "SCAM")
	seedLabel(t, pool, testNet(t, pool).ID, cpB, "MIXER")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.NoError(t, err)

	assert.Equal(t, 1, res.Coverage.CounterpartiesTotal,
		"sentinel is not an expansion position")
	cats := map[string]string{}
	for _, it := range res.Items {
		cats[it.Counterparty] = it.Category
	}
	assert.Equal(t, "SCAM", cats[dead], "sentinel still emits its matched item")
	assert.Equal(t, "MIXER", cats[cpB], "hop-2 via cpA emits")
	// Root-returning edge produced no item for root itself.
	_, selfListed := cats[root]
	assert.False(t, selfListed)
}

// Test 6/15: bound truncations — max_nodes and max_provider_requests each
// name their reason.
func TestExposures_Bounds(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)

	t.Run("max_nodes", func(t *testing.T) {
		testutil.TruncateAll(t, pool)
		stub := newStub("chainscan")
		stub.head = 100
		stub.pages[root] = []provider.Transfer{
			tf("txlist", "0x4a1", cpA, root, "1", 90),
			tf("txlist", "0x4a2", cpB, root, "1", 91),
			tf("txlist", "0x4a3", cpC, root, "1", 92),
		}
		svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 1, MaxEdges: 100})
		res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
		require.NoError(t, err)
		require.NotNil(t, res.Truncated)
		assert.Equal(t, TruncMaxNodes, *res.Truncated)
		assert.Equal(t, 1, res.Coverage.NodesExamined)
		assert.Equal(t, 2, res.Coverage.NodesRemaining)
	})

	t.Run("max_provider_requests", func(t *testing.T) {
		testutil.TruncateAll(t, pool)
		stub := newStub("chainscan")
		stub.head = 100
		stub.pages[root] = []provider.Transfer{
			tf("txlist", "0x5a1", cpA, root, "1", 90),
			tf("txlist", "0x5a2", cpB, root, "1", 91),
		}
		svc, q, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
		// Pool of 2: head + one node index — the second node never starts.
		svc.tx = transaction.New(pool, q,
			map[string]provider.TxProvider{net.Code: stub},
			wallet.NewService(q), transaction.Config{
				FreshTTL: time.Minute, NegativeTTL: time.Minute,
				StaleAfter: 15 * time.Minute, PageSize: 100,
				Budget: map[string]int{stub.Name(): 2},
			}, nil)
		res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
		require.NoError(t, err)
		require.NotNil(t, res.Truncated)
		assert.Equal(t, TruncMaxReqs, *res.Truncated)
	})

	t.Run("max_hops_1_no_expansion", func(t *testing.T) {
		testutil.TruncateAll(t, pool)
		stub := newStub("chainscan")
		stub.head = 100
		stub.pages[root] = []provider.Transfer{
			tf("txlist", "0x6a1", cpA, root, "1", 90),
		}
		svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
		res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 1})
		require.NoError(t, err)
		assert.Equal(t, 0, res.Coverage.NodesExamined)
		assert.Equal(t, 0, res.Coverage.PositionsEmitted)
		assert.Equal(t, 1, res.Coverage.NodesRemaining)
	})
}

// Test 21b: max_hops validation — 0 and >5 rejected (phase 7: 1..5).
func TestValidateRequest(t *testing.T) {
	for _, hops := range []int{0, 6, -1} {
		r := Request{MaxHops: hops}
		require.Error(t, ValidateRequest(&r))
	}
	for _, hops := range []int{1, 2, 3, 4, 5} {
		r := Request{MaxHops: hops}
		require.NoError(t, ValidateRequest(&r))
	}
	r := Request{MaxHops: 1, Direction: "self"}
	require.Error(t, ValidateRequest(&r))
	r = Request{MaxHops: 1, Limit: 101}
	require.Error(t, ValidateRequest(&r))
}

// Tests 10/19: same-network sanctioned counterparty → SANCTIONED over a
// label match (precedence); a cross-chain-only listing never emits
// SANCTIONED.
func TestExposures_SanctionsPrecedence(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	netID := testNet(t, pool).ID

	// Published OFAC snapshot + a CHAIN_SPECIFIC listing for cpA on this
	// network, and a cross-chain listing (network NULL → unresolved).
	_, err := pool.Exec(context.Background(), `
		INSERT INTO sanctions_snapshots (source_id, version, checksum,
			entity_count, wallet_count, program_count, published_at)
		VALUES ((SELECT id FROM data_sources WHERE code='ofac_sdn'),
			'snap-tr', 'c', 1, 2, 0, NOW())`)
	require.NoError(t, err)
	var entityID int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO sanctions_entities (source_id, external_entity_id, primary_name)
		VALUES ((SELECT id FROM data_sources WHERE code='ofac_sdn'),
			'OFAC-TR-1', 'Trace Entity') RETURNING id`).Scan(&entityID))
	_, err = pool.Exec(context.Background(), `
		INSERT INTO sanctions_wallets (sanctions_entity_id, listed_address,
			normalized_address, network_id, identifier_type, match_scope)
		VALUES ($1, $2, $2, $3, 'ETH', 'CHAIN_SPECIFIC')`,
		entityID, cpA, netID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO sanctions_wallets (sanctions_entity_id, listed_address,
			normalized_address, network_id, identifier_type, match_scope)
		VALUES ($1, $2, $2, NULL, 'ETH', 'ADDRESS_ONLY')`,
		entityID, cpB)
	require.NoError(t, err)
	seedLabel(t, pool, netID, cpA, "SCAM") // label must lose to SANCTIONED

	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0x8a1", cpA, root, "1", 90),
		tf("txlist", "0x8a2", cpB, root, "1", 91),
	}
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 1})
	require.NoError(t, err)

	cats := map[string]Item{}
	for _, it := range res.Items {
		cats[it.Counterparty] = it
	}
	require.Contains(t, cats, cpA)
	assert.Equal(t, CatSanctioned, cats[cpA].Category,
		"sanctions_direct outranks the label")
	var sawDirect, sawLabel bool
	for _, sig := range cats[cpA].MatchedSignals {
		if sig.Kind == SigSanctionsDirect {
			sawDirect = true
		}
		if sig.Kind == SigLabel {
			sawLabel = true
		}
	}
	assert.True(t, sawDirect && sawLabel, "all signals carried")

	require.Contains(t, cats, cpB)
	assert.Equal(t, CatCrossChain, cats[cpB].Category,
		"unresolved-network listing ⇒ cross_chain_listing, never SANCTIONED")
}

// Test 22: deterministic order — hops asc then position index then
// counterparty; identical inputs give identical order.
func TestExposures_Deterministic(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0x7a1", cpC, root, "1", 90),
		tf("txlist", "0x7a2", cpA, root, "1", 91),
		tf("txlist", "0x7a3", cpB, root, "1", 92),
	}
	for _, cp := range []string{cpA, cpB, cpC} {
		seedLabel(t, pool, testNet(t, pool).ID, cp, "SCAM")
	}
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res1, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.NoError(t, err)
	res2, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.NoError(t, err)
	require.Equal(t, len(res1.Items), len(res2.Items))
	for i := range res1.Items {
		assert.Equal(t, res1.Items[i].Counterparty, res2.Items[i].Counterparty)
	}
	// Position order = first-edge order: cpC(rank0), cpA(rank1), cpB(rank2).
	assert.Equal(t, cpC, res1.Items[0].Counterparty)
	assert.Equal(t, cpA, res1.Items[1].Counterparty)
	assert.Equal(t, cpB, res1.Items[2].Counterparty)
}

// Review M1 regression: root edge scan clipped by max_edges must emit
// truncated=max_edges — the clip is reported, never dropped.
func TestExposures_EdgeClipReported(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	for i := 0; i < 5; i++ {
		addr := fmt.Sprintf("0x0000000000000000000000000000000000000e%02d", i)
		stub.pages[root] = append(stub.pages[root],
			tf("txlist", fmt.Sprintf("0xe%d", i), addr, root, "1", int64(90+i)))
		seedLabel(t, pool, testNet(t, pool).ID, addr, "SCAM")
	}
	svc, _, net := newStack(t, pool, stub,
		Config{MaxNodes: 10, MaxEdges: 3})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.NoError(t, err)
	require.NotNil(t, res.Truncated)
	assert.Equal(t, TruncMaxEdges, *res.Truncated,
		"clipped root scan must be reported")
	assert.LessOrEqual(t, res.Coverage.EdgesExamined, int64(3))
}

// Review M2 regression: a non-context failure inside hop-2 expansion is
// an error, never a fake 'deadline' truncation. The pool closes inside
// the hop-2 node's LookupTransfers — root already indexed, so the fault
// lands exactly on the M2 branch (EnsureIndexed error → propagate).
func TestExposures_InfraErrorPropagates(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xf1a", cpA, root, "500", 90),
	}
	var once sync.Once
	stub.hook = func(addr string) {
		if addr == cpA {
			once.Do(func() { pool.Close() })
		}
	}
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.Error(t, err, "hop-2 infra failure must surface as error")
	assert.Nil(t, res)
}

// Review M4 regression: N edges via the same intermediary → ONE path
// (node sequences, not edges); the picked e2 follows the pinned rule
// (earliest ts → smallest amount → tx_hash/transfer_index).
func TestExposures_Hop2DedupBySequence(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xd1a", cpA, root, "500", 90),
	}
	// Two via→cp2 edges, same ts — pick smallest amount.
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xd2a", cpA, cpB, "900", 95),
		tf("txlist", "0xd2b", cpA, cpB, "100", 95),
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpB, "SCAM")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.NoError(t, err)
	var it *Item
	for i := range res.Items {
		if res.Items[i].Counterparty == cpB {
			it = &res.Items[i]
		}
	}
	require.NotNil(t, it, "hop-2 item for cpB")
	assert.Equal(t, RelIndirect, it.Relationship)
	require.Len(t, it.Paths, 1, "same-via edges collapse to one sequence")
	require.Len(t, it.Paths[0].Edges, 2)
	assert.Equal(t, "0xd2b", it.Paths[0].Edges[1].TxHash,
		"pickEdge selects smallest amount at equal ts")
}

// ==================================================================
// Phase 7 (plan 20261104-0400 §6) — advanced tracing pinned tests.
// ==================================================================

// §6-1/9/31: 3-hop item — hops=3, indirect_hop, 3-edge path; a
// non-monotone MIDDLE edge flips temporal_order_valid; coverage
// vocabulary pinned (Σ frontiers, per-hop maps, max_depth_reached,
// node_indexed_until spanning depths).
func TestExposures_Hop3(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 200
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xa1", cpA, root, "100", 90),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xa2", cpB, cpA, "60", 110),
	}
	stub.pages[cpB] = []provider.Transfer{
		// e3 ts=105 < e2 ts=110 → adjacent-pair violation → false.
		tf("txlist", "0xa3", cpC, cpB, "30", 105),
	}
	seedLabelV(t, pool, testNet(t, pool).ID, cpC, "HACK", "verified", "high", "c", "")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)

	items := itemByCp(res.Items)
	c, ok := items[cpC]
	require.True(t, ok, "cpC must emit a hop-3 item")
	assert.Equal(t, 3, c.Hops)
	assert.Equal(t, RelIndirect, c.Relationship)
	assert.Equal(t, "HACK", c.Category)
	require.Len(t, c.Paths, 1)
	require.Len(t, c.Paths[0].Edges, 3)
	assert.Equal(t, 3, c.Paths[0].HopCount)
	assert.Equal(t, "0xa1", c.Paths[0].Edges[0].TxHash)
	assert.Equal(t, "0xa2", c.Paths[0].Edges[1].TxHash)
	assert.Equal(t, "0xa3", c.Paths[0].Edges[2].TxHash)
	assert.False(t, c.Paths[0].TemporalOrderValid,
		"non-monotone middle edge → false")
	// verified/high MALICIOUS at hop-3: 70×1.0×1.0×0.25 = 17.5 → 18.
	require.NotNil(t, c.ExposureScore)
	assert.Equal(t, 18, *c.ExposureScore)
	assert.Equal(t, "exposure-v1", res.ExposurePolicyVersion)

	// Coverage vocabulary (§6-9/31): maps keyed {2..3}, Σ invariants.
	cov := res.Coverage
	assert.Equal(t, map[int]int{2: 1, 3: 1}, cov.FrontierByHop)
	assert.Equal(t, map[int]int{2: 1, 3: 1}, cov.NodesExaminedByHop)
	assert.Equal(t, map[int]int{2: 1, 3: 1}, cov.FrontierEmittedByHop)
	assert.Equal(t, map[int]int{2: 0, 3: 0}, cov.NodesRemainingByHop)
	assert.Equal(t, 3, cov.MaxDepthReached)
	assert.Equal(t, 2, cov.CounterpartiesTotal)
	assert.Equal(t, 2, cov.NodesExamined)
	assert.Equal(t, 2, cov.PositionsEmitted)
	assert.Equal(t, cov.CounterpartiesTotal,
		cov.NodesExamined+cov.NodesRemaining)
	assert.Contains(t, cov.NodeIndexedUntil, cpA)
	assert.Contains(t, cov.NodeIndexedUntil, cpB)
}

// §6-3: a frontier SCHEDULED for expansion that exceeds its per-depth
// cap → truncated=max_nodes; per-hop coverage consistent.
func TestExposures_DepthCapMaxNodes(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xc1", cpA, root, "1", 90),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xc2", cpB, cpA, "1", 95),
		tf("txlist", "0xc3", cpC, cpA, "1", 96),
		tf("txlist", "0xc4", cpD, cpA, "1", 97),
		tf("txlist", "0xc5", cpE, cpA, "1", 98),
	}
	svc, _, net := newStack(t, pool, stub, Config{
		MaxNodesPerDepth: [4]int{10, 2, 12, 6}, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	require.NotNil(t, res.Truncated)
	assert.Equal(t, TruncMaxNodes, *res.Truncated,
		"|frontier_3|=4 > MaxNodes_3=2 → max_nodes")
	assert.Equal(t, 4, res.Coverage.FrontierByHop[3])
	assert.Equal(t, 2, res.Coverage.NodesExaminedByHop[3])
	assert.Equal(t, 2, res.Coverage.NodesRemainingByHop[3])
	assert.Equal(t, 1, res.Coverage.FrontierByHop[2])
	assert.Equal(t, 5, res.Coverage.CounterpartiesTotal)
}

// §6-4: min-depth dedup — a node reachable at hop-2 AND hop-3 emits at
// hop-2 only (its deeper candidacy is suppressed by dedup).
func TestExposures_MinDepthDedup(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xd1", cpA, root, "1", 90),
	}
	// cpX enters cpAgg_3 via cpA; cpY joins frontier_3 too — and cpY's
	// scan points back to cpX, which dedup must already cover.
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xd2", cpB, cpA, "1", 95), // cpB = the hop-2 target
		tf("txlist", "0xd3", cpC, cpA, "1", 96), // cpC = second frontier-3 pos
	}
	stub.pages[cpC] = []provider.Transfer{
		tf("txlist", "0xd4", cpB, cpC, "1", 100), // deeper path back to cpB
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpB, "SCAM")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	var got []Item
	for _, it := range res.Items {
		if it.Counterparty == cpB {
			got = append(got, it)
		}
	}
	require.Len(t, got, 1, "cpB emits exactly once")
	assert.Equal(t, 2, got[0].Hops, "min-depth: hop-2 wins over hop-3")
}

// §6-5: ancestor cycle — A→B→…→A edges never materialize a position
// for A at deeper levels (dedup-set members are excluded from
// candidacy): no extra provider call, no deeper item.
func TestExposures_CycleExclusion(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xe1", cpA, root, "1", 90),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xe2", cpB, cpA, "1", 95),
		tf("txlist", "0xe6", cpA, root, "1", 96), // root-returning — dropped
	}
	stub.pages[cpB] = []provider.Transfer{
		tf("txlist", "0xe3", cpA, cpB, "1", 100), // cycle back to A (dedup'd)
		tf("txlist", "0xe4", cpC, cpB, "1", 101), // real hop-3 target
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpA, "SCAM")
	seedLabel(t, pool, testNet(t, pool).ID, cpC, "HACK")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)

	assert.Equal(t, 1, stub.called(cpA),
		"A is indexed exactly once — the cycle edge never repositions it")
	assert.Equal(t, map[int]int{2: 1, 3: 1}, res.Coverage.FrontierByHop)
	items := itemByCp(res.Items)
	require.Contains(t, items, cpA)
	assert.Equal(t, 1, items[cpA].Hops, "A emits at hop-1 only")
	require.Contains(t, items, cpC)
	assert.Equal(t, 3, items[cpC].Hops)
}

// §6-8: direction=out at max_hops=3 — hop-1 edge set filtered, deep
// evidence edges unfiltered (inbound e2 carried as evidence).
func TestExposures_DirectionScope(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xf1", cpB, root, "1", 90), // inbound — filtered out
		tf("txlist", "0xf2", root, cpA, "1", 91), // outbound — kept
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xf3", cpC, cpA, "1", 95), // inbound deep edge — fact
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpB, "SCAM")
	seedLabel(t, pool, testNet(t, pool).ID, cpC, "HACK")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root,
		Request{MaxHops: 3, Direction: "out"})
	require.NoError(t, err)
	items := itemByCp(res.Items)
	_, filtered := items[cpB]
	assert.False(t, filtered, "in-only hop-1 cp filtered under direction=out")
	require.Contains(t, items, cpC)
	require.Len(t, items[cpC].Paths, 1)
	e2 := items[cpC].Paths[0].Edges[1]
	assert.Equal(t, cpC, e2.FromAddress)
	assert.Equal(t, cpA, e2.ToAddress,
		"inbound deep edge carried as evidence despite direction=out")
}

// §6-12/13/14: exposure-v1 arithmetic end-to-end — sanctioned 2-hop vs
// label 1-hop vs cross-chain listing; SANCTIONED label vs
// sanctions_direct; disputed contributes 0; all-zero → null.
func TestExposures_ExposureScore(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	netID := testNet(t, pool).ID

	// OFAC snapshot + CHAIN_SPECIFIC listing for cpB (sanctions_direct)
	// and an ADDRESS_ONLY (unresolved-network) listing for cpC.
	_, err := pool.Exec(context.Background(), `
		INSERT INTO sanctions_snapshots (source_id, version, checksum,
			entity_count, wallet_count, program_count, published_at)
		VALUES ((SELECT id FROM data_sources WHERE code='ofac_sdn'),
			'snap-exp', 'c', 1, 2, 0, NOW())`)
	require.NoError(t, err)
	var entityID int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO sanctions_entities (source_id, external_entity_id, primary_name)
		VALUES ((SELECT id FROM data_sources WHERE code='ofac_sdn'),
			'OFAC-EXP-1', 'Exposure Entity') RETURNING id`).Scan(&entityID))
	_, err = pool.Exec(context.Background(), `
		INSERT INTO sanctions_wallets (sanctions_entity_id, listed_address,
			normalized_address, network_id, identifier_type, match_scope)
		VALUES ($1, $2, $2, $3, 'ETH', 'CHAIN_SPECIFIC')`, entityID, cpB, netID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO sanctions_wallets (sanctions_entity_id, listed_address,
			normalized_address, network_id, identifier_type, match_scope)
		VALUES ($1, $2, $2, NULL, 'ETH', 'ADDRESS_ONLY')`, entityID, cpC)
	require.NoError(t, err)

	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xb1", cpA, root, "1", 90),
		tf("txlist", "0xb3", cpC, root, "1", 92),
		tf("txlist", "0xb4", cpD, root, "1", 93),
		tf("txlist", "0xb5", cpE, root, "1", 94),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xb2", cpB, cpA, "1", 95),
	}
	// cpA: verified/high SCAM at hop-1 → 70.
	seedLabelV(t, pool, netID, cpA, "SCAM", "verified", "high", "a", "")
	// cpD: verified SANCTIONED label, unknown confidence → 85×1.0×0.7 =
	// 59.5 → 60 (per-term half-up) — distinct from sanctions_direct=100.
	seedLabelV(t, pool, netID, cpD, "SANCTIONED", "verified", "unknown", "d", "")
	// cpE: disputed SCAM (0) + verified/high HACK (70) → 70 — disputed
	// contributes exactly 0 to the Σ.
	seedLabelV(t, pool, netID, cpE, "SCAM", "disputed", "high", "e1", "")
	seedLabelV(t, pool, netID, cpE, "HACK", "verified", "high", "e2", "")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.NoError(t, err)
	items := itemByCp(res.Items)

	require.NotNil(t, items[cpA].ExposureScore)
	assert.Equal(t, 70, *items[cpA].ExposureScore, "label 1-hop: 70×1.0")
	require.NotNil(t, items[cpB].ExposureScore)
	assert.Equal(t, 50, *items[cpB].ExposureScore,
		"sanctions_direct 2-hop: 100×0.5")
	require.NotNil(t, items[cpC].ExposureScore)
	assert.Equal(t, 20, *items[cpC].ExposureScore,
		"unresolved-network listing 1-hop: 20×1.0")
	require.NotNil(t, items[cpD].ExposureScore)
	assert.Equal(t, 60, *items[cpD].ExposureScore,
		"label SANCTIONED: 85×1.0×0.7=59.5→60 (≠100 direct)")
	require.NotNil(t, items[cpE].ExposureScore)
	assert.Equal(t, 70, *items[cpE].ExposureScore, "disputed adds 0")
	// Kind discrimination: cpD carries a label signal, never a
	// sanctions_direct one.
	for _, sig := range items[cpD].MatchedSignals {
		assert.NotEqual(t, SigSanctionsDirect, sig.Kind)
	}
}

// §6-14/15: all-zero-weight signals → exposure_score null on the wire
// (never 0); envelope always carries exposure_policy_version and never
// any risk_score field.
func TestExposures_ExposureScoreNull(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xn1", cpA, root, "1", 90),
	}
	// EXCHANGE = ENTITY_TYPE → base 0; ENTITY → 0. Item still emits.
	seedLabelV(t, pool, testNet(t, pool).ID, cpA, "EXCHANGE", "verified", "high", "x", "")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 1})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	assert.Nil(t, res.Items[0].ExposureScore,
		"informational-only signals → null, never 0")

	raw, err := json.Marshal(res)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"exposure_score":null`)
	assert.Contains(t, string(raw), `"exposure_policy_version":"exposure-v1"`)
	assert.NotContains(t, string(raw), `"risk_score"`,
		"exposure score never leaks into the risk-score vocabulary")
}

// §6-16: per-depth index tiers — root pages at Hop0, frontier-2 at
// HopN, frontier≥3 at Hop3 (provider call counts asserted);
// node_indexed_until spans depths.
func TestExposures_PageTiers(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	// Sequential pages longer than every tier cap → calls per addr =
	// exactly the tier bound.
	stub.seq[root] = [][]provider.Transfer{
		{tf("txlist", "0xp1", cpA, root, "1", 90)},
		{}, {}, {}, {}, {}, {}, // len 7 > Hop0=5 → 5 calls
	}
	stub.seq[cpA] = [][]provider.Transfer{
		{tf("txlist", "0xp2", cpB, cpA, "1", 95)},
		{}, {}, {}, // len 4 > HopN=2 → 2 calls
	}
	stub.seq[cpB] = [][]provider.Transfer{
		{tf("txlist", "0xp3", cpC, cpB, "1", 99)},
		{}, {}, {}, // len 4 > Hop3=1 → 1 call
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpC, "SCAM")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	assert.Equal(t, 5, stub.called(root), "root pages at MaxPagesHop0=5")
	assert.Equal(t, 2, stub.called(cpA), "frontier-2 at MaxPagesHopN=2")
	assert.Equal(t, 1, stub.called(cpB), "frontier-3 at MaxPagesHop3=1")
	assert.Contains(t, res.Coverage.NodeIndexedUntil, cpB,
		"deep node watermark surfaced")
	require.Contains(t, itemByCp(res.Items), cpC)
	assert.Equal(t, 3, itemByCp(res.Items)[cpC].Hops)
}

// §6-16b: a failed deep index resolves as nodes_failed (never a clean
// contribution).
func TestExposures_DeepNodeFailed(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xg1", cpA, root, "1", 90),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xg2", cpB, cpA, "1", 95),
	}
	stub.errs[cpB] = errors.New("upstream 500")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Coverage.NodesFailed,
		"failed depth-3 index is reported, not hidden")
	assert.Equal(t, 1, res.Coverage.NodesExaminedByHop[3])
}

// §6-17: determinism — identical inputs → identical items AND paths
// across goroutine scheduling (multi-parent deep fixture).
func TestExposures_DeterministicDeep(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xk1", cpA, root, "1", 90),
		tf("txlist", "0xk2", cpB, root, "1", 91),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xk3", cpC, cpA, "1", 95),
	}
	stub.pages[cpB] = []provider.Transfer{
		tf("txlist", "0xk4", cpC, cpB, "1", 96),
	}
	stub.pages[cpC] = []provider.Transfer{
		tf("txlist", "0xk5", cpD, cpC, "1", 100),
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpC, "SCAM")
	seedLabel(t, pool, testNet(t, pool).ID, cpD, "HACK")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res1, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	res2, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	assert.Equal(t, res1.Items, res2.Items,
		"identical inputs → identical items AND paths")
}

// §6-18: a mid-sequence unattempted node at depth 3 → honest
// contiguous-prefix truncation for that depth — the behind-gap subtree
// is invisible, the prefix's items emit.
func TestExposures_PrefixTruncationDeep(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xt1", cpA, root, "1", 90),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xt2", cpB, cpA, "1", 95),
		tf("txlist", "0xt3", cpC, cpA, "1", 96),
	}
	stub.pages[cpB] = []provider.Transfer{
		tf("txlist", "0xt4", cpD, cpB, "1", 100),
	}
	stub.pages[cpC] = []provider.Transfer{
		tf("txlist", "0xt5", cpE, cpC, "1", 101),
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpD, "SCAM")
	seedLabel(t, pool, testNet(t, pool).ID, cpE, "HACK")
	svc, q, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
	// Pool of 6: root(2) + cpA(2) + cpB(2) — cpC never attempted.
	svc.tx = transaction.New(pool, q,
		map[string]provider.TxProvider{net.Code: stub},
		wallet.NewService(q), transaction.Config{
			FreshTTL: time.Minute, NegativeTTL: time.Minute,
			StaleAfter: 15 * time.Minute, PageSize: 100,
			Budget: map[string]int{stub.Name(): 6},
		}, nil)

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	require.NotNil(t, res.Truncated)
	assert.Equal(t, TruncMaxReqs, *res.Truncated)
	assert.Equal(t, 1, res.Coverage.FrontierEmittedByHop[3],
		"depth-3 emitted prefix stops at the unattempted node")
	assert.Equal(t, 1, res.Coverage.NodesExaminedByHop[3])
	assert.Equal(t, 3, res.Coverage.MaxDepthReached,
		"attempted (not emitted) counts toward max_depth_reached")
	items := itemByCp(res.Items)
	assert.Contains(t, items, cpD, "emitted depth-3 node's cp emits")
	_, leaked := items[cpE]
	assert.False(t, leaked,
		"beyond-prefix node's subtree never materializes")
	assert.Equal(t, 1, stub.called(cpB))
	assert.Equal(t, 0, stub.called(cpC))
}

// §6-19/26b: deadline mid-depth-3 → truncated=deadline, emitted
// prefixes intact; attempted-then-clipped counts max_depth_reached;
// max_edges/max_provider_requests each reported at most once.
func TestExposures_DeadlineDeep(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xu1", cpA, root, "1", 90),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xu2", cpB, cpA, "1", 95),
	}
	stub.pages[cpB] = []provider.Transfer{
		tf("txlist", "0xu3", cpC, cpB, "1", 100),
	}
	stub.hook = func(addr string) {
		if addr == cpB {
			time.Sleep(1200 * time.Millisecond) // outlive the deadline
		}
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpB, "SCAM")
	seedLabel(t, pool, testNet(t, pool).ID, cpC, "HACK")
	svc, _, net := newStack(t, pool, stub, Config{
		MaxNodes: 10, MaxEdges: 100, Deadline: 600 * time.Millisecond})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	require.NotNil(t, res.Truncated)
	assert.Equal(t, TruncDeadline, *res.Truncated)
	assert.Equal(t, 1, res.Coverage.FrontierEmittedByHop[2],
		"level-2 emitted prefix intact")
	assert.Equal(t, 0, res.Coverage.FrontierEmittedByHop[3])
	assert.Equal(t, 1, res.Coverage.NodesExaminedByHop[3])
	assert.Equal(t, 3, res.Coverage.MaxDepthReached,
		"attempted-then-clipped counts (§6-26b)")
	items := itemByCp(res.Items)
	assert.Contains(t, items, cpB, "hop-2 item still emits post-deadline")
	_, leaked := items[cpC]
	assert.False(t, leaked, "unresolved depth-3 node emits nothing")
}

// §6-22/35: max_hops=1 — frontier_2 materialized for coverage but NOT
// scheduled: no max_nodes even when |frontier_2| exceeds the cap.
func TestExposures_MaxHops1Coverage(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xv1", cpA, root, "1", 90),
		tf("txlist", "0xv2", cpB, root, "1", 91),
		tf("txlist", "0xv3", cpC, root, "1", 92),
	}
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 1, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 1})
	require.NoError(t, err)
	assert.Nil(t, res.Truncated,
		"|frontier_2|=3 > MaxNodes_2=1 but unscheduled → no max_nodes")
	cov := res.Coverage
	assert.Equal(t, 3, cov.CounterpartiesTotal)
	assert.Equal(t, 0, cov.NodesExamined)
	assert.Equal(t, 0, cov.PositionsEmitted)
	assert.Equal(t, 3, cov.NodesRemaining)
	assert.Equal(t, 0, cov.MaxDepthReached)
	assert.Equal(t, map[int]int{2: 3}, cov.FrontierByHop)
	assert.Equal(t, map[int]int{2: 0}, cov.NodesExaminedByHop)
	assert.Equal(t, map[int]int{2: 0}, cov.FrontierEmittedByHop)
	assert.Equal(t, map[int]int{2: 3}, cov.NodesRemainingByHop)
}

// §6-23: ≥4-sequence fixture with frontier order ≠ address order —
// emitted paths are the true top-3 by ADDRESS-tuple order
// (via₂-most-significant), not frontier order.
func TestExposures_PathTupleOrdering(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	v2lo := "0x0000000000000000000000000000000000000019"
	v2hi := "0x00000000000000000000000000000000000000e9"
	v3P := "0x00000000000000000000000000000000000000b9"
	v3Q := "0x00000000000000000000000000000000000000c9"
	stub := newStub("chainscan")
	stub.head = 100
	// v2hi gets the earlier edge → frontier position 0 despite the
	// larger address — frontier order ≠ address order.
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xhi", v2hi, root, "1", 90),
		tf("txlist", "0xlo", v2lo, root, "1", 91),
	}
	stub.pages[v2hi] = []provider.Transfer{
		tf("txlist", "0xhp", v3P, v2hi, "1", 95),
		tf("txlist", "0xhq", v3Q, v2hi, "1", 96),
	}
	stub.pages[v2lo] = []provider.Transfer{
		tf("txlist", "0xlp", v3P, v2lo, "1", 95),
		tf("txlist", "0xlq", v3Q, v2lo, "1", 96),
	}
	stub.pages[v3P] = []provider.Transfer{
		tf("txlist", "0xpc", cpD, v3P, "1", 100),
	}
	stub.pages[v3Q] = []provider.Transfer{
		tf("txlist", "0xqc", cpD, v3Q, "1", 100),
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpD, "SCAM")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	it, ok := itemByCp(res.Items)[cpD]
	require.True(t, ok)
	require.Len(t, it.Paths, 3, "4 sequences → top-3 by tuple order")
	type sig struct{ e1, e2, e3 string }
	var seqs []sig
	for _, p := range it.Paths {
		require.Len(t, p.Edges, 3)
		seqs = append(seqs, sig{
			p.Edges[0].TxHash, p.Edges[1].TxHash, p.Edges[2].TxHash})
	}
	// Tuple order is via₂-most-significant by ADDRESS: v2lo < v2hi →
	// (lo,P), (lo,Q), (hi,P) — the (hi,Q) tail drops even though v2hi
	// is the earlier frontier position.
	assert.Equal(t, []sig{
		{"0xlo", "0xlp", "0xpc"},
		{"0xlo", "0xlq", "0xqc"},
		{"0xhi", "0xhp", "0xpc"},
	}, seqs)
}

// §6-24: hop-1 ordering with an interleaved sentinel — cpOrder
// [dead, A, B] → item order dead, A, B (minPos = rank over ALL
// counterparties incl. sentinels).
func TestExposures_SentinelOrder(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xs1", dead, root, "1", 90),
		tf("txlist", "0xs2", cpA, root, "1", 91),
		tf("txlist", "0xs3", cpB, root, "1", 92),
	}
	for _, a := range []string{dead, cpA, cpB} {
		seedLabel(t, pool, testNet(t, pool).ID, a, "SCAM")
	}
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 1})
	require.NoError(t, err)
	require.Len(t, res.Items, 3)
	assert.Equal(t, dead, res.Items[0].Counterparty)
	assert.Equal(t, cpA, res.Items[1].Counterparty)
	assert.Equal(t, cpB, res.Items[2].Counterparty)
	// The sentinel holds rank 0 of the hop-1 minPos domain.
	assert.Equal(t, 0, res.Items[0].minPos)
	assert.Equal(t, 1, res.Items[1].minPos)
	assert.Equal(t, 2, res.Items[2].minPos)
}

// §6-26a: scheduled-but-zero-attempts — budget gone before the first
// depth-3 attempt → max_depth_reached stays 2.
func TestExposures_MaxDepthReachedBudget(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xm1", cpA, root, "1", 90),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xm2", cpB, cpA, "1", 95),
	}
	svc, q, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
	// Pool of 4: root(2) + cpA(2) — frontier_3 exists, zero attempts.
	svc.tx = transaction.New(pool, q,
		map[string]provider.TxProvider{net.Code: stub},
		wallet.NewService(q), transaction.Config{
			FreshTTL: time.Minute, NegativeTTL: time.Minute,
			StaleAfter: 15 * time.Minute, PageSize: 100,
			Budget: map[string]int{stub.Name(): 4},
		}, nil)

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Coverage.MaxDepthReached,
		"scheduled-but-zero-attempts does not count")
	assert.Equal(t, 1, res.Coverage.FrontierByHop[3])
	assert.Equal(t, 0, res.Coverage.NodesExaminedByHop[3])
	assert.Equal(t, 1, res.Coverage.NodesRemainingByHop[3])
	assert.Equal(t, 0, stub.called(cpB))
}

// §6-27: per-term integer half-up — two terms each at a .5 boundary
// round UP independently (Σ of rounded terms, not rounded Σ).
func TestExposureScoreRounding(t *testing.T) {
	mk := func(code, vt, conf string) labelEvidence {
		return labelEvidence{CategoryCode: code,
			CategoryType: "MALICIOUS_ACTIVITY",
			Verification: vt, Confidence: conf}
	}
	// verified/unknown-conf MALICIOUS at hop-2:
	// 70×10×7×8 = 39200 → 39200/1600 = 24.5 → 25.
	l := []labelEvidence{mk("SCAM", "verified", "unknown")}
	got := exposureScore(2, l, nil)
	require.NotNil(t, got)
	assert.Equal(t, 25, *got)
	// Two such terms: per-term Σ = 50 (naive round(49) would be 49).
	got = exposureScore(2, []labelEvidence{
		mk("SCAM", "verified", "unknown"),
		mk("HACK", "verified", "unknown")}, nil)
	require.NotNil(t, got)
	assert.Equal(t, 50, *got, "per-term .5 rounds up independently")
	// SANCTIONED label verified/unknown hop-1: 85×10×7×16=95200 →
	// 59.5 → 60.
	got = exposureScore(1, []labelEvidence{
		{CategoryCode: "SANCTIONED", CategoryType: "LEGAL",
			Verification: "verified", Confidence: "unknown"}}, nil)
	require.NotNil(t, got)
	assert.Equal(t, 60, *got)
	// All informational → null.
	got = exposureScore(1, []labelEvidence{
		{CategoryCode: "EXCHANGE", CategoryType: "ENTITY_TYPE",
			Verification: "verified", Confidence: "high"}}, nil)
	assert.Nil(t, got)
}

// §6-28: beyond-prefix glimpse — a node seen ONLY via a
// done-but-not-emitted depth-2 scan stays invisible to cpAgg_3, but
// the same node reachable via an emitted depth-3 path emits at hop 3.
// The deterministic gap: a2's index state carries a foreign source_id,
// so a held row lock stalls its provider-switch reset until positions
// 0,2,3 drain the provider pool — its EnsureIndexed then returns
// BudgetExhausted (pulled, never attempted). viaY is pre-indexed
// fresh so depth-3 pull-through still resolves it for free.
func TestExposures_BeyondPrefixGlimpse(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	ctx := context.Background()
	a1 := "0x0000000000000000000000000000000000000aa1"
	a2 := "0x0000000000000000000000000000000000000aa2"
	a3 := "0x0000000000000000000000000000000000000aa3"
	a4 := "0x0000000000000000000000000000000000000aa4"
	viaY := "0x0000000000000000000000000000000000000bb9"
	cpX := "0x0000000000000000000000000000000000000cc9"

	// a2's index state claims a different adapter's provenance: the
	// provider-switch reset (wallet_index_state UPDATE, pre-tryTake)
	// blocks on the held row lock while a1/a3/a4 drain the pool —
	// a2's head tryTake then fails (pulled, never attempted). The
	// wallets table is untouched, so the root's BulkResolveWallets
	// batch is unaffected.
	var a2w int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO wallets (network_id, address, normalized_address, address_type)
		VALUES ((SELECT id FROM blockchain_networks WHERE code='ethereum'),
			$1, $1, 'evm') RETURNING id`, a2).Scan(&a2w))
	_, err := pool.Exec(ctx, `
		INSERT INTO wallet_index_state (network_id, wallet_id, source_id, status)
		VALUES ((SELECT id FROM blockchain_networks WHERE code='ethereum'),
			$1, (SELECT id FROM data_sources WHERE code='nodereal'),
			'not_indexed')`, a2w)
	require.NoError(t, err)
	lockTx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer lockTx.Rollback(ctx)
	_, err = lockTx.Exec(ctx,
		`UPDATE wallet_index_state SET updated_at = NOW() WHERE wallet_id = $1`,
		a2w)
	require.NoError(t, err)

	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0z01", a1, root, "1", 90),
		tf("txlist", "0z02", a2, root, "1", 91),
		tf("txlist", "0z03", a3, root, "1", 92),
		tf("txlist", "0z04", a4, root, "1", 93),
	}
	stub.pages[a1] = []provider.Transfer{
		tf("txlist", "0z11", viaY, a1, "1", 95),
	}
	stub.pages[a3] = []provider.Transfer{
		tf("txlist", "0z12", cpX, a3, "1", 95), // the glimpse — beyond prefix
	}
	stub.pages[viaY] = []provider.Transfer{
		tf("txlist", "0z14", cpX, viaY, "1", 100), // emitted hop-3 path
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpX, "SCAM")

	svc, q, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
	// Pool 8 = root(2) + a1(2) + a3(2) + a4(2) — a2's tryTake finds 0.
	svc.tx = transaction.New(pool, q,
		map[string]provider.TxProvider{net.Code: stub},
		wallet.NewService(q), transaction.Config{
			FreshTTL: time.Minute, NegativeTTL: time.Minute,
			StaleAfter: 15 * time.Minute, PageSize: 100,
			Budget: map[string]int{stub.Name(): 8},
		}, nil)
	// viaY already resolves fresh: at depth 3 it emits with zero
	// provider calls (the pool gates calls, not pull-through
	// resolution of persisted index state).
	_, err = svc.tx.EnsureIndexed(ctx, net, viaY, transaction.EnsureOptions{
		Budget: transaction.NewBudget(10)})
	require.NoError(t, err)

	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := svc.Exposures(ctx, net, root, Request{MaxHops: 3})
		done <- outcome{r, err}
	}()
	// Hold the lock until the three unconstrained positions finished
	// indexing (terminal 'fresh' rows prove their budget charges
	// settled — a2's released tryTake must find the pool empty).
	ok3 := assert.Eventually(t, func() bool {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM wallet_index_state s
			JOIN wallets w ON w.id = s.wallet_id
			WHERE w.normalized_address = ANY($1) AND s.status = 'fresh'`,
			[]string{a1, a3, a4}).Scan(&n); err != nil {
			return false
		}
		return n == 3
	}, 10*time.Second, 10*time.Millisecond)
	if !ok3 {
		t.Fatalf("positions never finished: calls=%v",
			map[string]int{root: stub.called(root), a1: stub.called(a1),
				a2: stub.called(a2), a3: stub.called(a3), a4: stub.called(a4),
				viaY: stub.called(viaY)})
	}
	require.NoError(t, lockTx.Commit(ctx))
	out := <-done
	require.NoError(t, out.err)
	res := out.res

	assert.Equal(t, 1, res.Coverage.FrontierEmittedByHop[2],
		"a2 stalled-then-budget-clipped → emitted prefix = a1 only")
	assert.Equal(t, 3, res.Coverage.NodesExaminedByHop[2],
		"a1,a3,a4 attempted; a2 never attempted")
	assert.Equal(t, 0, stub.called(a2), "a2 never reached the provider")
	assert.Equal(t, 1, stub.called(a3), "the glimpse scan ran")
	assert.Equal(t, 1, res.Coverage.FrontierByHop[3],
		"frontier_3 = viaY only — the beyond-prefix glimpse never seeds")
	assert.Equal(t, 1, res.Coverage.FrontierEmittedByHop[3])
	assert.Equal(t, 1, res.Coverage.NodesExaminedByHop[3],
		"viaY resolved fresh — attempted, zero provider calls")
	assert.Equal(t, int64(8), res.Coverage.ProviderCalls)
	assert.Equal(t, 3, res.Coverage.MaxDepthReached)
	require.NotNil(t, res.Truncated)
	assert.Equal(t, TruncMaxReqs, *res.Truncated,
		"a2 sits unattempted with the pool empty — reported honestly")
	it, ok := itemByCp(res.Items)[cpX]
	require.True(t, ok, "cpX must emit via the emitted depth-3 path")
	assert.Equal(t, 3, it.Hops,
		"the beyond-prefix glimpse never leaked into cpAgg_3")
	require.Len(t, it.Paths, 1)
	require.Len(t, it.Paths[0].Edges, 3)
	// Wire order is root-first: a1→root, viaY→a1, cpX→viaY.
	assert.Equal(t, a1, it.Paths[0].Edges[0].FromAddress)
	assert.Equal(t, root, it.Paths[0].Edges[0].ToAddress)
	assert.Equal(t, viaY, it.Paths[0].Edges[1].FromAddress)
	assert.Equal(t, a1, it.Paths[0].Edges[1].ToAddress)
	assert.Equal(t, cpX, it.Paths[0].Edges[2].FromAddress)
	assert.Equal(t, viaY, it.Paths[0].Edges[2].ToAddress)
}

// §6-29: direction=out + an in-only hop-1 cp filtered from cpAgg_2 —
// the same node IS a hop-2 item when reached by an emitted deep edge
// (post-filter dedup domain pinned).
func TestExposures_DirectionDedupDomain(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xw1", cpB, root, "1", 90), // inbound — filtered
		tf("txlist", "0xw2", root, cpA, "1", 91), // outbound — kept
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xw3", cpB, cpA, "1", 95), // cpB reachable at depth 2
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpB, "SCAM")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root,
		Request{MaxHops: 2, Direction: "out"})
	require.NoError(t, err)
	it, ok := itemByCp(res.Items)[cpB]
	require.True(t, ok,
		"direction-filtered hop-1 cp emits at hop-2 via an emitted edge")
	assert.Equal(t, 2, it.Hops)
}

// §6-30/7: deep sentinel — 0xdead as a depth-2 candidate: never a
// frontier_3 position or provider call, still emits its item carrying
// the contributing path edges.
func TestExposures_DeepSentinel(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0xy1", cpA, root, "1", 90),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0xy2", dead, cpA, "1", 95),
		tf("txlist", "0xy3", cpB, cpA, "1", 96),
	}
	stub.pages[cpB] = []provider.Transfer{
		tf("txlist", "0xy4", cpC, cpB, "1", 100),
	}
	seedLabel(t, pool, testNet(t, pool).ID, dead, "SCAM")
	seedLabel(t, pool, testNet(t, pool).ID, cpC, "HACK")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)

	it, ok := itemByCp(res.Items)[dead]
	require.True(t, ok, "sentinel still emits its matched item")
	assert.Equal(t, 2, it.Hops)
	require.Len(t, it.Paths, 1)
	require.Len(t, it.Paths[0].Edges, 2)
	assert.Equal(t, "0xy2", it.Paths[0].Edges[1].TxHash,
		"the contributing edge is carried as evidence")
	assert.Equal(t, 0, stub.called(dead),
		"sentinel never consumes a provider call")
	assert.Equal(t, 1, res.Coverage.FrontierByHop[3],
		"frontier_3 = {cpB} only — sentinel excluded from positions")
	require.Contains(t, itemByCp(res.Items), cpC)
}

// §6-33: a sentinel seen as a level-1 counterparty emits at hop-1 and
// never re-emits deeper (dedup covers sentinels).
func TestExposures_SentinelNoReemit(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0yz1", dead, root, "1", 90),
		tf("txlist", "0yz2", cpA, root, "1", 91),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0yz3", dead, cpA, "1", 95), // dead again at depth 2
	}
	seedLabel(t, pool, testNet(t, pool).ID, dead, "SCAM")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.NoError(t, err)
	var got []Item
	for _, it := range res.Items {
		if it.Counterparty == dead {
			got = append(got, it)
		}
	}
	require.Len(t, got, 1, "sentinel emits once")
	assert.Equal(t, 1, got[0].Hops)
	assert.Equal(t, 0, stub.called(dead))
}

// §6-34: hop-3 minPos = min FIRST-via position index in frontier_2
// across contributing sequences — the domain is asserted by ordering,
// not just tuple content.
func TestExposures_Hop3MinPos(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	lo := "0x0000000000000000000000000000000000000010" // via B only (pos1)
	hi := "0x00000000000000000000000000000000000000f0" // via A only (pos0)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0y01", cpA, root, "1", 90), // pos 0
		tf("txlist", "0y02", cpB, root, "1", 91), // pos 1
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0y03", cpC, cpA, "1", 95),
	}
	stub.pages[cpB] = []provider.Transfer{
		tf("txlist", "0y04", cpD, cpB, "1", 95),
	}
	stub.pages[cpC] = []provider.Transfer{
		tf("txlist", "0y05", hi, cpC, "1", 100), // hop-3 via cpA → minPos 0
	}
	stub.pages[cpD] = []provider.Transfer{
		tf("txlist", "0y06", lo, cpD, "1", 100), // hop-3 via cpB → minPos 1
	}
	seedLabel(t, pool, testNet(t, pool).ID, hi, "SCAM")
	seedLabel(t, pool, testNet(t, pool).ID, lo, "HACK")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	items := itemByCp(res.Items)
	require.Contains(t, items, hi)
	require.Contains(t, items, lo)
	assert.Equal(t, 3, items[hi].Hops)
	assert.Equal(t, 3, items[lo].Hops)
	assert.Equal(t, 0, items[hi].minPos, "first-via cpA is position 0")
	assert.Equal(t, 1, items[lo].minPos, "first-via cpB is position 1")
	// Item order: hi (minPos 0) precedes lo (minPos 1) despite
	// lo < hi — the domain is the frontier_2 position index.
	idx := map[string]int{}
	for i, it := range res.Items {
		idx[it.Counterparty] = i
	}
	assert.Less(t, idx[hi], idx[lo])
}

// §6-6/10/11/25: cross-chain label correlation — a bsc label on an
// ethereum-graph counterparty emits kind:"label" with network +
// listed_address (original representation), never participates in
// consider(), scores at full label weight; an unmatched depth-2 node's
// counterparty seeds frontier_3.
func TestExposures_ForeignLabel(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	ethNet := testNet(t, pool)
	bscNet := netByCode(t, pool, "bsc")

	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0j01", cpA, root, "1", 90),
	}
	stub.pages[cpA] = []provider.Transfer{
		tf("txlist", "0j02", cpB, cpA, "1", 95),
		tf("txlist", "0j03", cpC, cpA, "1", 96),
	}
	stub.pages[cpB] = []provider.Transfer{
		tf("txlist", "0j04", cpD, cpB, "1", 100),
	}
	// cpB: labeled on bsc ONLY, checksum-case original address —
	// listed_address must be the foreign original, not the normalized.
	checksum := "0x00000000000000000000000000000000000000B2"
	seedLabelV(t, pool, bscNet.ID, cpB, "SCAM", "verified", "high",
		"foreign-scam", checksum)
	// cpC: local MIXER (informational) + foreign HACK — category stays
	// local: foreign never joins consider().
	seedLabel(t, pool, ethNet.ID, cpC, "MIXER")
	seedLabelV(t, pool, bscNet.ID, cpC, "HACK", "reported", "high", "x", "")
	seedLabel(t, pool, ethNet.ID, cpD, "SCAM")

	svc, _, net := newStackNets(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100},
		[]network.Network{ethNet, bscNet})
	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 3})
	require.NoError(t, err)
	items := itemByCp(res.Items)

	b, ok := items[cpB]
	require.True(t, ok, "foreign-labeled node emits")
	assert.Equal(t, 2, b.Hops)
	assert.Equal(t, CatCrossChain, b.Category,
		"solely-foreign item → cross_chain_listing")
	var foreign *MatchedSignal
	for i := range b.MatchedSignals {
		if b.MatchedSignals[i].Kind == SigLabel {
			foreign = &b.MatchedSignals[i]
		}
		assert.NotEqual(t, SigListing, b.MatchedSignals[i].Kind,
			"no duplicate kind:listing signal from label rows")
	}
	require.NotNil(t, foreign)
	assert.Equal(t, SigLabel, foreign.Kind)
	require.NotNil(t, foreign.Network)
	assert.Equal(t, "bsc", *foreign.Network)
	assert.Equal(t, checksum, foreign.ListedAddress,
		"listed_address = foreign wallets.address original representation")
	assert.Equal(t, "verified", foreign.VerificationStatus)
	// §6-25: foreign SCAM verified/high at hop-2 → 70×0.5 = 35.
	require.NotNil(t, b.ExposureScore)
	assert.Equal(t, 35, *b.ExposureScore)

	// Category never wins for foreign: cpC's local MIXER stands.
	c, ok := items[cpC]
	require.True(t, ok)
	assert.Equal(t, "MIXER", c.Category,
		"foreign HACK never participates in consider()")

	// §6-6: cpB is unmatched locally yet its counterparty cpD reaches
	// hop-3 — clean intermediaries seed deeper frontiers.
	d, ok := items[cpD]
	require.True(t, ok)
	assert.Equal(t, 3, d.Hops)
	assert.Equal(t, 2, res.Coverage.FrontierByHop[3],
		"frontier_3 = {cpB, cpC} — clean intermediaries seed deeper frontiers")

	// §6-11: the BFS never crossed networks — provider calls only for
	// ethereum graph addrs (a bsc-only wallet can never be a position).
	assert.Equal(t, 1, stub.called(cpB))
	assert.Equal(t, 0, stub.called("0x0000000000000000000000000000000000000b5c"))
}

// §6-32: single-network resolver → foreign set empty; the ANY('{}')
// path returns zero rows, no error, no foreign signals.
func TestExposures_SingleNetResolver(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0h01", cpA, root, "1", 90),
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpA, "SCAM")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.NoError(t, err, "ANY('{}') foreign lookup must not error")
	require.Len(t, res.Items, 1)
	for _, sig := range res.Items[0].MatchedSignals {
		assert.Nil(t, sig.Network,
			"no foreign provenance under a single-network resolver")
	}
}
