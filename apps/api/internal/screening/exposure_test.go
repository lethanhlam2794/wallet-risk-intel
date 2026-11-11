package screening

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/enrichment"
	"checkwallet/internal/scoring"
)

type fakeStore struct {
	in        []db.ListConfirmedCounterpartyTransfersRow
	out       []db.ListConfirmedCounterpartyTransfersRow
	recv      []db.ListConfirmedInboundToWalletsRow
	labels    []db.BulkMatchLabelsRow
	sanc      []db.BulkMatchSanctionedAddressesRow
	states    []db.WalletIndexState
	errIn     error
	errStates error
	errRecv   error
	calls     map[string]int
}

func (f *fakeStore) bump(k string) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[k]++
}
func (f *fakeStore) ListConfirmedCounterpartyTransfers(_ context.Context,
	a db.ListConfirmedCounterpartyTransfersParams) (
	[]db.ListConfirmedCounterpartyTransfersRow, error) {
	f.bump("scan:" + a.Direction)
	if a.Direction == "in" {
		return f.in, f.errIn
	}
	return f.out, nil
}
func (f *fakeStore) ListConfirmedInboundToWallets(_ context.Context,
	p db.ListConfirmedInboundToWalletsParams) (
	[]db.ListConfirmedInboundToWalletsRow, error) {
	f.bump("recv")
	if f.errRecv != nil {
		return nil, f.errRecv
	}
	// Faithful to the generated query: rows filtered by WalletIds.
	var out []db.ListConfirmedInboundToWalletsRow
	for _, r := range f.recv {
		for _, id := range p.WalletIds {
			if r.ReceiverWalletID.Valid && r.ReceiverWalletID.Int64 == id {
				out = append(out, r)
				break
			}
		}
	}
	return out, nil
}
func (f *fakeStore) BulkMatchLabels(_ context.Context,
	_ db.BulkMatchLabelsParams) ([]db.BulkMatchLabelsRow, error) {
	f.bump("labels")
	return f.labels, nil
}
func (f *fakeStore) BulkMatchSanctionedAddresses(_ context.Context,
	_ db.BulkMatchSanctionedAddressesParams) (
	[]db.BulkMatchSanctionedAddressesRow, error) {
	f.bump("sanc")
	return f.sanc, nil
}
func (f *fakeStore) ListWalletIndexStates(_ context.Context,
	p db.ListWalletIndexStatesParams) ([]db.WalletIndexState, error) {
	f.bump("states")
	if f.errStates != nil {
		return nil, f.errStates
	}
	var out []db.WalletIndexState
	for _, s := range f.states {
		for _, id := range p.WalletIds {
			if s.WalletID == id {
				out = append(out, s)
				break
			}
		}
	}
	return out, nil
}

type fakePrices struct{}

func (fakePrices) IsMeaningful(_, asset, amt string) (bool, bool, error) {
	if asset != "native" {
		return false, false, nil // unpriced
	}
	if amt == "bad" {
		return false, true, errors.New("malformed")
	}
	return amt != "1", true, nil // amount "1" = below floor
}

func row(addr string, wid int64, asset, amt, hash string) db.ListConfirmedCounterpartyTransfersRow {
	return db.ListConfirmedCounterpartyTransfersRow{
		TxHash: hash, TransferIndex: "0", TransferType: "NATIVE",
		AmountRaw: amt, CounterpartyAddress: addr,
		CounterpartyWalletID: wid,
		AssetIdentifier:      pgtype.Text{String: asset, Valid: asset != ""},
	}
}
func recvRow(sender string, recvID int64, amt, hash string) db.ListConfirmedInboundToWalletsRow {
	return db.ListConfirmedInboundToWalletsRow{
		TxHash: hash, TransferType: "NATIVE", AmountRaw: amt,
		ReceiverWalletID:    pgtype.Int8{Int64: recvID, Valid: true},
		CounterpartyAddress: sender}
}
func lbl(addr, cat, status string) db.BulkMatchLabelsRow {
	return db.BulkMatchLabelsRow{NormalizedAddress: addr, Label: cat,
		CategoryCode: cat, VerificationStatus: status}
}
func st(wid int64, status string, trunc bool) db.WalletIndexState {
	return stAt(wid, status, trunc, time.Now())
}
func stAt(wid int64, status string, trunc bool,
	at time.Time) db.WalletIndexState {
	return db.WalletIndexState{WalletID: wid, Status: status,
		HistoryTruncated: trunc,
		IndexedAt:        pgtype.Timestamptz{Time: at, Valid: true}}
}
func lblRow(addr, cat, status string) db.BulkMatchLabelsRow {
	return db.BulkMatchLabelsRow{NormalizedAddress: addr,
		CategoryCode: cat, VerificationStatus: status}
}

func newAnalyzer(fs *fakeStore) *exposureAnalyzer {
	return &exposureAnalyzer{store: fs, prices: fakePrices{},
		freshTTL: time.Hour, now: time.Now}
}

func TestAnalyze_DirectTaintedSource(t *testing.T) {
	fs := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xsrc", 11, "", "200", "0xt1"),
			row("0xsrc", 11, "", "200", "0xt2")},
		labels: []db.BulkMatchLabelsRow{lblRow("0xsrc", "SCAM", "reported")},
		states: []db.WalletIndexState{st(1, "fresh", false), st(11, "fresh", false)},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	require.Len(t, out.findings, 2, "exposure + proceeds_receiver")
	f := out.findings[0]
	assert.Equal(t, scoring.KindExposure, f.Kind)
	assert.Equal(t, scoring.BasisOnchainComputed, f.Basis)
	assert.Equal(t, "0xsrc", f.SourceAddress)
	assert.Equal(t, "INBOUND", f.Direction)
	assert.Equal(t, 2, f.MeaningfulCount)
	assert.Equal(t, "direct", f.TaintKind)
	assert.Equal(t, "reported", f.VerificationStatus)
	assert.Equal(t, scoring.KindProceedsReceiver, out.findings[1].Kind)
	assert.Equal(t, "complete", out.onchainCoverage)
	assert.Equal(t, 0, out.sourcesReceiverUnchecked)
	assert.LessOrEqual(t, len(fs.calls), 8)
}

func TestAnalyze_BelowFloorAndUnpriced(t *testing.T) {
	in := []db.ListConfirmedCounterpartyTransfersRow{
		row("0xlow", 11, "", "1", "0xl1"), // priced, below floor
	}
	unpr := db.ListConfirmedCounterpartyTransfersRow{
		TxHash: "0xu1", TransferType: "EXTERNAL", AmountRaw: "999",
		CounterpartyAddress: "0xunp", CounterpartyWalletID: int64(12),
		AssetIdentifier: pgtype.Text{String: "0xtok", Valid: true}}
	fs := &fakeStore{
		in: append(in, unpr),
		labels: []db.BulkMatchLabelsRow{
			lblRow("0xlow", "SCAM", "verified"), lblRow("0xunp", "HACK", "reported")},
		states: []db.WalletIndexState{st(1, "fresh", false),
			st(11, "fresh", false), st(12, "fresh", false)},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	require.Len(t, out.findings, 2)
	assert.Equal(t, "below_materiality_floor", out.findings[0].Reason)
	assert.Equal(t, "unpriced_asset", out.findings[1].Reason)
}

func TestAnalyze_ChainTaintOneLevel(t *testing.T) {
	fs := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xrecv", 11, "", "200", "0xt1")},
		recv: []db.ListConfirmedInboundToWalletsRow{
			recvRow("0xbad", 11, "200", "0xd1")},
		labels: []db.BulkMatchLabelsRow{lblRow("0xbad", "RANSOMWARE", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false), st(11, "fresh", false)},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	require.Len(t, out.findings, 1, "chain taint only → exposure, NO proceeds")
	f := out.findings[0]
	assert.Equal(t, "chain", f.TaintKind)
	assert.Equal(t, "RANSOMWARE", f.Category, "sender taint carries through")
	assert.Equal(t, "verified", f.VerificationStatus)

	// Receiver-of-receiver does NOT propagate: the sender is only
	// chain-tainted (it has a tainted sender itself? no — sender is a
	// depth-2 addr evaluated DIRECT-only; give 0xbad no direct taint
	// and assert 0xrecv stays untainted).
	fs2 := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xrecv", 11, "", "200", "0xt1")},
		recv: []db.ListConfirmedInboundToWalletsRow{
			recvRow("0xmid", 11, "200", "0xd1")},
		labels: []db.BulkMatchLabelsRow{lblRow("0xother", "SCAM", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false), st(11, "fresh", false)},
	}
	out2 := newAnalyzer(fs2).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Empty(t, out2.findings, "untainted sender → no exposure")
}

func TestAnalyze_DisputedAndService(t *testing.T) {
	fs := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xdisp", 11, "", "200", "0xt1"),
			row("0xexch", 12, "", "200", "0xt2")},
		labels: []db.BulkMatchLabelsRow{
			lblRow("0xdisp", "SCAM", "disputed"),
			lblRow("0xexch", "EXCHANGE", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false),
			st(11, "fresh", false), st(12, "fresh", false)},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Empty(t, out.findings,
		"disputed never taints; service-classified source is never a source")
}

func TestAnalyze_CandidateServiceClassified(t *testing.T) {
	fs := &fakeStore{
		labels: []db.BulkMatchLabelsRow{lblRow("0xcand", "MIXER", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false)},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.True(t, out.serviceClassified)
}

func TestAnalyze_OutboundVictimContact(t *testing.T) {
	fs := &fakeStore{
		out: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xvict", 0, "", "5", "0xo1")},
		labels: []db.BulkMatchLabelsRow{lblRow("0xvict", "DRAINER", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false)},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	require.Len(t, out.findings, 1)
	f := out.findings[0]
	assert.Equal(t, scoring.KindPossibleVictimContact, f.Kind)
	assert.Equal(t, "OUTBOUND", f.Direction)
}

func TestAnalyze_CoverageAbsentAndUnchecked(t *testing.T) {
	// No candidate index-state rows → absent + degraded status flag.
	fs := &fakeStore{}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Equal(t, "absent", out.onchainCoverage)

	// Source with no index state → receiver-check unverifiable.
	fs2 := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xsrc", 11, "", "200", "0xt1")},
		labels: []db.BulkMatchLabelsRow{lblRow("0xsrc", "HACK", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false)},
	}
	out2 := newAnalyzer(fs2).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Equal(t, "partial", out2.onchainCoverage)
	assert.Equal(t, 1, out2.sourcesReceiverUnchecked)
}

func TestAnalyze_QueryErrorDegrades(t *testing.T) {
	fs := &fakeStore{errIn: errors.New("boom")}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.True(t, out.degraded)
	assert.Equal(t, "partial", out.onchainCoverage)
}

// --- Remediation round 1 ---

// Finding #2: a service-classified address that is ALSO direct-tainted
// or sanctioned stays a source — service labels never erase taint.
func TestAnalyze_ServiceClassifiedStillTainted(t *testing.T) {
	fs := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xmixer", 11, "", "200", "0xt1")},
		labels: []db.BulkMatchLabelsRow{
			lblRow("0xmixer", "MIXER", "verified"),
			lblRow("0xmixer", "HACK", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false),
			st(11, "fresh", false)},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	require.NotEmpty(t, out.findings, "HACK+MIXER source must score")
	assert.Equal(t, "HACK", out.findings[0].Category)

	// Sanctioned exchange (Tornado-Cash case) — still a source.
	fs2 := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xexch", 11, "", "200", "0xt1")},
		labels: []db.BulkMatchLabelsRow{
			lblRow("0xexch", "EXCHANGE", "verified")},
		sanc: []db.BulkMatchSanctionedAddressesRow{
			{NormalizedAddress: "0xexch"}},
		states: []db.WalletIndexState{st(1, "fresh", false),
			st(11, "fresh", false)},
	}
	out2 := newAnalyzer(fs2).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	require.NotEmpty(t, out2.findings, "sanctioned service is a source")
	assert.Equal(t, "SANCTIONED", out2.findings[0].Category)
	assert.Equal(t, "verified", out2.findings[0].VerificationStatus)

	// Sanctions outranks a lower-weight label (FROZEN=60 < 85) — A13.
	fs3 := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xfr", 11, "", "200", "0xt1")},
		labels: []db.BulkMatchLabelsRow{
			lblRow("0xfr", "FROZEN", "reported")},
		sanc: []db.BulkMatchSanctionedAddressesRow{
			{NormalizedAddress: "0xfr"}},
		states: []db.WalletIndexState{st(1, "fresh", false),
			st(11, "fresh", false)},
	}
	out3 := newAnalyzer(fs3).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	require.NotEmpty(t, out3.findings)
	assert.Equal(t, "SANCTIONED", out3.findings[0].Category,
		"highest base-weight evidence wins, not first-found")
}

// Finding #3: index-state query ERROR → partial, never absent. A failed
// probe cannot prove a no-index condition.
func TestAnalyze_IndexStatesErrorIsPartial(t *testing.T) {
	fs := &fakeStore{errStates: errors.New("boom")}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Equal(t, "partial", out.onchainCoverage)
	assert.True(t, out.degraded)
}

// Finding #4 coverage cases: stale FreshTTL, recv-leg error, scan-limit,
// source cap, depth-2 cap — all must yield partial coverage.
func TestAnalyze_CoveragePartialCases(t *testing.T) {
	// Stale index row → source receiver-check unverifiable → partial.
	fs := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xsrc", 11, "", "200", "0xt1")},
		labels: []db.BulkMatchLabelsRow{lblRow("0xsrc", "HACK", "verified")},
		states: []db.WalletIndexState{
			stAt(1, "fresh", false, time.Now()),
			stAt(11, "fresh", false, time.Now().Add(-2*time.Hour))},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Equal(t, "partial", out.onchainCoverage, "stale source index")
	assert.Equal(t, 1, out.sourcesReceiverUnchecked)

	// NULL indexed_at: a fresh row cannot prove freshness (nit #15).
	fsN := &fakeStore{
		states: []db.WalletIndexState{{WalletID: 1, Status: "fresh"}},
	}
	outN := newAnalyzer(fsN).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Equal(t, "partial", outN.onchainCoverage, "NULL indexed_at")

	// recv-leg error → partial + degraded.
	fsR := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xsrc", 11, "", "200", "0xt1")},
		labels:  []db.BulkMatchLabelsRow{lblRow("0xsrc", "HACK", "verified")},
		states:  []db.WalletIndexState{st(1, "fresh", false), st(11, "fresh", false)},
		errRecv: errors.New("boom"),
	}
	outR := newAnalyzer(fsR).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Equal(t, "partial", outR.onchainCoverage)
	assert.True(t, outR.degraded)

	// Scan-limit: exactly 5000 inbound rows → partial.
	big := make([]db.ListConfirmedCounterpartyTransfersRow,
		scoring.ExposureScanLimitV1_1)
	for i := range big {
		big[i] = row("0xsrc", 11, "", "200", "0xt")
	}
	fsL := &fakeStore{in: big,
		labels: []db.BulkMatchLabelsRow{lblRow("0xsrc", "HACK", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false), st(11, "fresh", false)}}
	outL := newAnalyzer(fsL).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Equal(t, "partial", outL.onchainCoverage, "scan-limit hit")

	// Source cap: 257 distinct sources → partial.
	many := make([]db.ListConfirmedCounterpartyTransfersRow,
		scoring.ExposureMaxSourcesV1_1+1)
	lbls := []db.BulkMatchLabelsRow{}
	for i := range many {
		a := "0xsrc" + strconv.Itoa(i)
		many[i] = row(a, 0, "", "200", "0xt")
		lbls = append(lbls, lblRow(a, "SCAM", "verified"))
	}
	fsC := &fakeStore{in: many, labels: lbls,
		states: []db.WalletIndexState{st(1, "fresh", false)}}
	outC := newAnalyzer(fsC).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Equal(t, "partial", outC.onchainCoverage, "source cap")
	assert.Equal(t, scoring.ExposureMaxSourcesV1_1, outC.sourcesEvaluated)

	// Depth-2 cap: >512 distinct senders to one receiver → partial.
	senders := make([]db.ListConfirmedInboundToWalletsRow,
		scoring.ExposureMaxDepth2SendersV1_1+1)
	for i := range senders {
		senders[i] = recvRow("0xs"+strconv.Itoa(i), 11, "200", "0xd")
	}
	fsD := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xrecv", 11, "", "200", "0xt1")},
		recv:   senders,
		states: []db.WalletIndexState{st(1, "fresh", false), st(11, "fresh", false)},
	}
	outD := newAnalyzer(fsD).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Equal(t, "partial", outD.onchainCoverage, "depth-2 sender cap")
}

// Nit #16: a non-NATIVE transfer with NULL asset_identifier is
// unassessable — never priced as the chain coin.
func TestAnalyze_NullAssetIDNotNative(t *testing.T) {
	fs := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{{
			TxHash: "0xt1", TransferType: "EXTERNAL", AmountRaw: "5",
			CounterpartyAddress: "0xsrc", CounterpartyWalletID: int64(11)}},
		labels: []db.BulkMatchLabelsRow{lblRow("0xsrc", "HACK", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false), st(11, "fresh", false)},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	require.Len(t, out.findings, 1)
	assert.Equal(t, 0, out.findings[0].MeaningfulCount)
	assert.Equal(t, "unpriced_asset", out.findings[0].Reason)
}

// Nit #17: malformed amounts are NOT "unpriced_asset" evidence.
func TestAnalyze_MalformedAmountReason(t *testing.T) {
	fs := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xsrc", 11, "", "bad", "0xt1")},
		labels: []db.BulkMatchLabelsRow{lblRow("0xsrc", "HACK", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false), st(11, "fresh", false)},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	require.Len(t, out.findings, 1)
	assert.Equal(t, "unassessable_amount", out.findings[0].Reason)
}

// A1 mapper-level: a provider OUTBOUND exposure maps to
// possible_victim_contact, never scores, and the wire item carries the
// normalized vocabulary (finding #9).
func TestMapper_ProviderOutboundIsVictimContact(t *testing.T) {
	g := &gathered{enrich: &enrichment.Result{Providers: []enrichment.ProviderResult{{
		Source: "goplus", Status: "OK",
		Exposures: []enrichment.Exposure{{
			Counterparty: "0x0000000000000000000000000000000000000bad",
			Category:     "SCAM", Hops: 1, Direction: "outbound"}},
	}}}}
	m := mapGathered(g, DefaultOptions(), nil, scoring.PolicyV1_1)
	require.Len(t, m.exposures.Items, 1)
	it := m.exposures.Items[0]
	assert.Equal(t, "OUTBOUND", it.Direction, "normalized vocabulary")
	assert.False(t, it.Scored)
	var found bool
	for _, f := range m.findings {
		if f.Kind == scoring.KindPossibleVictimContact {
			found = true
			assert.Equal(t, "OUTBOUND", f.Direction)
		}
	}
	assert.True(t, found, "provider outbound → victim-contact finding")
}

// Finding #12: an evaluated on-chain leg makes the exposure dimension
// usable even when no provider reported.
func TestMapper_OnchainLegMakesExposureUsable(t *testing.T) {
	g := &gathered{onchain: &onchainExposure{
		evaluated: true, onchainCoverage: "complete",
	}}
	m := mapGathered(g, DefaultOptions(), nil, scoring.PolicyV1_1)
	assert.True(t, m.completeness.Exposure.Usable)
	assert.Equal(t, DataAvailable, m.completeness.Exposure.DataStatus)

	// A failed on-chain leg (not evaluated) with no provider → unusable.
	g2 := &gathered{onchain: &onchainExposure{
		evaluated: false, onchainCoverage: "partial", degraded: true}}
	m2 := mapGathered(g2, DefaultOptions(), nil, scoring.PolicyV1_1)
	assert.False(t, m2.completeness.Exposure.Usable)
	assert.Equal(t, DataUnavailable, m2.completeness.Exposure.DataStatus)
}

// --- Remediation round 2 ---

// Round-2 major: a service-classified source with ONLY chain taint is
// NOT a source — chain taint through Binance must not mass-flag its
// recipients. (Direct-tainted/sanctioned services still source — see
// TestAnalyze_ServiceClassifiedStillTainted.)
func TestAnalyze_ServiceSourceChainTaintExcluded(t *testing.T) {
	fs := &fakeStore{
		in: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xexch", 11, "", "200", "0xt1")},
		recv: []db.ListConfirmedInboundToWalletsRow{
			recvRow("0xbad", 11, "200", "0xd1")},
		labels: []db.BulkMatchLabelsRow{
			lblRow("0xexch", "EXCHANGE", "verified"),
			lblRow("0xbad", "RANSOMWARE", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false),
			st(11, "fresh", false)},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Empty(t, out.findings,
		"chain-tainted service must not emit a source finding")
}

// Round-2 nit: an outbound counterparty that is chain-tainted (received
// meaningful inbound from a direct-tainted wallet) IS a
// possible_victim_contact — spec §3.1 "tainted" includes chain.
func TestAnalyze_ChainTaintedVictimContact(t *testing.T) {
	fs := &fakeStore{
		out: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xvict", 12, "", "5", "0xo1")},
		recv: []db.ListConfirmedInboundToWalletsRow{
			recvRow("0xbad", 12, "200", "0xd1")},
		labels: []db.BulkMatchLabelsRow{lblRow("0xbad", "SCAM", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false),
			st(12, "fresh", false)},
	}
	out := newAnalyzer(fs).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	require.Len(t, out.findings, 1, "chain-tainted outbound cp → contact")
	f := out.findings[0]
	assert.Equal(t, scoring.KindPossibleVictimContact, f.Kind)
	assert.Equal(t, "chain", f.TaintKind)
	assert.Equal(t, "SCAM", f.Category)

	// A service-only cp with chain taint stays excluded — paying a
	// chain-tainted exchange is not victim evidence per §3.1 intent.
	fs2 := &fakeStore{
		out: []db.ListConfirmedCounterpartyTransfersRow{
			row("0xexch", 12, "", "5", "0xo1")},
		recv: []db.ListConfirmedInboundToWalletsRow{
			recvRow("0xbad", 12, "200", "0xd1")},
		labels: []db.BulkMatchLabelsRow{
			lblRow("0xexch", "EXCHANGE", "verified"),
			lblRow("0xbad", "SCAM", "verified")},
		states: []db.WalletIndexState{st(1, "fresh", false),
			st(12, "fresh", false)},
	}
	out2 := newAnalyzer(fs2).analyze(context.Background(), 1, "ethereum", 1, "0xcand")
	assert.Empty(t, out2.findings,
		"service-only cp requires direct taint even outbound")
}
