package scoring

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func onchainExp(addr string, n int, taint string) Finding {
	return Finding{Kind: KindExposure, Category: "SCAM", Source: "onchain",
		Ref: "onchain:" + addr, Basis: BasisOnchainComputed, Hops: 1,
		VerificationStatus: VerificationReported, SourceAddress: addr,
		Direction: "INBOUND", MeaningfulCount: n, TaintKind: taint}
}

func provExp(addr string, hops int) Finding {
	return Finding{Kind: KindExposure, Category: "SCAM", Source: "goplus",
		Ref: "goplus:" + addr + ":h1:in", Basis: BasisProviderReported,
		Hops: hops, VerificationStatus: VerificationReported,
		SourceAddress: addr, Direction: "INBOUND"}
}

func weightOf(v Verdict, code, ref string) int {
	for _, r := range v.Reasons {
		if r.Code == code && r.EvidenceRef == ref {
			return r.Weight
		}
	}
	return -1
}

func TestEvaluateFor_Dispatch(t *testing.T) {
	fs := FindingSet{HasUsableData: true}
	assert.Equal(t, PolicyV1, EvaluateFor(fs, PolicyV1).PolicyVersion)
	assert.Equal(t, PolicyV1, EvaluateFor(fs, "").PolicyVersion)
	assert.Equal(t, PolicyV1_1, EvaluateFor(fs, PolicyV1_1).PolicyVersion)

	v := EvaluateFor(fs, "v9.9.9")
	assert.Equal(t, "v9.9.9", v.PolicyVersion)
	assert.Equal(t, RiskUnknown, v.RiskLevel)
	assert.Nil(t, v.Score)
	require.Len(t, v.Reasons, 1)
	assert.Equal(t, ReasonNotEvaluated, v.Reasons[0].Code)
}

func TestV11_OnchainSourceScoring(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		onchainExp("0xaaa", 1, "direct"),
		onchainExp("0xbbb", 2, "chain"),
	}}
	v := EvaluateFor(fs, PolicyV1_1)
	// 30 + (30+7) = 67 exposure.
	assert.Equal(t, 67, v.Blocks.Exposure.Subtotal)
	assert.Equal(t, 30, weightOf(v, ReasonExposureOnchainSource, "onchain:0xaaa"))
	assert.Equal(t, 37, weightOf(v, ReasonExposureOnchainSource, "onchain:0xbbb"))
	require.NotNil(t, v.Score)
	assert.Equal(t, 67, *v.Score)
}

func TestV11_ExposureCap70(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		onchainExp("0xa", 8, "direct"), // 30+7*7=79
		onchainExp("0xb", 5, "direct"), // 30+7*4=58
	}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 70, v.Blocks.Exposure.Subtotal)
	assert.True(t, v.Blocks.Exposure.Capped)
}

func TestV11_DedupeOnchainWinsEvenZero(t *testing.T) {
	// Onchain winner with n=0 still suppresses the provider +21.
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		onchainExp("0xaaa", 0, "direct"),
		provExp("0xaaa", 1),
	}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 0, v.Blocks.Exposure.Subtotal)
	var suppressed bool
	for _, r := range v.Reasons {
		if r.EvidenceRef == "goplus:0xaaa:h1:in" {
			assert.Equal(t, 0, r.Weight)
			assert.Contains(t, r.Detail, "suppressed_by_onchain")
			suppressed = true
		}
	}
	assert.True(t, suppressed, "provider +21 must be suppressed by onchain winner")
}

func TestV11_ProviderSourceFlat21(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		provExp("0xaaa", 1), provExp("0xbbb", 2),
	}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 42, v.Blocks.Exposure.Subtotal)

	// Beyond max_hops → not eligible.
	fs2 := FindingSet{HasUsableData: true, MaxHops: 1, Findings: []Finding{
		provExp("0xaaa", 2)}}
	v2 := EvaluateFor(fs2, PolicyV1_1)
	assert.Equal(t, 0, v2.Blocks.Exposure.Subtotal)
}

func TestV11_OutboundNeverScores(t *testing.T) {
	f := onchainExp("0xaaa", 3, "direct")
	f.Direction = "OUTBOUND"
	p := provExp("0xbbb", 1)
	p.Direction = "OUTBOUND"
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{f, p}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 0, v.Blocks.Exposure.Subtotal)
}

func TestV11_ServiceClassifiedGate(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2,
		CandidateServiceClassified: true,
		Findings: []Finding{
			onchainExp("0xaaa", 2, "direct"), provExp("0xbbb", 1)}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 0, v.Blocks.Exposure.Subtotal)
	var svc bool
	for _, r := range v.Reasons {
		if r.Code == ReasonServiceClassifiedCandidate {
			svc = true
		}
	}
	assert.True(t, svc, "service-classification reason must be explicit")
}

func TestV11_InformationalKinds(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		{Kind: KindProceedsReceiver, Category: "SCAM", Source: "onchain",
			Ref: "onchain:proceeds:0xa", VerificationStatus: "reported"},
		{Kind: KindPossibleVictimContact, Category: "SCAM", Source: "goplus",
			Ref: "goplus:0xb:h1:out"},
	}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 0, *v.Score)
	assert.Equal(t, 0, weightOf(v, ReasonProceedsReceiver, "onchain:proceeds:0xa"))
	assert.Equal(t, 0, weightOf(v, ReasonPossibleVictimContact, "goplus:0xb:h1:out"))
}

func TestV11_GenericProviderKeepsV1Weights(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		{Kind: KindExposure, Category: "SANCTIONED", Source: "goplus",
			Ref: "cp-1", Basis: BasisProviderReported, Hops: 1,
			VerificationStatus: "reported"},
	}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 7, v.Blocks.Exposure.Subtotal, "v1 hop1 weight preserved")
}

// Shuffle determinism: any input order produces the identical verdict.
func TestV11_ShuffleDeterminism(t *testing.T) {
	base := []Finding{
		onchainExp("0xa", 2, "direct"), onchainExp("0xb", 1, "chain"),
		provExp("0xa", 1), provExp("0xc", 2),
		{Kind: KindLabel, Category: "SCAM", Source: "wattr", Ref: "wallet_label:1",
			VerificationStatus: "verified", Confidence: "high"},
		{Kind: KindProviderScore, Source: "goplus", Ref: "goplus:score",
			ProviderScore: ptr(80)},
		{Kind: KindProceedsReceiver, Source: "onchain", Ref: "onchain:proceeds:0xa"},
	}
	ref := EvaluateFor(FindingSet{HasUsableData: true, MaxHops: 2,
		Findings: base}, PolicyV1_1)
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 30; i++ {
		fs := append([]Finding{}, base...)
		rng.Shuffle(len(fs), func(a, b int) { fs[a], fs[b] = fs[b], fs[a] })
		got := EvaluateFor(FindingSet{HasUsableData: true, MaxHops: 2,
			Findings: fs}, PolicyV1_1)
		assert.Equal(t, ref, got, "shuffle %d changed the verdict", i)
	}
}

// --- Remediation round 1: normative acceptance values ---

// 6 meaningful transfers from ONE source → 30 + 7*5 = +65.
func TestV11_SixTransfersOneSource(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		onchainExp("0xaaa", 6, "direct")}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 65, v.Blocks.Exposure.Subtotal)
}

// Two independent sources, one meaningful transfer each → 30+30 = +60.
func TestV11_TwoOneTransferSources(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		onchainExp("0xaaa", 1, "direct"), onchainExp("0xbbb", 1, "direct")}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 60, v.Blocks.Exposure.Subtotal)
}

// Three sources → 90 raw → capped at 70.
func TestV11_ThreeSourcesCap70(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		onchainExp("0xa", 1, "direct"), onchainExp("0xb", 1, "direct"),
		onchainExp("0xc", 1, "direct")}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 70, v.Blocks.Exposure.Subtotal)
	assert.True(t, v.Blocks.Exposure.Capped)
}

// 50 meaningful transfers from one source → 30+7*49=373 → capped 70.
func TestV11_FiftyTransfersCap(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		onchainExp("0xaaa", 50, "direct")}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 70, v.Blocks.Exposure.Subtotal)
}

// Dedupe for n>=1: onchain wins over provider flat-21 — NOT 30+21=51.
func TestV11_DedupeNot51(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		onchainExp("0xaaa", 1, "direct"), provExp("0xaaa", 1)}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 30, v.Blocks.Exposure.Subtotal)
}

// A8: MaxHops=0 → exposures are informational only.
func TestV11_MaxHopsZeroInformational(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 0, Findings: []Finding{
		onchainExp("0xaaa", 6, "direct"), provExp("0xbbb", 1)}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 0, v.Blocks.Exposure.Subtotal)
	assert.Equal(t, 0, weightOf(v, ReasonExposureInformational, "onchain:0xaaa"))
	assert.Equal(t, -1, weightOf(v, ReasonExposureOnchainSource, "onchain:0xaaa"),
		"ineligible onchain source remaps to informational")
}

// Major #1 (B3): a named provider source with UNKNOWN hops (Hops==0) is
// adjacency-eligible → flat +21. Explicit hops beyond bound score 0.
func TestV11_ProviderUnknownHopsAdjacency(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		provExp("0xaaa", 0)}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 21, v.Blocks.Exposure.Subtotal)

	// MaxHops=0 → even named sources score nothing (bound still governs).
	fs2 := FindingSet{HasUsableData: true, MaxHops: 0, Findings: []Finding{
		provExp("0xaaa", 0)}}
	assert.Equal(t, 0, EvaluateFor(fs2, PolicyV1_1).Blocks.Exposure.Subtotal)
}

// Minor #7: an OUTBOUND exposure finding sharing a source address can
// never join the INBOUND group or suppress its weight.
func TestV11_OutboundNeverJoinsGroup(t *testing.T) {
	out := provExp("0xaaa", 1)
	out.Direction = "OUTBOUND"
	out.Ref = "goplus:0xaaa:h1:out"
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
		provExp("0xaaa", 1), out}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 21, v.Blocks.Exposure.Subtotal,
		"OUTBOUND sibling must not suppress the inbound +21")
	assert.Equal(t, 0, weightOf(v, ReasonExposureInformational, out.Ref))
}

// Minor #8: service-classified candidate — each source reason keeps its
// code at weight 0, plus ONE explicit informational reason.
func TestV11_ServiceGateKeepsSourceCodes(t *testing.T) {
	fs := FindingSet{HasUsableData: true, MaxHops: 2,
		CandidateServiceClassified: true,
		Findings: []Finding{
			onchainExp("0xaaa", 2, "direct"), provExp("0xbbb", 1)}}
	v := EvaluateFor(fs, PolicyV1_1)
	assert.Equal(t, 0, v.Blocks.Exposure.Subtotal)
	var svcCount int
	var onchainKept, provKept bool
	for _, r := range v.Reasons {
		switch r.Code {
		case ReasonServiceClassifiedCandidate:
			svcCount++
		case ReasonExposureOnchainSource:
			assert.Equal(t, "onchain:0xaaa", r.EvidenceRef)
			assert.Equal(t, 0, r.Weight)
			onchainKept = true
		case ReasonExposureProviderSource:
			assert.Equal(t, "goplus:0xbbb:h1:in", r.EvidenceRef)
			assert.Equal(t, 0, r.Weight)
			provKept = true
		}
	}
	assert.Equal(t, 1, svcCount, "exactly one informational svc reason")
	assert.True(t, onchainKept && provKept, "per-source codes preserved")
}

// A/B divergence evidence (review finding #4): the SAME findings under
// v1.0.0 vs v1.1.0 produce materially different exposure math —
// v1: flat hop weights capped at 20; v1.1: per-source frequency/
// diversity weights capped at 70.
func TestAB_Divergence(t *testing.T) {
	a := onchainExp("0xaaa", 6, "direct") // n=6 → 65 under v1.1
	b := provExp("0xbbb", 1)              // named source → +21 under v1.1
	b.Category = "HACK"                   // v1 dedupes exposure by category
	fs := FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{a, b}}
	v1 := EvaluateFor(fs, PolicyV1)
	v11 := EvaluateFor(fs, PolicyV1_1)

	// v1.0.0: only the provider-reported finding scores — hop-1 flat
	// weight 7 under the 20 cap; the onchain basis is opaque to the
	// frozen engine (scores 0).
	require.NotNil(t, v1.Score)
	require.NotNil(t, v11.Score)
	assert.Equal(t, 7, v1.Blocks.Exposure.Subtotal)
	// v1.1.0: 65 (n=6 formula) + 21 (named provider source) = 86 → 70.
	assert.Equal(t, 70, v11.Blocks.Exposure.Subtotal)
	assert.NotEqual(t, *v1.Score, *v11.Score)
	assert.Equal(t, PolicyV1, v1.PolicyVersion)
	assert.Equal(t, PolicyV1_1, v11.PolicyVersion)
}
