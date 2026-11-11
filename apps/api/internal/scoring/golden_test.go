package scoring

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Golden v1 parity corpus (policy v1.1 plan, phase 4): testdata/v1_golden.json
// pins verdict JSON generated from the UNMODIFIED v1 engine. The corpus is
// regenerated only via `go test ./internal/scoring -run TestGoldenV1 -update`
// and the diff is reviewed into git — never silently.
var updateGolden = flag.Bool("update", false, "regenerate testdata/v1_golden.json from the current v1 engine")

const goldenPath = "testdata/v1_golden.json"

type goldenEntry struct {
	Name       string          `json:"name"`
	FindingSet json.RawMessage `json:"findingset"`
	Verdict    json.RawMessage `json:"verdict"`
}

// goldenCorpus is a broad FindingSet sweep: empty, every kind, dedup
// edge cases, exposure mixes, cap saturation, and the no-usable-data
// contract — the behaviors v1 selection must reproduce byte-identically.
func goldenCorpus() []struct {
	name string
	fs   FindingSet
} {
	exposure := func(ref string, hops int) Finding {
		return Finding{
			Kind: KindExposure, Category: "SANCTIONED", Source: "goplus",
			Ref: ref, Basis: BasisProviderReported, Hops: hops,
			VerificationStatus: VerificationReported,
		}
	}
	return []struct {
		name string
		fs   FindingSet
	}{
		{"empty usable", FindingSet{HasUsableData: true}},
		{"empty unusable", FindingSet{HasUsableData: false}},
		{"unusable with findings", FindingSet{HasUsableData: false, Findings: []Finding{
			labelFinding("SCAM", "verified", "high", "fp1"),
			{Kind: KindProviderFailed, Source: "goplus", Ref: "goplus"},
		}}},
		{"sanctions direct", FindingSet{HasUsableData: true, Findings: []Finding{
			{Kind: KindSanctionsDirect, Category: "SANCTIONED", Source: "ofac",
				Ref: "sanctions_entity:7", VerificationStatus: VerificationVerified,
				Confidence: ConfidenceHigh},
		}}},
		{"each label weight", FindingSet{HasUsableData: true, Findings: []Finding{
			labelFinding("RANSOMWARE", "verified", "high", "fp-r"),
			labelFinding("HACK", "reported", "medium", "fp-h"),
			labelFinding("DARKNET", "unverified", "low", "fp-d"),
		}}},
		{"classification kinds", FindingSet{HasUsableData: true, Findings: []Finding{
			labelFinding("GAMBLING", "verified", "high", "fp-g"),
			labelFinding("MIXER", "reported", "high", "fp-m"),
			labelFinding("EXCHANGE", "verified", "high", "fp-e"),
			labelFinding("BRAND_NEW_THREAT", "verified", "high", "fp-n"),
		}}},
		{"disputed label zero", FindingSet{HasUsableData: true, Findings: []Finding{
			labelFinding("SCAM", "disputed", "high", "fp-x"),
		}}},
		{"dedup strongest wins", FindingSet{HasUsableData: true, Findings: []Finding{
			labelFinding("SCAM", "disputed", "low", "same-key"),
			labelFinding("SCAM", "verified", "high", "same-key"),
		}}},
		{"provider observation sanctioned", FindingSet{HasUsableData: true, Findings: []Finding{
			{Kind: KindProviderObservation, Category: "SANCTIONED",
				CategoryType: "LEGAL", Source: "goplus", Ref: "goplus:sanctioned",
				VerificationStatus: "reported", Confidence: "medium"},
		}}},
		{"provider score bands", FindingSet{HasUsableData: true, Findings: []Finding{
			{Kind: KindProviderScore, Source: "goplus", Ref: "goplus:score",
				ProviderScore: ptr(90)},
			{Kind: KindProviderScore, Source: "publicaml", Ref: "publicaml:score",
				ProviderScore: ptr(55)},
		}}},
		{"provider score dedup highest", FindingSet{HasUsableData: true, Findings: []Finding{
			{Kind: KindProviderScore, Source: "goplus", Ref: "goplus:score",
				ProviderScore: ptr(30), VerificationStatus: VerificationReported},
			{Kind: KindProviderScore, Source: "goplus", Ref: "goplus:score",
				ProviderScore: ptr(75), VerificationStatus: VerificationReported},
		}}},
		{"exposure hops mix", FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
			exposure("cp-1", 1), exposure("cp-2", 2), exposure("cp-3", 3),
		}}},
		{"exposure above max hops", FindingSet{HasUsableData: true, MaxHops: 1, Findings: []Finding{
			exposure("cp-1", 2),
		}}},
		{"exposure non-provider basis", FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
			{Kind: KindExposure, Category: "SCAM", Source: "goplus", Ref: "cp-1",
				Basis: "graph_trace", Hops: 1, VerificationStatus: "reported"},
		}}},
		{"exposure cap saturated", FindingSet{HasUsableData: true, MaxHops: 2,
			Findings: []Finding{
				exposure("cp-a", 1), exposure("cp-b", 1), exposure("cp-c", 1),
				exposure("cp-d", 1), exposure("cp-e", 1),
			}}},
		{"cross chain + stale + failed + not_evaluated", FindingSet{
			HasUsableData: true, Findings: []Finding{
				{Kind: KindCrossChainListing, Category: "SANCTIONED",
					Source: "ofac", Ref: "sanctions_entity:9@bsc"},
				{Kind: KindSnapshotStale, Source: "ofac", Ref: "sanctions_snapshot"},
				{Kind: KindProviderFailed, Source: "chainabuse", Ref: "chainabuse"},
				{Kind: KindNotEvaluated, Ref: "labels"},
			}}},
		{"combined formula cap 100", FindingSet{HasUsableData: true, MaxHops: 2, Findings: []Finding{
			{Kind: KindSanctionsDirect, Category: "SANCTIONED", Source: "ofac", Ref: "sdn-1"},
			labelFinding("HACK", "verified", "high", "fp"),
			exposure("c1", 1),
			{Kind: KindProviderScore, Source: "goplus", Ref: "goplus:score",
				ProviderScore: ptr(99)},
		}}},
		{"unknown kind surfaced", FindingSet{HasUsableData: true, Findings: []Finding{
			{Kind: "future_kind", Source: "x", Ref: "y"},
		}}},
	}
}

// TestGoldenV1 asserts EvaluateFor(fs, PolicyV1) marshals byte-identical
// to the stored v1 verdict for every corpus entry — the frozen-contract
// proof for policy v1.1's zero-drift requirement.
func TestGoldenV1(t *testing.T) {
	if *updateGolden {
		var entries []goldenEntry
		for _, c := range goldenCorpus() {
			fsJSON, err := json.Marshal(c.fs)
			require.NoError(t, err)
			// Evaluate is the v1 entry — the corpus pins the baseline.
			vJSON, err := json.Marshal(Evaluate(c.fs))
			require.NoError(t, err)
			entries = append(entries, goldenEntry{
				Name: c.name, FindingSet: fsJSON, Verdict: vJSON})
		}
		raw, err := json.MarshalIndent(entries, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(goldenPath), 0o755))
		require.NoError(t, os.WriteFile(goldenPath, append(raw, '\n'), 0o644))
		t.Fatalf("regenerated %s — review the diff into git", goldenPath)
	}

	raw, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "golden corpus missing — generate with -update")
	var entries []goldenEntry
	require.NoError(t, json.Unmarshal(raw, &entries))
	require.NotEmpty(t, entries)

	for _, e := range entries {
		var fs FindingSet
		require.NoError(t, json.Unmarshal(e.FindingSet, &fs), e.Name)
		got, err := json.Marshal(EvaluateFor(fs, PolicyV1))
		require.NoError(t, err)
		// MarshalIndent re-indents RawMessage inside the corpus file —
		// compact the stored verdict for a canonical byte comparison.
		var want bytes.Buffer
		require.NoError(t, json.Compact(&want, e.Verdict), e.Name)
		assert.Equal(t, want.String(), string(got),
			"v1 verdict drifted for %q", e.Name)
	}
}
