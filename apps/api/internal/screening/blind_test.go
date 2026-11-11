package screening

// BLIND property suite — randomized analyzer graphs asserting
// invariants, never named acceptance values. BLIND_SEED selects the
// stream; failures print seed + case index for reproduction.

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/scoring"
)

const blindN = 200

func blindSeed(t *testing.T) int64 {
	t.Helper()
	s := os.Getenv("BLIND_SEED")
	if s == "" {
		return 1
	}
	n, err := strconv.ParseInt(s, 10, 64)
	require.NoError(t, err, "BLIND_SEED must be an int64")
	return n
}

func blindFatalf(t *testing.T, seed int64, i int,
	format string, args ...any) {
	t.Helper()
	all := append([]any{seed, i}, args...)
	t.Fatalf("BLIND_SEED=%d case=%d: "+format, all...)
}

// fakePrices contract (see exposure_test.go): native && amt ∉
// {"1","bad"} → meaningful; "1" → priced below floor; "bad" → malformed;
// any non-"native" asset key → unpriced.
func blindMeaningful(tt, amt string) bool {
	return tt == "NATIVE" && amt != "1" && amt != "bad"
}

func blindUnpricedKey(tt, asset string) bool {
	return tt != "NATIVE" && asset == "" // keyOK=false → unpriced
}

type blindAddr struct {
	addr string
	wid  int64
	hasW bool
}

type blindFixture struct {
	in     []db.ListConfirmedCounterpartyTransfersRow
	out    []db.ListConfirmedCounterpartyTransfersRow
	recv   []db.ListConfirmedInboundToWalletsRow
	labels []db.BulkMatchLabelsRow
	sanc   []db.BulkMatchSanctionedAddressesRow
	states []db.WalletIndexState
}

func blindGen(r *rand.Rand) blindFixture {
	var fx blindFixture
	// Address pools — small enough for dedupe pressure, big enough to
	// vary. Sources may share wallet IDs with cps only via coincidence.
	srcs := []blindAddr{}
	for i := 0; i < 7; i++ {
		srcs = append(srcs, blindAddr{
			addr: "0xs" + strconv.Itoa(i), wid: int64(10 + i), hasW: r.Intn(4) > 0})
	}
	cps := []blindAddr{}
	for i := 0; i < 5; i++ {
		cps = append(cps, blindAddr{
			addr: "0xc" + strconv.Itoa(i), wid: int64(30 + i), hasW: r.Intn(4) > 0})
	}
	senders := []string{"0xd0", "0xd1", "0xd2", "0xd3", "0xd4"}
	amts := []string{"1", "200", "bad", "55", "9"}

	pick := func() string { return amts[r.Intn(len(amts))] }
	asset := func() (string, string) {
		switch r.Intn(5) {
		case 0:
			return "EXTERNAL", "0xtok" // unpriced token
		case 1:
			return "EXTERNAL", "" // assetless token → unassessable
		default:
			return "NATIVE", ""
		}
	}
	wid := func(b blindAddr) any {
		if !b.hasW {
			return nil
		}
		return b.wid
	}

	for j := 0; j < r.Intn(14); j++ {
		s := srcs[r.Intn(len(srcs))]
		tt, as := asset()
		fx.in = append(fx.in, db.ListConfirmedCounterpartyTransfersRow{
			TxHash: "0xt" + strconv.Itoa(j), TransferType: tt,
			AmountRaw: pick(), CounterpartyAddress: s.addr,
			CounterpartyWalletID: wid(s),
			AssetIdentifier:      pgtype.Text{String: as, Valid: as != ""},
		})
	}
	for j := 0; j < r.Intn(8); j++ {
		c := cps[r.Intn(len(cps))]
		tt, as := asset()
		fx.out = append(fx.out, db.ListConfirmedCounterpartyTransfersRow{
			TxHash: "0xo" + strconv.Itoa(j), TransferType: tt,
			AmountRaw: pick(), CounterpartyAddress: c.addr,
			CounterpartyWalletID: wid(c),
			AssetIdentifier:      pgtype.Text{String: as, Valid: as != ""},
		})
	}
	// Depth-2 rows: sender → some source/cp wallet ID.
	for j := 0; j < r.Intn(8); j++ {
		var rid int64
		switch r.Intn(3) {
		case 0:
			rid = srcs[r.Intn(len(srcs))].wid
		case 1:
			rid = cps[r.Intn(len(cps))].wid
		default:
			rid = srcs[r.Intn(len(srcs))].wid
		}
		fx.recv = append(fx.recv, db.ListConfirmedInboundToWalletsRow{
			TxHash: "0xd" + strconv.Itoa(j), TransferType: "NATIVE",
			AmountRaw:           pick(),
			ReceiverWalletID:    pgtype.Int8{Int64: rid, Valid: true},
			CounterpartyAddress: senders[r.Intn(len(senders))],
		})
	}

	// Labels over the union of all addresses + candidate.
	cats := []string{"SCAM", "HACK", "RANSOMWARE", "EXCHANGE", "MIXER",
		"GAMBLING", "OTHER", "FROZEN"}
	stats := []string{"verified", "reported", "disputed"}
	pool := []string{"0xcand"}
	for _, s := range srcs {
		pool = append(pool, s.addr)
	}
	for _, c := range cps {
		pool = append(pool, c.addr)
	}
	pool = append(pool, senders...)
	seen := map[string]bool{}
	for _, a := range pool {
		if seen[a] || r.Intn(3) == 0 {
			continue
		}
		seen[a] = true
		fx.labels = append(fx.labels, db.BulkMatchLabelsRow{
			NormalizedAddress:  a,
			CategoryCode:       cats[r.Intn(len(cats))],
			VerificationStatus: stats[r.Intn(len(stats))],
		})
	}
	for _, a := range pool {
		if r.Intn(12) == 0 {
			fx.sanc = append(fx.sanc,
				db.BulkMatchSanctionedAddressesRow{NormalizedAddress: a})
		}
	}

	// Index states: a row for a random subset of wallet IDs; state
	// shape random (status / truncation / timestamp freshness).
	wids := map[int64]bool{1: true}
	for _, s := range srcs {
		wids[s.wid] = true
	}
	for _, c := range cps {
		wids[c.wid] = true
	}
	for id := range wids {
		if r.Intn(3) == 0 {
			continue // no index row for this wallet
		}
		row := db.WalletIndexState{
			WalletID:         id,
			Status:           []string{"fresh", "stale", "partial", "fresh"}[r.Intn(4)],
			HistoryTruncated: r.Intn(6) == 0,
		}
		switch r.Intn(4) {
		case 0:
			row.IndexedAt = pgtype.Timestamptz{Valid: false}
		case 1:
			row.IndexedAt = pgtype.Timestamptz{
				Time: time.Now().Add(-2 * time.Hour), Valid: true}
		default:
			row.IndexedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		}
		fx.states = append(fx.states, row)
	}
	return fx
}

// Recomputed expectations (independent re-derivation of the model).
type blindModel struct {
	order        []string
	meaningful   map[string]int
	txMeaningful map[string]map[string]bool
	srcWID       map[string]int64
	srcHasW      map[string]bool
	direct       map[string]bool
	service      map[string]bool
	chain        map[string]bool
	chainCP      map[string]bool
	unchecked    int
	candUsable   bool
	candRows     int
	srcCap       bool
}

func blindExpect(fx blindFixture, az *exposureAnalyzer,
	errStates, errRecv error) blindModel {
	// Under a query error the analyzer sees an empty result set — the
	// model must see the same.
	if errStates != nil {
		fx.states = nil
	}
	if errRecv != nil {
		fx.recv = nil
	}
	m := blindModel{
		meaningful:   map[string]int{},
		txMeaningful: map[string]map[string]bool{},
		srcWID:       map[string]int64{},
		srcHasW:      map[string]bool{},
		direct:       map[string]bool{},
		service:      map[string]bool{},
		chain:        map[string]bool{},
		chainCP:      map[string]bool{},
	}
	seen := map[string]bool{}
	for _, r := range fx.in {
		a := r.CounterpartyAddress
		if a == "" {
			continue
		}
		if !seen[a] {
			if len(m.order) >= scoring.ExposureMaxSourcesV1_1 {
				m.srcCap = true
				continue
			}
			seen[a] = true
			m.order = append(m.order, a)
			// Wallet ID binds at group creation — first row only.
			if id, ok := r.CounterpartyWalletID.(int64); ok {
				m.srcWID[a], m.srcHasW[a] = id, true
			}
		}
		if r.TransferType != "NATIVE" && r.AssetIdentifier.String == "" {
			continue // assetless token row — never meaningful
		}
		if blindMeaningful(r.TransferType, r.AmountRaw) {
			m.meaningful[a]++
			if m.txMeaningful[a] == nil {
				m.txMeaningful[a] = map[string]bool{}
			}
			m.txMeaningful[a][r.TxHash] = true
		}
	}
	labelOf := map[string][]db.BulkMatchLabelsRow{}
	for _, l := range fx.labels {
		labelOf[l.NormalizedAddress] = append(labelOf[l.NormalizedAddress], l)
	}
	sancOf := map[string]bool{}
	for _, s := range fx.sanc {
		sancOf[s.NormalizedAddress] = true
	}
	for a, rows := range labelOf {
		for _, l := range rows {
			cat := l.CategoryCode
			if l.VerificationStatus == "disputed" {
				continue
			}
			if scoring.ServiceClassificationV1_1(cat) {
				m.service[a] = true
			}
			if scoring.DirectTaintCategoryV1_1(cat) {
				m.direct[a] = true
			}
		}
		if sancOf[a] {
			m.direct[a] = true
		}
	}
	for a := range sancOf {
		m.direct[a] = true
	}

	usableWid := func(wid int64) bool {
		for _, s := range fx.states {
			if s.WalletID != wid || s.Status != "fresh" || s.HistoryTruncated {
				continue
			}
			if !s.IndexedAt.Valid {
				continue
			}
			if time.Since(s.IndexedAt.Time) > az.freshTTL && az.freshTTL > 0 {
				continue
			}
			return true
		}
		return false
	}
	for _, s := range fx.states {
		if s.WalletID == 1 {
			m.candRows++
		}
	}
	m.candUsable = usableWid(1)

	// Chain: receiver wid has a meaningful inbound row from a
	// direct-tainted sender.
	chainFor := func(wid int64) bool {
		for _, rr := range fx.recv {
			if !rr.ReceiverWalletID.Valid ||
				rr.ReceiverWalletID.Int64 != wid {
				continue
			}
			if !blindMeaningful(rr.TransferType, rr.AmountRaw) {
				continue
			}
			if m.direct[rr.CounterpartyAddress] {
				return true
			}
		}
		return false
	}
	for _, a := range m.order {
		if !m.srcHasW[a] || !usableWid(m.srcWID[a]) {
			m.unchecked++
			continue
		}
		if !m.service[a] && chainFor(m.srcWID[a]) {
			m.chain[a] = true
		}
	}
	// Outbound-only cps with wallet IDs join the receiver check.
	for _, r := range fx.out {
		a := r.CounterpartyAddress
		if a == "" || seen[a] {
			continue
		}
		id, ok := r.CounterpartyWalletID.(int64)
		if !ok {
			continue
		}
		if usableWid(id) && chainFor(id) {
			m.chainCP[a] = true
		}
	}
	return m
}

func TestBlind_Analyzer_Invariants(t *testing.T) {
	seed := blindSeed(t)
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < blindN; i++ {
		fx := blindGen(r)
		fs := &fakeStore{
			in: fx.in, out: fx.out, recv: fx.recv,
			labels: fx.labels, sanc: fx.sanc, states: fx.states,
		}
		if r.Intn(10) == 0 {
			fs.errStates = errors.New("blind states err")
		}
		if r.Intn(10) == 0 {
			fs.errRecv = errors.New("blind recv err")
		}
		if r.Intn(12) == 0 {
			fs.errIn = errors.New("blind in err")
		}
		az := newAnalyzer(fs)
		out := az.analyze(context.Background(), 1, "ethereum", 1, "0xcand")
		m := blindExpect(fx, az, fs.errStates, fs.errRecv)

		// Coverage vocabulary + honesty.
		switch out.onchainCoverage {
		case "complete", "partial", "absent":
		default:
			blindFatalf(t, seed, i, "coverage %q", out.onchainCoverage)
		}
		if fs.errIn != nil {
			if out.onchainCoverage != "partial" || !out.degraded {
				blindFatalf(t, seed, i, "in-scan error → degraded partial")
			}
			continue // early return — nothing else emitted
		}
		if fs.errStates == nil && m.candRows == 0 {
			if out.onchainCoverage != "absent" {
				blindFatalf(t, seed, i, "proven no-index ≠ absent: %q",
					out.onchainCoverage)
			}
		}
		if fs.errStates != nil && out.onchainCoverage == "absent" {
			blindFatalf(t, seed, i, "query error cannot prove absent")
		}
		if out.onchainCoverage == "complete" {
			if fs.errStates != nil || fs.errRecv != nil ||
				m.unchecked > 0 || !m.candUsable || m.srcCap {
				blindFatalf(t, seed, i,
					"complete coverage with degraded/bounded inputs")
			}
		}

		// sourcesEvaluated mirrors the distinct-source bound.
		if out.sourcesEvaluated != len(m.order) {
			blindFatalf(t, seed, i, "sourcesEvaluated %d != %d",
				out.sourcesEvaluated, len(m.order))
		}
		if out.sourcesReceiverUnchecked != m.unchecked {
			blindFatalf(t, seed, i, "unchecked %d != %d",
				out.sourcesReceiverUnchecked, m.unchecked)
		}

		inboundSet := map[string]bool{}
		for _, a := range m.order {
			inboundSet[a] = true
		}
		for _, f := range out.findings {
			// Per-finding coverage vocabulary.
			if f.Coverage != "" && f.Coverage != "complete" &&
				f.Coverage != "partial" {
				blindFatalf(t, seed, i, "finding coverage %q", f.Coverage)
			}
			switch f.Kind {
			case scoring.KindExposure:
				if f.Direction != "INBOUND" {
					blindFatalf(t, seed, i, "exposure dir %q", f.Direction)
				}
				if !inboundSet[f.SourceAddress] {
					blindFatalf(t, seed, i,
						"outbound-only cp %s became a source", f.SourceAddress)
				}
				if f.MeaningfulCount != m.meaningful[f.SourceAddress] {
					blindFatalf(t, seed, i, "meaningful %d != %d",
						f.MeaningfulCount, m.meaningful[f.SourceAddress])
				}
				if len(f.TxHashes) > scoring.ExposureEvidenceTxsV1_1 {
					blindFatalf(t, seed, i, "tx_hashes %d > bound",
						len(f.TxHashes))
				}
				for _, h := range f.TxHashes {
					if !m.txMeaningful[f.SourceAddress][h] {
						blindFatalf(t, seed, i,
							"tx hash %s not a confirmed meaningful transfer", h)
					}
				}
				switch f.TaintKind {
				case "direct":
					if !m.direct[f.SourceAddress] {
						blindFatalf(t, seed, i,
							"direct-tainted claim for untainted %s", f.SourceAddress)
					}
				case "chain":
					if m.service[f.SourceAddress] {
						blindFatalf(t, seed, i,
							"service source %s scored via chain", f.SourceAddress)
					}
					if !m.chain[f.SourceAddress] {
						blindFatalf(t, seed, i,
							"chain claim for %s with no tainted sender",
							f.SourceAddress)
					}
				default:
					blindFatalf(t, seed, i, "taint_kind %q", f.TaintKind)
				}
				if !m.direct[f.SourceAddress] && !m.chain[f.SourceAddress] {
					blindFatalf(t, seed, i,
						"untainted source %s emitted", f.SourceAddress)
				}
			case scoring.KindProceedsReceiver:
				if !m.direct[f.SourceAddress] || m.meaningful[f.SourceAddress] < 1 {
					blindFatalf(t, seed, i,
						"proceeds for non-direct/empty source %s", f.SourceAddress)
				}
			case scoring.KindPossibleVictimContact:
				if f.Direction != "OUTBOUND" {
					blindFatalf(t, seed, i, "victim dir %q", f.Direction)
				}
				if inboundSet[f.SourceAddress] {
					blindFatalf(t, seed, i,
						"inbound source %s double-emitted as victim",
						f.SourceAddress)
				}
				if !m.direct[f.SourceAddress] &&
					(m.service[f.SourceAddress] || !m.chainCP[f.SourceAddress]) {
					blindFatalf(t, seed, i,
						"untainted/service cp %s emitted as victim",
						f.SourceAddress)
				}
			default:
				blindFatalf(t, seed, i, "unexpected finding kind %q", f.Kind)
			}
		}

		// Determinism: identical second evaluation.
		out2 := az.analyze(context.Background(), 1, "ethereum", 1, "0xcand")
		assert.Equal(t, out, out2,
			"seed=%d case=%d nondeterministic analyzer output", seed, i)
	}
}
