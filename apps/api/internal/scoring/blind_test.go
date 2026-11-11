package scoring

// BLIND property suite — randomized invariant probes the reviewer did
// not see. Every test reads BLIND_SEED (default 1); failures print
// seed + case index for reproduction.

import (
	"math/big"
	"math/rand"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

var (
	blindKinds = []string{
		KindSanctionsDirect, KindLabel, KindProviderObservation,
		KindProviderScore, KindExposure, KindProceedsReceiver,
		KindPossibleVictimContact, KindCrossChainListing,
		KindSnapshotStale, KindProviderFailed,
	}
	blindCats = []string{
		"SCAM", "HACK", "RANSOMWARE", "PHISHING", "TERRORISM", "DRAINER",
		"SANCTIONED", "MONEY_LAUNDERING", "DARKNET", "EXTREMISM", "FROZEN",
		"MIXER", "EXCHANGE", "GAMBLING", "",
	}
	blindSources = []string{"goplus", "onchain", "wattr", "chainabuse", "publicaml"}
	blindBases   = []string{"", BasisProviderReported, BasisOnchainComputed}
	blindStatus  = []string{
		VerificationVerified, VerificationReported,
		VerificationUnverified, VerificationDisputed, "",
	}
	blindDirs     = []string{"", "INBOUND", "OUTBOUND"}
	blindTaint    = []string{"", "direct", "chain"}
	blindCoverage = []string{"", "complete", "partial"}
)

func blindFinding(r *rand.Rand, i int) Finding {
	f := Finding{
		Kind:               blindKinds[r.Intn(len(blindKinds))],
		Category:           blindCats[r.Intn(len(blindCats))],
		Source:             blindSources[r.Intn(len(blindSources))],
		Ref:                "blind:" + strconv.Itoa(i) + ":" + strconv.Itoa(r.Intn(1<<20)),
		VerificationStatus: blindStatus[r.Intn(len(blindStatus))],
		Basis:              blindBases[r.Intn(len(blindBases))],
		Hops:               r.Intn(5),
		MeaningfulCount:    r.Intn(12),
		TaintKind:          blindTaint[r.Intn(len(blindTaint))],
		Coverage:           blindCoverage[r.Intn(len(blindCoverage))],
		Direction:          blindDirs[r.Intn(len(blindDirs))],
	}
	// Small address pool → real dedupe pressure across findings.
	if r.Intn(3) > 0 {
		f.SourceAddress = "0x" + string(rune('a'+r.Intn(8)))
	}
	if r.Intn(4) == 0 {
		ps := r.Intn(101)
		f.ProviderScore = &ps
	}
	if r.Intn(4) == 0 {
		f.Confidence = []string{
			ConfidenceHigh, ConfidenceMedium,
			ConfidenceLow, ConfidenceUnknown}[r.Intn(4)]
	}
	return f
}

func blindFindingSet(r *rand.Rand, n int) FindingSet {
	fs := FindingSet{
		HasUsableData:              r.Intn(5) > 0,
		MaxHops:                    r.Intn(4),
		CandidateServiceClassified: r.Intn(4) == 0,
	}
	for i := 0; i < n; i++ {
		fs.Findings = append(fs.Findings, blindFinding(r, i))
	}
	return fs
}

// Core invariants over random finding sets: score bounds, exposure cap,
// determinism, policy stamping, unknown-policy fail-closed.
func TestBlind_V11_Invariants(t *testing.T) {
	seed := blindSeed(t)
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < blindN; i++ {
		fs := blindFindingSet(r, r.Intn(20))
		v := EvaluateFor(fs, PolicyV1_1)

		assert.Equal(t, PolicyV1_1, v.PolicyVersion,
			"seed=%d case=%d policy stamp", seed, i)
		if v.Score != nil {
			if *v.Score < 0 || *v.Score > 100 {
				blindFatalf(t, seed, i, "score %d out of [0,100]", *v.Score)
			}
		} else {
			assert.Equal(t, RiskUnknown, v.RiskLevel)
		}
		if v.Blocks.Exposure.Subtotal < 0 || v.Blocks.Exposure.Subtotal > exposureCapV1_1 {
			blindFatalf(t, seed, i, "exposure %d outside [0,%d]",
				v.Blocks.Exposure.Subtotal, exposureCapV1_1)
		}
		for _, rs := range v.Reasons {
			if rs.Weight < 0 {
				blindFatalf(t, seed, i, "negative reason weight %d (%s)",
					rs.Weight, rs.Code)
			}
		}
		// Identical re-evaluation must produce an identical verdict.
		v2 := EvaluateFor(fs, PolicyV1_1)
		assert.Equal(t, v, v2, "seed=%d case=%d nondeterministic verdict", seed, i)

		// Unknown policy fails closed for the same input.
		vu := EvaluateFor(fs, "v9.9.9-blind")
		assert.Nil(t, vu.Score, "seed=%d case=%d unknown policy scored", seed, i)
		assert.Equal(t, RiskUnknown, vu.RiskLevel)
	}
}

// v1 parity: EvaluateFor(·, PolicyV1) must be byte-equal to the frozen
// Evaluate on every random input.
func TestBlind_V1DispatchParity(t *testing.T) {
	seed := blindSeed(t)
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < blindN; i++ {
		fs := blindFindingSet(r, r.Intn(20))
		got := EvaluateFor(fs, PolicyV1)
		want := Evaluate(fs)
		assert.Equal(t, want, got,
			"seed=%d case=%d v1 dispatch diverged from frozen Evaluate", seed, i)
	}
}

// Dedupe: N random findings sharing ONE address — the group contributes
// exactly the rank-winner's weight (never winner+loser), capped at 70.
func TestBlind_V11_DedupeGroupWeight(t *testing.T) {
	seed := blindSeed(t)
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < blindN; i++ {
		addr := "0xgroup"
		fs := FindingSet{HasUsableData: true, MaxHops: r.Intn(4)}
		var group []Finding
		for j := 0; j < 1+r.Intn(8); j++ {
			f := blindFinding(r, j)
			f.Kind, f.SourceAddress = KindExposure, addr
			group = append(group, f)
		}
		fs.Findings = append(fs.Findings, group...)

		// Expected winner: the engine dedupes by DedupKey first, so the
		// representative is the first max-rank member in DEDUPE order —
		// OUTBOUND findings can never join the group.
		deduped := dedupe(group)
		var winner *Finding
		for j := range deduped {
			f := &deduped[j]
			if f.Direction == "OUTBOUND" {
				continue
			}
			if winner == nil ||
				exposureWinnerRank(*f) > exposureWinnerRank(*winner) {
				winner = f
			}
		}
		want := 0
		if winner != nil && fs.MaxHops >= 1 {
			switch winner.Basis {
			case BasisOnchainComputed:
				if winner.MeaningfulCount >= 1 && winner.TaintKind != "" {
					want = exposureSourceBaseV1_1 +
						exposureSourceFreqV1_1*(winner.MeaningfulCount-1)
				}
			case BasisProviderReported:
				if winner.Hops == 0 || winner.Hops <= fs.MaxHops {
					want = exposureReportedSourceV1_1
				}
			}
		}
		if want > exposureCapV1_1 {
			want = exposureCapV1_1
		}
		got := EvaluateFor(fs, PolicyV1_1).Blocks.Exposure.Subtotal
		if got != want {
			blindFatalf(t, seed, i,
				"group exposure %d != winner weight %d (winner+loser summed?)",
				got, want)
		}
	}
}

// Monotonicity: n+1 meaningful transfers never reduces a tainted
// source's contribution (until the 70 cap binds).
func TestBlind_V11_MonotonicMeaningfulCount(t *testing.T) {
	seed := blindSeed(t)
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < blindN; i++ {
		maxHops := 1 + r.Intn(3)
		base := r.Intn(12)
		mk := func(n int) FindingSet {
			return FindingSet{HasUsableData: true, MaxHops: maxHops,
				Findings: []Finding{
					onchainExp("0xsrc", n, "direct"),
				}}
		}
		w0 := EvaluateFor(mk(base), PolicyV1_1).Blocks.Exposure.Subtotal
		w1 := EvaluateFor(mk(base+1), PolicyV1_1).Blocks.Exposure.Subtotal
		if w1 < w0 {
			blindFatalf(t, seed, i,
				"n=%d→%d weight decreased %d→%d", base, base+1, w0, w1)
		}
		if w1 > exposureCapV1_1 {
			blindFatalf(t, seed, i, "weight %d exceeds cap", w1)
		}
	}
}

// Direction: sets whose every KindExposure is OUTBOUND can never score
// exposure — regardless of hops, basis, or counts.
func TestBlind_V11_OutboundOnlyNeverScores(t *testing.T) {
	seed := blindSeed(t)
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < blindN; i++ {
		fs := blindFindingSet(r, r.Intn(15))
		for j := range fs.Findings {
			if fs.Findings[j].Kind == KindExposure {
				fs.Findings[j].Direction = "OUTBOUND"
			}
		}
		got := EvaluateFor(fs, PolicyV1_1).Blocks.Exposure.Subtotal
		if got != 0 {
			blindFatalf(t, seed, i, "outbound-only exposure scored %d", got)
		}
	}
}

// big.Int boundary fuzz: meaningful flips exactly at the computed raw
// threshold for random price/decimals overlays.
func TestBlind_IsMeaningful_BoundaryFuzz(t *testing.T) {
	seed := blindSeed(t)
	r := rand.New(rand.NewSource(seed))
	t.Cleanup(func() { ApplyScoringAssetPrices(nil) })
	floor := big.NewInt(meaningfulFloorUSDV1_1 * 1_000_000)
	for i := 0; i < blindN; i++ {
		micro := int64(1 + r.Int63n(1_000_000_000)) // 1..1e9 micro-usd
		dec := int64(r.Intn(19))                    // 0..18 decimals
		ApplyScoringAssetPrices(map[string]AssetPriceV1_1{
			"blind:native": {MicroUSD: micro, Decimals: int(dec)},
		})

		// Threshold: smallest raw with raw*micro >= floor*10^dec.
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(dec), nil)
		num := new(big.Int).Mul(floor, scale)
		thr := new(big.Int)
		thr.Quo(num, big.NewInt(micro))
		if new(big.Int).Mod(num, big.NewInt(micro)).Sign() > 0 {
			thr.Add(thr, big.NewInt(1))
		}
		m, priced, err := IsMeaningfulV1_1("blind", "native", thr.String())
		require.NoError(t, err, "seed=%d case=%d", seed, i)
		if !priced || !m {
			blindFatalf(t, seed, i,
				"threshold raw %s (micro=%d dec=%d) must be meaningful",
				thr, micro, dec)
		}
		if thr.Sign() > 0 {
			below := new(big.Int).Sub(thr, big.NewInt(1))
			m2, priced2, err2 := IsMeaningfulV1_1("blind", "native", below.String())
			require.NoError(t, err2, "seed=%d case=%d", seed, i)
			if !priced2 || m2 {
				blindFatalf(t, seed, i,
					"raw %s (one below threshold) must NOT be meaningful",
					below)
			}
		}
	}
}
