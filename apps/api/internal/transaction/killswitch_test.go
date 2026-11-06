package transaction

// Runtime kill-switch tests (db-provider-config phase 3):
// provider_configs.status is consulted per EnsureIndexed through a
// TTL-cached checker — inactive members are skipped through the exact
// disabled-skip path: zero upstream traffic, never a failover trigger,
// all-inactive ⇒ disabled verdict under chain[0]'s source.

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/provider"
	"checkwallet/internal/providerconfig"
	"checkwallet/internal/testutil"
	"checkwallet/internal/wallet"
)

type stubStatusChecker struct {
	mu     sync.Mutex
	active map[string]bool
}

func newStubChecker() *stubStatusChecker {
	return &stubStatusChecker{active: map[string]bool{}}
}

func (s *stubStatusChecker) set(code string, active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[code] = active
}

func (s *stubStatusChecker) Active(_ context.Context, code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.active[code]
	return !ok || v // missing ⇒ active (env parity)
}

// Inactive member is skipped through the disabled path — zero upstream
// calls, no state row touched, later members still serve.
func TestKillSwitch_InactiveMemberSkipped(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	sec := newStub("nodereal")
	sec.head = 500
	sec.pages[addrRoot] = [][]provider.Transfer{{
		tf("tx", provider.TransferKindNative, "0xfa1", addrA, addrRoot, "", "7", 495, false),
	}}
	svc, _, net := newSvcChain(t, pool, []*stubTx{pri, sec},
		map[string]int{"chainscan": 8, "nodereal": 8})
	checker := newStubChecker()
	checker.set("chainscan", false)
	svc.SetStatusChecker(checker)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	assert.Equal(t, int64(0), pri.headCalls.Load(), "inactive ⇒ zero upstream")
	assert.Equal(t, int64(0), pri.lookCalls.Load())
	assert.Equal(t, int64(1), sec.headCalls.Load())
}

// Flip to inactive mid-service ⇒ the next refresh skips without restart;
// flip back ⇒ it serves again.
func TestKillSwitch_FlipAndFlipBack(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	pri.head = 100
	pri.pages[addrRoot] = [][]provider.Transfer{{
		tf("txlist", provider.TransferKindNative, "0xfb1", addrA, addrRoot, "", "3", 90, false),
	}}
	svc, _, net := newSvcChain(t, pool, []*stubTx{pri},
		map[string]int{"chainscan": 8})
	checker := newStubChecker()
	svc.SetStatusChecker(checker)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	assert.Equal(t, int64(1), pri.headCalls.Load())

	// Flip inactive ⇒ next refresh never touches upstream and reports
	// the disabled verdict through the same path as a disabled adapter.
	checker.set("chainscan", false)
	out, err = svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, StatusDisabled, out.IndexStatus)
	assert.Equal(t, int64(1), pri.headCalls.Load(), "no traffic while inactive")
	require.NotNil(t, out.ProviderStatus)
	assert.Equal(t, "disabled", *out.ProviderStatus)

	// Flip back ⇒ serves again on the next refresh — no restart needed.
	checker.set("chainscan", true)
	out, err = svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	assert.Equal(t, int64(2), pri.headCalls.Load())
}

// All members inactive ⇒ the disabled terminal is written under
// chain[0]'s source — the selector's verdict, not a phantom row.
func TestKillSwitch_AllInactiveDisabled(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	sec := newStub("nodereal")
	svc, _, net := newSvcChain(t, pool, []*stubTx{pri, sec},
		map[string]int{"chainscan": 8, "nodereal": 8})
	checker := newStubChecker()
	checker.set("chainscan", false)
	checker.set("nodereal", false)
	svc.SetStatusChecker(checker)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusDisabled, out.IndexStatus)
	require.NotNil(t, out.ProviderStatus)
	assert.Equal(t, "disabled", *out.ProviderStatus)
	wid := walletID(t, pool, net.ID, addrRoot)
	ps := getStateSrc(t, pool, net.ID, wid, "chainscan")
	assert.Equal(t, StatusDisabled, ps.Status)
	assert.Equal(t, "disabled", ps.Error.String)
}

// DB-backed end-to-end: a real StatusCache reading
// provider_configs.status + data_sources.is_active — flipping the row
// takes effect on the next EnsureIndexed without restart.
func TestKillSwitch_DBBackedStatusCache(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	pri := newStub("chainscan")
	pri.head = 100
	pri.pages[addrRoot] = [][]provider.Transfer{{
		tf("txlist", provider.TransferKindNative, "0xfc1", addrA, addrRoot, "", "3", 90, false),
	}}
	svc, q, net := newSvcChain(t, pool, []*stubTx{pri},
		map[string]int{"chainscan": 8})
	// ttl=0 ⇒ every Active() reloads — deterministic flip visibility.
	cache := providerconfig.NewStatusCache(q, 0)
	svc.SetStatusChecker(cache)

	out, err := svc.EnsureIndexed(context.Background(), net, addrRoot, EnsureOptions{})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)

	// Operator flips the switch in the DB.
	_, err = q.UpsertProviderConfig(context.Background(), db.UpsertProviderConfigParams{
		SourceCode: "chainscan", Status: "inactive",
	})
	require.NoError(t, err)

	out, err = svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, StatusDisabled, out.IndexStatus)
	assert.Equal(t, int64(1), pri.headCalls.Load(), "no traffic after flip")

	// Catalog switch also ANDs in: is_active=false ⇒ inactive even with
	// the row active.
	_, err = q.UpsertProviderConfig(context.Background(), db.UpsertProviderConfigParams{
		SourceCode: "chainscan", Status: "active",
	})
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`UPDATE data_sources SET is_active = false WHERE code = 'chainscan'`)
	require.NoError(t, err)
	out, err = svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, StatusDisabled, out.IndexStatus)
	assert.Equal(t, int64(1), pri.headCalls.Load())
	_, err = pool.Exec(context.Background(),
		`UPDATE data_sources SET is_active = true WHERE code = 'chainscan'`)
	require.NoError(t, err)

	// Mid-flight 'indexing' rows under a flipped-inactive source self-heal:
	// the skip leaves the persisted row untouched (never served as fresh).
	w, err := wallet.NewService(q).GetOrCreate(context.Background(),
		net, addrRoot, addrRoot)
	require.NoError(t, err)
	src, err := q.GetSourceByCode(context.Background(), "chainscan")
	require.NoError(t, err)
	require.NoError(t, q.MarkWalletIndexing(context.Background(),
		db.MarkWalletIndexingParams{
			NetworkID: net.ID, WalletID: w.ID, SourceID: src.ID}))
	_, err = q.UpsertProviderConfig(context.Background(), db.UpsertProviderConfigParams{
		SourceCode: "chainscan", Status: "inactive",
	})
	require.NoError(t, err)
	out, err = svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, StatusDisabled, out.IndexStatus)
	assert.Equal(t, int64(1), pri.headCalls.Load())

	// No checker ⇒ nil path preserves env parity (kill switch absent).
	svc.SetStatusChecker(nil)
	_, err = q.UpsertProviderConfig(context.Background(), db.UpsertProviderConfigParams{
		SourceCode: "chainscan", Status: "active",
	})
	require.NoError(t, err)
	out, err = svc.EnsureIndexed(context.Background(), net, addrRoot,
		EnsureOptions{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, StatusFresh, out.IndexStatus)
	assert.Equal(t, int64(2), pri.headCalls.Load())
}
