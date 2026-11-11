package scoring

import "strings"

// PolicyV1_1 is scoring policy "diversity-weighted exposure"
// (docs/exposure-scoring-v1.1-proposal.md): distinct-risky-source
// weighting computed from indexed transactions at screening time.
// v1.1 is a NEW version — PolicyV1 output stays byte-identical.
const PolicyV1_1 = "v1.1.0"

// KnownPolicies is the ordered registry of scoring policies — the
// single source of truth consumed by config boot validation, the
// per-request options.policy_version check, and EvaluateFor dispatch.
var KnownPolicies = []string{PolicyV1, PolicyV1_1}

// KnownPolicy reports whether v is a registered scoring policy.
func KnownPolicy(v string) bool {
	for _, p := range KnownPolicies {
		if p == v {
			return true
		}
	}
	return false
}

// v1.1 constants — one auditable policy unit (proposal §3.2–§3.4).
const (
	// exposureCapV1_1 bounds the exposure block (was 20 under v1).
	exposureCapV1_1 = 70
	// exposureSourceBaseV1_1 is the per-distinct-source base weight.
	exposureSourceBaseV1_1 = 30
	// exposureSourceFreqV1_1 is the bonus per extra meaningful inbound
	// transfer from the same source.
	exposureSourceFreqV1_1 = 7
	// exposureReportedSourceV1_1 is the flat weight of a
	// provider-reported-only source (no tx-level data to count).
	exposureReportedSourceV1_1 = 21
	// meaningfulFloorUSDV1_1 is the materiality floor in whole USD —
	// inbound transfers priced below it never count as meaningful.
	meaningfulFloorUSDV1_1 = 100

	// ExposureScanLimitV1_1 bounds rows per counterparty scan
	// (matches the TracingMaxEdges precedent).
	ExposureScanLimitV1_1 = 5000
	// ExposureMaxSourcesV1_1 bounds distinct sources evaluated.
	ExposureMaxSourcesV1_1 = 256
	// ExposureMaxDepth2SendersV1_1 bounds the depth-2 sender set fed
	// to the chain-taint bulk check.
	ExposureMaxDepth2SendersV1_1 = 512
	// ExposureEvidenceTxsV1_1 bounds tx hashes carried per finding.
	ExposureEvidenceTxsV1_1 = 10
)

// directTaintCategoriesV1_1 is the proposal §3.1 direct-taint set —
// the same 11 codes as baseWeightsV1 — tested verbatim.
var directTaintCategoriesV1_1 = map[string]bool{
	"RANSOMWARE":       true,
	"HACK":             true,
	"PHISHING":         true,
	"TERRORISM":        true,
	"SCAM":             true,
	"DRAINER":          true,
	"SANCTIONED":       true,
	"MONEY_LAUNDERING": true,
	"DARKNET":          true,
	"EXTREMISM":        true,
	"FROZEN":           true,
}

// serviceClassificationsV1_1 is the proposal §3.4 suppression list —
// classifications, not crimes: a service-classified candidate's
// exposure is business-model adjacency, informational only.
var serviceClassificationsV1_1 = map[string]bool{
	"EXCHANGE": true,
	"GAMBLING": true,
	"MIXER":    true,
}

// DirectTaintCategoryV1_1 reports whether cat is a proposal §3.1
// direct-taint category (case-insensitive).
func DirectTaintCategoryV1_1(cat string) bool {
	return directTaintCategoriesV1_1[strings.ToUpper(cat)]
}

// ServiceClassificationV1_1 reports whether cat is a proposal §3.4
// service classification (case-insensitive).
func ServiceClassificationV1_1(cat string) bool {
	return serviceClassificationsV1_1[strings.ToUpper(cat)]
}

// TaintWeightV1_1 returns a taint label's ordering weight (the v1 base
// weight — highest wins for multi-label sources, proposal A13).
func TaintWeightV1_1(cat string) int {
	return baseWeightsV1[strings.ToUpper(cat)]
}
