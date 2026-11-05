package warmindex

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
	"checkwallet/internal/transaction"
	"checkwallet/internal/wallet"
)

// stubTx is a scriptable TxProvider (failover-test stub style): sequential
// pages per address, per-address call counters for claim-disjointness
// assertions.
type stubTx struct {
	name    string
	enabled bool
	head    int64
	headErr error
	// headHook runs inside HeadBlock before the reply — shutdown tests
	// use it to kill the parent ctx mid-drain.
	headHook func(ctx context.Context)

	headCalls atomic.Int64
	lookCalls atomic.Int64

	mu      sync.Mutex
	pages   map[string][][]provider.Transfer
	idx     map[string]int
	lookEr  map[string]error
	calls   map[string]int
	streams []provider.StreamStatus
}

func newStub(name string) *stubTx {
	return &stubTx{name: name, enabled: true,
		pages: map[string][][]provider.Transfer{},
		idx:   map[string]int{}, lookEr: map[string]error{},
		calls: map[string]int{}}
}

func (s *stubTx) Name() string                 { return s.name }
func (s *stubTx) Enabled(network.Network) bool { return s.enabled }
func (s *stubTx) HeadBlock(ctx context.Context, _ network.Network) (int64, error) {
	s.headCalls.Add(1)
	if s.headHook != nil {
		s.headHook(ctx)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return s.head, s.headErr
}

func (s *stubTx) LookupTransfers(_ context.Context, _ network.Network,
	addr string, _ provider.TransferLookupOpts) (*provider.TransferPage, error) {
	s.lookCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[addr]++
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

func (s *stubTx) called(addr string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[addr]
}

// checkerFunc is a StatusChecker test double — the killswitch read.
type checkerFunc func(context.Context, string) bool

func (f checkerFunc) Active(ctx context.Context, code string) bool {
	return f(ctx, code)
}

func testCfg() Config {
	return Config{Enabled: true, DailyCap: 200, ItemBudget: 8,
		MaxPages: 10, PollInterval: time.Second}
}

func newLane(t *testing.T, pool *pgxpool.Pool, q *db.Queries,
	stub *stubTx, net network.Network) ProviderLane {
	t.Helper()
	return newLaneNegTTL(t, pool, q, stub, net, time.Minute)
}

// newLaneNegTTL is newLane with an explicit FAILED cooldown — dead-letter
// tests shrink it below real time so repeated drains actually retry.
func newLaneNegTTL(t *testing.T, pool *pgxpool.Pool, q *db.Queries,
	stub *stubTx, net network.Network, negTTL time.Duration) ProviderLane {
	t.Helper()
	return newLaneCfg(t, pool, q, stub, net, negTTL, 15*time.Minute)
}

// newLaneStale is newLane with an explicit 'indexing' claim staleness —
// shutdown tests shrink it so a follow-up cycle re-attempts instead of
// serving the lingering live claim.
func newLaneStale(t *testing.T, pool *pgxpool.Pool, q *db.Queries,
	stub *stubTx, net network.Network, staleAfter time.Duration) ProviderLane {
	t.Helper()
	return newLaneCfg(t, pool, q, stub, net, time.Minute, staleAfter)
}

func newLaneCfg(t *testing.T, pool *pgxpool.Pool, q *db.Queries,
	stub *stubTx, net network.Network, negTTL, staleAfter time.Duration) ProviderLane {
	t.Helper()
	txSvc := transaction.New(pool, q,
		map[string][]provider.TxProvider{net.Code: {stub}},
		wallet.NewService(q), transaction.Config{
			FreshTTL: 10 * time.Minute, NegativeTTL: negTTL,
			StaleAfter: staleAfter, PageSize: 100,
			MaxPagesHop0: 5, MaxPagesHopN: 2,
			ReorgTail: map[string]int64{net.Code: 4},
			Budget:    map[string]int{stub.Name(): 8},
		}, nil)
	return ProviderLane{Code: stub.Name(), Adapter: stub, Tx: txSvc}
}

func newDrainer(t *testing.T, pool *pgxpool.Pool,
	nets []network.Network, lanes []ProviderLane,
	checker transaction.StatusChecker, cfg Config) *Drainer {
	t.Helper()
	return NewDrainer(db.New(pool), network.NewResolver(nets), lanes,
		checker, cfg, nil)
}

// Claim ordering: priority DESC, id ASC tiebreak (D2); one item per cycle.
func TestDrainer_ClaimOrdering(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	stub.head = 100
	lane := newLane(t, pool, db.New(pool), stub, net)
	d := newDrainer(t, pool, []network.Network{net}, []ProviderLane{lane},
		nil, testCfg())

	low1 := insertWarmItem(t, pool, net.ID, addrW1, PriorityRiskyLabel)
	low2 := insertWarmItem(t, pool, net.ID, addrW2, PriorityRiskyLabel)
	high := insertWarmItem(t, pool, net.ID, addrW3, PrioritySanctionsMatch)

	done, err := d.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, "done", warmRow(t, pool, high).Status,
		"highest priority drains first")
	assert.Equal(t, "pending", warmRow(t, pool, low1).Status)

	done, err = d.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, "done", warmRow(t, pool, low1).Status,
		"equal priority drains in id order")
	assert.Equal(t, "pending", warmRow(t, pool, low2).Status)
}

// SKIP LOCKED (contract): two concurrent drainers sharing one stub via
// SEPARATE transaction.Service lanes must never process the same item —
// a double claim would show as a second LookupTransfers for the address.
func TestDrainer_ConcurrentClaimsDisjoint(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	stub.head = 100
	addrs := []string{addrW1, addrW2, addrW3, addrW4}
	for _, a := range addrs {
		insertWarmItem(t, pool, net.ID, a, PriorityRiskyLabel)
	}
	q := db.New(pool)
	laneA := newLane(t, pool, q, stub, net)
	laneB := newLane(t, pool, q, stub, net)
	dA := newDrainer(t, pool, []network.Network{net}, []ProviderLane{laneA}, nil, testCfg())
	dB := newDrainer(t, pool, []network.Network{net}, []ProviderLane{laneB}, nil, testCfg())

	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	drain := func(d *Drainer) {
		defer wg.Done()
		for {
			done, err := d.RunOnce(ctx)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			if !done {
				return
			}
		}
	}
	wg.Add(2)
	go drain(dA)
	go drain(dB)
	wg.Wait()

	require.Empty(t, errs)
	for _, a := range addrs {
		assert.Equal(t, 1, stub.called(a),
			"%s processed more than once → double claim", MaskAddr(a))
	}
	assert.Equal(t, int64(len(addrs)), stub.headCalls.Load(),
		"one head call per claimed item")
	var doneRows int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM warm_queue WHERE status='done'`).Scan(&doneRows))
	assert.Equal(t, len(addrs), doneRows)
}

// Lease expiry (contract + D10): a claim older than the lease counts as a
// failed attempt and returns to pending; the same cycle then drains it.
func TestDrainer_LeaseExpiry(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	stub.head = 100
	lane := newLane(t, pool, db.New(pool), stub, net)
	d := newDrainer(t, pool, []network.Network{net}, []ProviderLane{lane},
		nil, testCfg())

	id := insertWarmItem(t, pool, net.ID, addrW1, PrioritySanctionsMatch)
	_, err := pool.Exec(context.Background(),
		`UPDATE warm_queue SET status='claimed',
		 claimed_at = NOW() - interval '20 minutes' WHERE id=$1`, id)
	require.NoError(t, err)

	done, err := d.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	r := warmRow(t, pool, id)
	assert.Equal(t, "done", r.Status)
	assert.Equal(t, int32(1), r.Attempts,
		"the crashed claim counts as an attempt (D10)")

	// Strike 3 at the sweep: attempts=2 + expired claim → dead, and the
	// claim pass finds nothing pending.
	testutil.TruncateAll(t, pool)
	id = insertWarmItem(t, pool, net.ID, addrW2, PrioritySanctionsMatch)
	_, err = pool.Exec(context.Background(),
		`UPDATE warm_queue SET status='claimed', attempts=2,
		 claimed_at = NOW() - interval '20 minutes' WHERE id=$1`, id)
	require.NoError(t, err)
	before := stub.lookCalls.Load()
	done, err = d.RunOnce(context.Background())
	require.NoError(t, err)
	assert.False(t, done)
	r = warmRow(t, pool, id)
	assert.Equal(t, "dead", r.Status)
	assert.Equal(t, int32(3), r.Attempts)
	assert.Equal(t, before, stub.lookCalls.Load(), "a dead sweep spends nothing")
}

// Attempted-but-failed upstream drains bump attempts; three strikes → dead.
// The ledger still records the spent calls.
func TestDrainer_FetchFailureDeadLetter(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	stub.headErr = provider.Errorf(provider.ErrUpstream5xx, "upstream boom")
	// Near-zero FAILED cooldown: each cycle must re-attempt, not serve.
	lane := newLaneNegTTL(t, pool, db.New(pool), stub, net, time.Nanosecond)
	d := newDrainer(t, pool, []network.Network{net}, []ProviderLane{lane},
		nil, testCfg())
	id := insertWarmItem(t, pool, net.ID, addrW1, PriorityRiskyLabel)

	for i, want := range []struct {
		status   string
		attempts int32
	}{{"pending", 1}, {"pending", 2}, {"dead", 3}} {
		done, err := d.RunOnce(context.Background())
		require.NoError(t, err, "cycle %d", i)
		require.True(t, done, "cycle %d", i)
		r := warmRow(t, pool, id)
		assert.Equal(t, want.status, r.Status, "cycle %d", i)
		assert.Equal(t, want.attempts, r.Attempts, "cycle %d", i)
	}
	assert.Equal(t, 3, ledgerCalls(t, pool, "blockscout"),
		"each failed attempt spent its head call")
}

// Infra error (EnsureIndexed returns (nil,err)) → attempts+1, never done.
func TestDrainer_InfraFailure(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	stub.head = 100

	// A lane whose DB pool is closed — the first query inside
	// EnsureIndexed errors → a genuine (nil, err) infra failure.
	ctx := context.Background()
	pool2, err := pgxpool.New(ctx, testDSN)
	require.NoError(t, err)
	lane := newLane(t, pool2, db.New(pool2), stub, net)
	pool2.Close()

	d := newDrainer(t, pool, []network.Network{net}, []ProviderLane{lane},
		nil, testCfg())
	id := insertWarmItem(t, pool, net.ID, addrW1, PriorityRiskyLabel)

	done, err := d.RunOnce(ctx)
	require.NoError(t, err)
	require.True(t, done)
	r := warmRow(t, pool, id)
	assert.Equal(t, "pending", r.Status)
	assert.Equal(t, int32(1), r.Attempts)
	assert.Equal(t, 0, ledgerCalls(t, pool, "blockscout"),
		"infra errors carry no provider-call attribution")
}

// Daily cap (contract): ledger at cap → the cycle releases the item back
// to pending with zero upstream traffic, no attempt bump.
func TestDrainer_LedgerCapSkip(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	stub.head = 100
	lane := newLane(t, pool, db.New(pool), stub, net)
	d := newDrainer(t, pool, []network.Network{net}, []ProviderLane{lane},
		nil, testCfg())
	id := insertWarmItem(t, pool, net.ID, addrW1, PriorityRiskyLabel)

	today := time.Now().UTC().Truncate(24 * time.Hour)
	_, err := pool.Exec(context.Background(),
		`INSERT INTO warm_quota_ledger (provider_code, period_start, calls)
		 VALUES ('blockscout', $1, 200)`, pgtype.Date{Time: today, Valid: true})
	require.NoError(t, err)

	done, err := d.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	r := warmRow(t, pool, id)
	assert.Equal(t, "pending", r.Status)
	assert.Equal(t, int32(0), r.Attempts, "eligibility release never bumps")
	assert.Equal(t, int64(0), stub.headCalls.Load())
	assert.Equal(t, int64(0), stub.lookCalls.Load())
	assert.Equal(t, 200, ledgerCalls(t, pool, "blockscout"), "no spend at cap")
}

// Killswitch (contract): an inactive provider is never attempted — the
// item stays pending and upstream sees zero traffic.
func TestDrainer_KillswitchSkip(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	stub.head = 100
	lane := newLane(t, pool, db.New(pool), stub, net)
	d := newDrainer(t, pool, []network.Network{net}, []ProviderLane{lane},
		checkerFunc(func(context.Context, string) bool { return false }),
		testCfg())
	id := insertWarmItem(t, pool, net.ID, addrW1, PriorityRiskyLabel)

	done, err := d.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	r := warmRow(t, pool, id)
	assert.Equal(t, "pending", r.Status)
	assert.Equal(t, int32(0), r.Attempts)
	assert.Equal(t, int64(0), stub.headCalls.Load())
	assert.Equal(t, int64(0), stub.lookCalls.Load())
}

// A disabled adapter lane is skipped the same way — Enabled() gates
// traffic before the killswitch read.
func TestDrainer_DisabledAdapterSkip(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	stub.enabled = false
	stub.head = 100
	lane := newLane(t, pool, db.New(pool), stub, net)
	d := newDrainer(t, pool, []network.Network{net}, []ProviderLane{lane},
		nil, testCfg())
	id := insertWarmItem(t, pool, net.ID, addrW1, PriorityRiskyLabel)

	done, err := d.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, "pending", warmRow(t, pool, id).Status)
	assert.Equal(t, int64(0), stub.headCalls.Load())
}

// Allowlist order (D1): the first eligible lane spends; a capped first
// lane falls through to the next allowlisted provider.
func TestDrainer_LaneOrderAndCapFallthrough(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	q := db.New(pool)
	pri := newStub("blockscout")
	pri.head = 100
	sec := newStub("blockscout_pro")
	sec.head = 500
	laneA := newLane(t, pool, q, pri, net)
	laneB := newLane(t, pool, q, sec, net)
	d := newDrainer(t, pool, []network.Network{net},
		[]ProviderLane{laneA, laneB}, nil, testCfg())

	// Cap the first lane — the drain must spend the second provider only.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	_, err := pool.Exec(context.Background(),
		`INSERT INTO warm_quota_ledger (provider_code, period_start, calls)
		 VALUES ('blockscout', $1, 500)`, pgtype.Date{Time: today, Valid: true})
	require.NoError(t, err)
	id := insertWarmItem(t, pool, net.ID, addrW1, PriorityRiskyLabel)

	done, err := d.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, "done", warmRow(t, pool, id).Status)
	assert.Equal(t, int64(0), pri.headCalls.Load(), "capped lane untouched")
	assert.Equal(t, int64(1), sec.headCalls.Load())
	assert.Equal(t, 2, ledgerCalls(t, pool, "blockscout_pro"),
		"head + one page attributed to the spending lane")
	assert.Equal(t, 500, ledgerCalls(t, pool, "blockscout"))
}

// A2 regression: the input is by construction a just-indexed
// partial+fresh row (interactive MaxPagesHopN=2 left it partial). Without
// Refresh the freshness gate would serve it and nothing would deepen —
// the drain MUST make real upstream calls.
func TestDrainer_RefreshDeepensFreshPartial(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	stub.head = 1000
	stub.pages[addrW1] = [][]provider.Transfer{{
		{TxHash: "0xdd1", Stream: "txlist", Type: provider.TransferKindNative,
			From: addrW2, To: addrW1, AmountRaw: "5", BlockNumber: 900,
			BlockTimestamp: time.Unix(1700000000, 0).UTC(), Confirmations: 5},
	}}
	lane := newLane(t, pool, db.New(pool), stub, net)
	d := newDrainer(t, pool, []network.Network{net}, []ProviderLane{lane},
		nil, testCfg())
	id := insertWarmItem(t, pool, net.ID, addrW1, PriorityRiskyLabel)

	// Seed the state exactly as the interactive lane left it.
	var wid int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO wallets (network_id, address, normalized_address, address_type)
		 VALUES ($1, $2, $2, 'evm') RETURNING id`, net.ID, addrW1).Scan(&wid))
	_, err := pool.Exec(context.Background(),
		`INSERT INTO wallet_index_state
		 (network_id, wallet_id, source_id, status, indexed_at,
		  indexed_until_block, history_truncated)
		 VALUES ($1, $2, (SELECT id FROM data_sources WHERE code='blockscout'),
		         'partial', NOW(), 100, true)`, net.ID, wid)
	require.NoError(t, err)

	done, err := d.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	assert.GreaterOrEqual(t, stub.lookCalls.Load(), int64(1),
		"fresh partial row must be deepened, not served")
	assert.Equal(t, "done", warmRow(t, pool, id).Status)
	assert.Equal(t, 2, ledgerCalls(t, pool, "blockscout"),
		"head + one page")
}

// Cooldown parity: a FAILED row inside NegativeTTL is served without
// traffic (ProviderCalls=0) → the item releases pending, never bumps.
func TestDrainer_CooldownFailedNoTraffic(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	stub.head = 100
	lane := newLane(t, pool, db.New(pool), stub, net)
	d := newDrainer(t, pool, []network.Network{net}, []ProviderLane{lane},
		nil, testCfg())
	id := insertWarmItem(t, pool, net.ID, addrW1, PriorityRiskyLabel)

	var wid int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO wallets (network_id, address, normalized_address, address_type)
		 VALUES ($1, $2, $2, 'evm') RETURNING id`, net.ID, addrW1).Scan(&wid))
	_, err := pool.Exec(context.Background(),
		`INSERT INTO wallet_index_state
		 (network_id, wallet_id, source_id, status, indexed_at, error)
		 VALUES ($1, $2, (SELECT id FROM data_sources WHERE code='blockscout'),
		         'failed', NOW(), 'upstream_5xx')`, net.ID, wid)
	require.NoError(t, err)

	done, err := d.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	r := warmRow(t, pool, id)
	assert.Equal(t, "pending", r.Status)
	assert.Equal(t, int32(0), r.Attempts)
	assert.Equal(t, int64(0), stub.headCalls.Load())
	assert.Equal(t, int64(0), stub.lookCalls.Load())
	assert.Equal(t, 0, ledgerCalls(t, pool, "blockscout"))
}

// An item whose network_id is real but outside this drainer's resolver is
// permanently unprocessable → dead, never retried.
func TestDrainer_UnresolvableNetworkDead(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	eth := netByCode(t, pool, "ethereum")
	bsc := netByCode(t, pool, "bsc")
	stub := newStub("blockscout")
	stub.head = 100
	lane := newLane(t, pool, db.New(pool), stub, eth)
	d := newDrainer(t, pool, []network.Network{eth}, // bsc not resolvable here
		[]ProviderLane{lane}, nil, testCfg())
	id := insertWarmItem(t, pool, bsc.ID, addrW1, PriorityRiskyLabel)

	done, err := d.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, "dead", warmRow(t, pool, id).Status)
	assert.Equal(t, int64(0), stub.headCalls.Load())
}

// SIGTERM mid-drain (D9 + graceful shutdown): the parent ctx dies inside
// EnsureIndexed — the writeTerminal on the dead child ctx surfaces a
// (nil, err) infra failure — and settle must release the claim under a
// detached ctx. The item returns to pending with NO attempt bump and a
// later cycle drains it normally.
func TestDrainer_ShutdownReleasesClaim(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	stub.head = 100
	// Tiny StaleAfter: the aborted first drain leaves a wallet_index_state
	// 'indexing' claim; the follow-up cycle must see it as crashed, not
	// serve it as live.
	lane := newLaneStale(t, pool, db.New(pool), stub, net, time.Nanosecond)
	d := newDrainer(t, pool, []network.Network{net}, []ProviderLane{lane},
		nil, testCfg())
	id := insertWarmItem(t, pool, net.ID, addrW1, PriorityRiskyLabel)

	pctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub.headHook = func(ctx context.Context) {
		cancel()     // SIGTERM lands mid-drain…
		<-ctx.Done() // …and the drain ctx is guaranteed dead too
	}

	done, err := d.RunOnce(pctx)
	require.Error(t, err, "the cancelled drain surfaces ctx.Err()")
	require.True(t, done)
	r := warmRow(t, pool, id)
	assert.Equal(t, "pending", r.Status,
		"detached release must return the claim, not strand it")
	assert.Equal(t, int32(0), r.Attempts,
		"shutdown is not a failed attempt — no unearned strike")

	// Not stranded: a live cycle completes the same item.
	stub.headHook = nil
	done, err = d.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	assert.Equal(t, "done", warmRow(t, pool, id).Status)
}

// Disabled flag → Run exits immediately (contract §7).
func TestDrainer_DisabledRunExits(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	net := netByCode(t, pool, "ethereum")
	stub := newStub("blockscout")
	lane := newLane(t, pool, db.New(pool), stub, net)
	cfg := testCfg()
	cfg.Enabled = false
	d := newDrainer(t, pool, []network.Network{net}, []ProviderLane{lane},
		nil, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	d.Run(ctx) // must return immediately, not wait for ctx
	assert.Less(t, time.Since(start), 2*time.Second)
}
