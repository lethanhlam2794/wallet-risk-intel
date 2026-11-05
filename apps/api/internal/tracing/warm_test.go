package tracing

// Warm index enqueue hook tests (warm-index-queue v1): the pure decision
// table plus fail-open/sentinel/hop-1-only behavior in Exposures.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/testutil"
	"checkwallet/internal/warmindex"
)

func strptr(s string) *string { return &s }

func TestWarmEnqueuePlan(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sigs     []MatchedSignal
		priority int
		reason   string
		ok       bool
	}{
		{"sanctions_direct", []MatchedSignal{
			{Kind: SigSanctionsDirect, Category: CatSanctioned},
		}, warmindex.PrioritySanctionsMatch, "sanctions_direct_match", true},
		{"sanctions beats label regardless of order", []MatchedSignal{
			{Kind: SigLabel, Category: "SCAM"},
			{Kind: SigSanctionsDirect, Category: CatSanctioned},
		}, warmindex.PrioritySanctionsMatch, "sanctions_direct_match", true},
		{"risky label", []MatchedSignal{
			{Kind: SigLabel, Category: "MIXER"},
		}, warmindex.PriorityRiskyLabel, "label:MIXER", true},
		{"first risky label wins deterministically", []MatchedSignal{
			{Kind: SigLabel, Category: "HACK"},
			{Kind: SigLabel, Category: "SCAM"},
		}, warmindex.PriorityRiskyLabel, "label:HACK", true},
		{"non-risky then risky still enqueues", []MatchedSignal{
			{Kind: SigLabel, Category: "GAMBLING"},
			{Kind: SigLabel, Category: "PHISHING"},
		}, warmindex.PriorityRiskyLabel, "label:PHISHING", true},
		{"foreign risky label carries net", []MatchedSignal{
			{Kind: SigLabel, Category: "RANSOMWARE", Network: strptr("bsc")},
		}, warmindex.PriorityRiskyLabel, "label:RANSOMWARE@bsc", true},
		{"GAMBLING never enqueues", []MatchedSignal{
			{Kind: SigLabel, Category: "GAMBLING"},
		}, 0, "", false},
		{"EXCHANGE never enqueues", []MatchedSignal{
			{Kind: SigLabel, Category: "EXCHANGE"},
		}, 0, "", false},
		{"narrow risky set — DRAINER excluded", []MatchedSignal{
			{Kind: SigLabel, Category: "DRAINER"},
		}, 0, "", false},
		{"narrow risky set — MONEY_LAUNDERING excluded", []MatchedSignal{
			{Kind: SigLabel, Category: "MONEY_LAUNDERING"},
		}, 0, "", false},
		{"listing-only never enqueues", []MatchedSignal{
			{Kind: SigListing, Category: CatCrossChain},
		}, 0, "", false},
		{"empty", nil, 0, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pri, reason, ok := warmEnqueuePlan(tc.sigs)
			assert.Equal(t, tc.priority, pri)
			assert.Equal(t, tc.reason, reason)
			assert.Equal(t, tc.ok, ok)
		})
	}
	// Every risky category enqueues; the seeded-but-excluded set does not.
	for _, cat := range []string{"SCAM", "PHISHING", "HACK", "RANSOMWARE", "MIXER"} {
		_, _, ok := warmEnqueuePlan([]MatchedSignal{{Kind: SigLabel, Category: cat}})
		assert.True(t, ok, "category %s must enqueue", cat)
	}
}

type enqCall struct {
	netID    int64
	addr     string
	priority int
	reason   string
}

type fakeEnqueuer struct {
	mu    sync.Mutex
	calls []enqCall
	err   error
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, netID int64, addr string,
	priority int, reason string, _ *uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, enqCall{netID, addr, priority, reason})
	return f.err
}

func (f *fakeEnqueuer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// Risky hop-1 neighbor → one enqueue carrying priority+reason.
func TestExposures_WarmEnqueueRiskyNeighbor(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0x1aa", cpA, root, "500", 90),
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpA, "SCAM")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
	fe := &fakeEnqueuer{}
	svc.SetEnqueuer(fe, slog.New(slog.DiscardHandler))

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 1})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	require.Equal(t, 1, fe.count())
	c := fe.calls[0]
	assert.Equal(t, net.ID, c.netID)
	assert.Equal(t, cpA, c.addr)
	assert.Equal(t, warmindex.PriorityRiskyLabel, c.priority)
	assert.Equal(t, "label:SCAM", c.reason)
}

// Sanctions-direct neighbor → priority 100 (evidence outranks labels).
func TestExposures_WarmEnqueueSanctions(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	netID := testNet(t, pool).ID
	_, err := pool.Exec(context.Background(), `
		INSERT INTO sanctions_snapshots (source_id, version, checksum,
			entity_count, wallet_count, program_count, published_at)
		VALUES ((SELECT id FROM data_sources WHERE code='ofac_sdn'),
			'snap-w', 'c', 1, 1, 0, NOW())`)
	require.NoError(t, err)
	var entityID int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO sanctions_entities (source_id, external_entity_id, primary_name)
		VALUES ((SELECT id FROM data_sources WHERE code='ofac_sdn'),
			'OFAC-W-1', 'Warm Entity') RETURNING id`).Scan(&entityID))
	_, err = pool.Exec(context.Background(), `
		INSERT INTO sanctions_wallets (sanctions_entity_id, listed_address,
			normalized_address, network_id, identifier_type, match_scope)
		VALUES ($1, $2, $2, $3, 'ETH', 'CHAIN_SPECIFIC')`,
		entityID, cpA, netID)
	require.NoError(t, err)

	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0x1bb", cpA, root, "500", 90),
	}
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
	fe := &fakeEnqueuer{}
	svc.SetEnqueuer(fe, slog.New(slog.DiscardHandler))

	_, err = svc.Exposures(context.Background(), net, root, Request{MaxHops: 1})
	require.NoError(t, err)
	require.Equal(t, 1, fe.count())
	assert.Equal(t, warmindex.PrioritySanctionsMatch, fe.calls[0].priority)
	assert.Equal(t, "sanctions_direct_match", fe.calls[0].reason)
}

// GAMBLING/EXCHANGE neighbors are classifications, not crimes — never
// enqueued (contract invariant).
func TestExposures_WarmEnqueueGamblingExchange(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0x1c1", cpA, root, "500", 90),
		tf("txlist", "0x1c2", cpB, root, "500", 91),
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpA, "GAMBLING")
	seedLabel(t, pool, testNet(t, pool).ID, cpB, "EXCHANGE")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
	fe := &fakeEnqueuer{}
	svc.SetEnqueuer(fe, slog.New(slog.DiscardHandler))

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 1})
	require.NoError(t, err)
	require.Len(t, res.Items, 2, "items still emit — only enqueue is gated")
	assert.Equal(t, 0, fe.count())
}

// Sentinel counterparties (0x0/0xdead) still emit items but are never
// enqueued — indexing them is pure waste.
func TestExposures_WarmEnqueueSentinelSkipped(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0x1d1", dead, root, "500", 90),
	}
	seedLabel(t, pool, testNet(t, pool).ID, dead, "SCAM")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
	fe := &fakeEnqueuer{}
	svc.SetEnqueuer(fe, slog.New(slog.DiscardHandler))

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 1})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	assert.Equal(t, 0, fe.count(), "sentinels never reach the queue")
}

// Hop ≥ 2 items never enqueue — the hook is hop-1-only by contract.
func TestExposures_WarmEnqueueHop2Skipped(t *testing.T) {
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
	fe := &fakeEnqueuer{}
	svc.SetEnqueuer(fe, slog.New(slog.DiscardHandler))

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 2})
	require.NoError(t, err)
	var hop2 *Item
	for i := range res.Items {
		if res.Items[i].Hops == 2 {
			hop2 = &res.Items[i]
		}
	}
	require.NotNil(t, hop2, "cpB still emits its hop-2 item")
	assert.Equal(t, 0, fe.count())
}

// Foreign-network risky label → enqueued as "label:<CAT>@<net>" — the cp
// is a real counterparty on THIS network; the foreign evidence is only
// prioritization input, never a direct-listing claim.
func TestExposures_WarmEnqueueForeignLabel(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	bsc := netByCode(t, pool, "bsc")
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0x1e1", cpA, root, "500", 90),
	}
	seedLabel(t, pool, bsc.ID, cpA, "MIXER")
	svc, _, net := newStackNets(t, pool, stub,
		Config{MaxNodes: 10, MaxEdges: 100},
		[]network.Network{testNet(t, pool), bsc})
	fe := &fakeEnqueuer{}
	svc.SetEnqueuer(fe, slog.New(slog.DiscardHandler))

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 1})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	require.Equal(t, 1, fe.count())
	c := fe.calls[0]
	assert.Equal(t, cpA, c.addr)
	assert.Equal(t, "label:MIXER@bsc", c.reason)
	assert.Equal(t, warmindex.PriorityRiskyLabel, c.priority)
}

// Fail-open (contract): an enqueue error is logged, never propagated —
// the screening result is unchanged.
func TestExposures_WarmEnqueueFailOpen(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	stub := newStub("chainscan")
	stub.head = 100
	stub.pages[root] = []provider.Transfer{
		tf("txlist", "0x1f1", cpA, root, "500", 90),
	}
	seedLabel(t, pool, testNet(t, pool).ID, cpA, "SCAM")
	svc, _, net := newStack(t, pool, stub, Config{MaxNodes: 10, MaxEdges: 100})
	fe := &fakeEnqueuer{err: errors.New("warm_queue db down")}
	svc.SetEnqueuer(fe, slog.New(slog.DiscardHandler))

	res, err := svc.Exposures(context.Background(), net, root, Request{MaxHops: 1})
	require.NoError(t, err, "enqueue failure must never fail the screening")
	require.Len(t, res.Items, 1)
	assert.Equal(t, 1, fe.count(), "the attempt happened — it just failed")
}
