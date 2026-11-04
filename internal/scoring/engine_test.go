package scoring

import (
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr(i int) *int { return &i }

func labelFinding(cat, verif, conf, ref string) Finding {
	return Finding{
		Kind: KindLabel, Category: cat, CategoryType: "MALICIOUS_ACTIVITY",
		Source: "wallet_attribution", Ref: ref,
		VerificationStatus: verif, Confidence: conf,
	}
}

func TestEvaluate_ScoreBoundsAndLevels(t *testing.T) {
	cases := []struct {
		name     string
		findings []Finding
		score    int
		level    string
	}{
		{"empty usable set is LOW 0", nil, 0, RiskLow},
		{"ransomware verified high", []Finding{
			labelFinding("RANSOMWARE", "verified", "high", "fp1"),
		}, 95, RiskCritical},
		{"scam reported unknown conf", []Finding{
			labelFinding("SCAM", "reported", "unknown", "fp2"),
		}, 42, RiskMedium}, // round(85 × 0.7 × 0.7) = 41.65 → 42
	}
	for _, tc := range cases {
		v := Evaluate(FindingSet{HasUsableData: true, Findings: tc.findings})
		require.NotNil(t, v.Score, tc.name)
		assert.Equal(t, tc.score, *v.Score, tc.name)
		assert.Equal(t, tc.level, v.RiskLevel, tc.name)
		assert.GreaterOrEqual(t, *v.Score, 0)
		assert.LessOrEqual(t, *v.Score, 100)
	}
}

func TestEvaluate_NoUsableData_NullScore(t *testing.T) {
	v := Evaluate(FindingSet{HasUsableData: false, Findings: []Finding{
		labelFinding("SCAM", "verified", "high", "fp1"),
		{Kind: KindProviderFailed, Source: "goplus", Ref: "goplus"},
	}})
	assert.Nil(t, v.Score)
	assert.Equal(t, RiskUnknown, v.RiskLevel)
	// M3: no usable dimension → block subtotals stay zero; a nonzero
	// subtotal beside risk_score:null is a self-contradicting verdict.
	assert.Equal(t, Blocks{}, v.Blocks)
	// Informational reasons still surface even with a NULL score.
	var codes []string
	for _, r := range v.Reasons {
		codes = append(codes, r.Code)
	}
	assert.Contains(t, codes, ReasonProviderFailed)
}

func TestEvaluate_DedupKeepsStrongestEvidence(t *testing.T) {
	// M1: duplicate identities with differing statuses must keep the
	// STRONGEST representative — alphabetical ordering would retain
	// "disputed" and silently discount verified evidence to zero.
	strong := labelFinding("SCAM", "verified", "high", "same-key")
	weak := labelFinding("SCAM", "disputed", "low", "same-key")
	require.Equal(t, strong.DedupKey(), weak.DedupKey())

	for _, findings := range [][]Finding{{weak, strong}, {strong, weak}} {
		v := Evaluate(FindingSet{HasUsableData: true, Findings: findings})
		require.NotNil(t, v.Score)
		assert.Equal(t, 85, *v.Score,
			"verified+high SCAM survives dedup regardless of input order")
		require.Len(t, v.Reasons, 1)
		assert.Equal(t, "verified", v.Reasons[0].VerificationStatus)
		assert.Equal(t, 85, v.Reasons[0].Weight)
	}
}

func TestEvaluate_DedupKeepsHighestProviderScore(t *testing.T) {
	// R2-M1: same-key provider scores differ only on the score axis —
	// the highest must survive (lowest-first ordering silently
	// under-scores by up to a band).
	lo := Finding{Kind: KindProviderScore, Source: "goplus",
		Ref: "goplus:score", ProviderScore: ptr(30),
		VerificationStatus: VerificationReported}
	hi := lo
	hi.ProviderScore = ptr(75)
	require.Equal(t, lo.DedupKey(), hi.DedupKey())

	for _, findings := range [][]Finding{{lo, hi}, {hi, lo}} {
		v := Evaluate(FindingSet{HasUsableData: true, Findings: findings})
		// band(75) = 10 → other subtotal 10 regardless of order;
		// lowest-first ordering would leave band(30) = 0.
		assert.Equal(t, 10, v.Blocks.Other.Subtotal)
	}
}

func TestEvaluate_DirectMaxNeverSum(t *testing.T) {
	v := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		labelFinding("SCAM", "verified", "high", "a"),     // 85
		labelFinding("PHISHING", "verified", "high", "b"), // 90
		labelFinding("DARKNET", "verified", "high", "c"),  // 75
	}})
	require.NotNil(t, v.Score)
	assert.Equal(t, 90, *v.Score, "direct block is MAX, not sum")
	assert.Equal(t, 90, v.Blocks.Direct.Subtotal)
}

func TestEvaluate_DedupNoDoubleCount(t *testing.T) {
	f := labelFinding("SCAM", "verified", "high", "fp-dup")
	single := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{f}})
	doubled := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{f, f, f}})
	require.Equal(t, single, doubled)
	// Mirror findings with DIFFERENT refs stay distinct but the direct
	// block still takes max — no stacking.
	mirror := f
	mirror.Ref = "fp-mirror"
	mirror.Source = "wallet_attribution"
	two := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{f, mirror}})
	assert.Equal(t, *single.Score, *two.Score)
	assert.Len(t, two.Reasons, 2, "both refs remain visible as reasons")
}

func TestEvaluate_SanctionsFlagIndependent(t *testing.T) {
	v := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		{Kind: KindSanctionsDirect, Category: "SANCTIONED", Source: "ofac",
			Ref: "sdn-123", VerificationStatus: "verified"},
	}})
	require.NotNil(t, v.Score)
	assert.Equal(t, 100, *v.Score)
	assert.Equal(t, RiskCritical, v.RiskLevel)
	assert.Contains(t, v.Flags, FlagDirectSanctions)
	var found bool
	for _, r := range v.Reasons {
		if r.Code == ReasonDirectSanctionsMatch {
			found = true
			assert.Equal(t, 100, r.Weight)
		}
	}
	assert.True(t, found)
}

func TestEvaluate_ReportedBelowVerified(t *testing.T) {
	verified := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		labelFinding("SCAM", "verified", "high", "x"),
	}})
	reported := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		labelFinding("SCAM", "reported", "high", "x"),
	}})
	unverified := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		labelFinding("SCAM", "unverified", "high", "x"),
	}})
	assert.Less(t, *reported.Score, *verified.Score)
	assert.Less(t, *unverified.Score, *reported.Score)
}

func TestEvaluate_ConfidenceOrdering(t *testing.T) {
	score := func(conf string) int {
		v := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
			labelFinding("HACK", "reported", conf, "x"),
		}})
		return *v.Score
	}
	high, medium, low, unknown := score("high"), score("medium"), score("low"), score("unknown")
	assert.Greater(t, high, medium)
	assert.Greater(t, medium, low)
	assert.Greater(t, low, unknown)
	assert.Equal(t, 63, high)    // round(90 × 0.7 × 1.0)
	assert.Equal(t, 44, unknown) // round(90 × 0.7 × 0.7) = 44.1
}

func TestEvaluate_DisputedScoresZero(t *testing.T) {
	v := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		labelFinding("SCAM", "disputed", "high", "x"),
	}})
	require.NotNil(t, v.Score)
	assert.Equal(t, 0, *v.Score)
	assert.Equal(t, RiskLow, v.RiskLevel)
	assert.Equal(t, 0, v.Blocks.Direct.Subtotal)
	// The disputed label is still explained — not silently dropped.
	require.Len(t, v.Reasons, 1)
	assert.Equal(t, ReasonDirectLabel, v.Reasons[0].Code)
	assert.Equal(t, 0, v.Reasons[0].Weight)
	assert.Equal(t, "disputed", v.Reasons[0].VerificationStatus)
}

func TestEvaluate_ClassificationsScoreZero(t *testing.T) {
	for _, cat := range []string{"GAMBLING", "MIXER", "EXCHANGE"} {
		v := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
			labelFinding(cat, "verified", "high", "x"),
		}})
		require.NotNil(t, v.Score, cat)
		assert.Equal(t, 0, *v.Score, cat+" is a classification, not a crime")
		require.Len(t, v.Reasons, 1)
		assert.Equal(t, ReasonLabelClassification, v.Reasons[0].Code)
	}
	// Unknown dynamic category → same zero-weight classification path.
	v := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		labelFinding("BRAND_NEW_THREAT", "verified", "high", "x"),
	}})
	assert.Equal(t, 0, *v.Score)
	assert.Equal(t, ReasonLabelClassification, v.Reasons[0].Code)
}

func TestEvaluate_ExposureHops(t *testing.T) {
	mk := func(hops int) Finding {
		return Finding{
			Kind: KindExposure, Category: "SANCTIONED", Source: "goplus",
			Ref: "cp-1", Basis: BasisProviderReported, Hops: hops,
			VerificationStatus: "reported",
		}
	}
	v1 := Evaluate(FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{mk(1)}})
	assert.Equal(t, 7, *v1.Score) // round(10 × 0.7)
	assert.Equal(t, 7, v1.Blocks.Exposure.Subtotal)

	v2 := Evaluate(FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{mk(2)}})
	assert.Equal(t, 4, *v2.Score) // round(5 × 0.7)

	// hops>2 → listed but never scored.
	v3 := Evaluate(FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{mk(3), mk(7)}})
	assert.Equal(t, 0, *v3.Score)
	for _, r := range v3.Reasons {
		assert.Equal(t, ReasonExposureProvider, r.Code)
		assert.Equal(t, 0, r.Weight)
	}

	// hops within range but above the requested max_hops → unscored.
	v4 := Evaluate(FindingSet{HasUsableData: true, MaxHops: 1, Findings: []Finding{mk(2)}})
	assert.Equal(t, 0, *v4.Score)
	assert.Equal(t, 0, v4.Reasons[0].Weight)

	// Non-provider basis is never scored.
	graph := mk(1)
	graph.Basis = "graph_trace"
	vg := Evaluate(FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{graph}})
	assert.Equal(t, 0, *vg.Score)
}

func TestEvaluate_ExposureCap(t *testing.T) {
	var fs []Finding
	for i := 0; i < 10; i++ {
		fs = append(fs, Finding{
			Kind: KindExposure, Category: "SCAM", Source: "goplus",
			Ref: "cp-" + string(rune('a'+i)), Basis: BasisProviderReported,
			Hops: 1, VerificationStatus: "reported",
		})
	}
	v := Evaluate(FindingSet{HasUsableData: true, MaxHops: 2, Findings: fs})
	assert.Equal(t, 20, v.Blocks.Exposure.Subtotal, "cap 20")
	assert.True(t, v.Blocks.Exposure.Capped)
	assert.Equal(t, 20, *v.Score)
}

func TestEvaluate_ProviderScoreBand_MaxNotSum(t *testing.T) {
	v := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		{Kind: KindProviderScore, Source: "goplus", Ref: "goplus:score",
			ProviderScore: ptr(90)},
		{Kind: KindProviderScore, Source: "publicaml", Ref: "publicaml:score",
			ProviderScore: ptr(85)},
	}})
	// Max band only: 90 → +20, NOT 20+20.
	assert.Equal(t, 20, v.Blocks.Other.Subtotal)
	assert.Equal(t, 20, *v.Score)

	mid := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		{Kind: KindProviderScore, Source: "goplus", Ref: "goplus:score",
			ProviderScore: ptr(55)},
	}})
	assert.Equal(t, 10, *mid.Score)

	low := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		{Kind: KindProviderScore, Source: "goplus", Ref: "goplus:score",
			ProviderScore: ptr(30)},
	}})
	assert.Equal(t, 0, *low.Score)

	// Provider score is capped by the formula's other-block cap (30).
	high := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		{Kind: KindProviderScore, Source: "goplus", Ref: "goplus:score",
			ProviderScore: ptr(100)},
	}})
	assert.LessOrEqual(t, high.Blocks.Other.Subtotal, 30)
}

func TestEvaluate_ProviderSanctionedObservation_SameTable(t *testing.T) {
	v := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		{Kind: KindProviderObservation, Category: "SANCTIONED",
			CategoryType: "LEGAL", Source: "goplus", Ref: "goplus:sanctioned",
			VerificationStatus: "reported", Confidence: "medium"},
	}})
	// round(85 × 0.7 × 0.9) = 54 — direct-block table path, not flat 100.
	require.NotNil(t, v.Score)
	assert.Equal(t, 54, *v.Score)
	assert.Equal(t, 54, v.Blocks.Direct.Subtotal)
	require.Len(t, v.Reasons, 1)
	assert.Equal(t, ReasonProviderObservation, v.Reasons[0].Code)
	// A provider-reported "sanctioned" observation is NOT the official
	// flag — only an OFAC direct match raises it.
	assert.NotContains(t, v.Flags, FlagDirectSanctions)
}

func TestEvaluate_SingleBlockMembership(t *testing.T) {
	// A provider observation contributes once via the direct block and
	// does not also appear in the other block.
	v := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		{Kind: KindProviderObservation, Category: "SCAM", Source: "goplus",
			Ref: "goplus:scam", VerificationStatus: "reported", Confidence: "high"},
		{Kind: KindProviderScore, Source: "goplus", Ref: "goplus:score",
			ProviderScore: ptr(100)},
	}})
	// direct = round(85×0.7×1.0)=60; other = band(100)=20 → 80.
	assert.Equal(t, 60, v.Blocks.Direct.Subtotal)
	assert.Equal(t, 20, v.Blocks.Other.Subtotal)
	assert.Equal(t, 80, *v.Score)
}

func TestEvaluate_CombinedFormulaAndCap100(t *testing.T) {
	v := Evaluate(FindingSet{HasUsableData: true, Findings: []Finding{
		{Kind: KindSanctionsDirect, Category: "SANCTIONED", Source: "ofac",
			Ref: "sdn-1"},
		labelFinding("HACK", "verified", "high", "fp"),
		{Kind: KindExposure, Source: "goplus", Ref: "c1",
			Basis: BasisProviderReported, Hops: 1},
		{Kind: KindProviderScore, Source: "goplus", Ref: "goplus:score",
			ProviderScore: ptr(99)},
	}, MaxHops: 2})
	assert.Equal(t, 100, *v.Score, "clamped at 100")
	assert.Equal(t, 100, v.Blocks.Direct.Subtotal)
}

func TestEvaluate_Determinism_ByteIdentical(t *testing.T) {
	base := []Finding{
		labelFinding("SCAM", "reported", "high", "a"),
		labelFinding("GAMBLING", "verified", "medium", "b"),
		{Kind: KindExposure, Source: "goplus", Ref: "c1",
			Basis: BasisProviderReported, Hops: 2},
		{Kind: KindProviderScore, Source: "goplus", Ref: "s1",
			ProviderScore: ptr(70)},
		{Kind: KindProviderFailed, Source: "chainabuse", Ref: "chainabuse"},
		{Kind: KindCrossChainListing, Category: "SANCTIONED",
			Source: "ofac", Ref: "eth:sdn-9"},
	}
	fs := FindingSet{HasUsableData: true, Findings: base}
	want, err := json.Marshal(Evaluate(fs))
	require.NoError(t, err)

	// 40 seeded shuffles must produce byte-identical verdicts.
	r := rand.New(rand.NewSource(42))
	for i := 0; i < 40; i++ {
		shuffled := make([]Finding, len(base))
		copy(shuffled, base)
		r.Shuffle(len(shuffled), func(a, b int) {
			shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
		})
		got, err := json.Marshal(Evaluate(FindingSet{
			HasUsableData: true, Findings: shuffled}))
		require.NoError(t, err)
		require.Equal(t, string(want), string(got), "shuffle %d", i)
	}
}

func TestEvaluate_RiskLevelBoundaries(t *testing.T) {
	assert.Equal(t, RiskCritical, level(80))
	assert.Equal(t, RiskHigh, level(79))
	assert.Equal(t, RiskHigh, level(60))
	assert.Equal(t, RiskMedium, level(59))
	assert.Equal(t, RiskMedium, level(30))
	assert.Equal(t, RiskLow, level(29))
	assert.Equal(t, RiskLow, level(0))
}
