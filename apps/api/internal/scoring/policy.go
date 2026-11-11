package scoring

// PolicyV1 is the original scoring policy version (spec §10.2),
// registered in the KnownPolicies registry beside PolicyV1_1. Scores
// are reproducible by policy version — any weight change is a NEW
// version, never a silent edit.
const PolicyV1 = "v1.0.0"

// §10.1 risk-dimension → mechanism map:
//
//	Direct Sanctions            → sanctions_direct finding, flat 100
//	Direct Malicious Labels     → baseWeights × verif × conf, direct block
//	Indirect Transaction Exposure → exposure block, provider-reported
//	                              hops≤2 only, cap 20
//	Service Classification      → zero-weight classification reasons,
//	                              never scored (GAMBLING/EXCHANGE/MIXER)
//	Data Confidence             → confidenceFactors, labels AND
//	                              provider observations
//	Data Freshness              → snapshot_stale informational reasons;
//	                              degraded data stays visible
//	Provider Coverage           → provider_failed/not_evaluated reasons;
//	                              absence of coverage ≠ clean
//
// Score formula (fixed precedence):
//
//	score = min(100, direct_max + min(20, exposure) + min(30, other))
//
// direct_max is the MAXIMUM direct weight — never a sum.
//
// Base weights by category code. Categories absent here (incl. every
// SERVICE_TYPE / ENTITY_TYPE / OTHER category and any dynamic upstream
// category) score 0 through the classification path with an explicit
// informational reason — never a silent weight.
var baseWeightsV1 = map[string]int{
	"RANSOMWARE":       95,
	"HACK":             90,
	"PHISHING":         90,
	"TERRORISM":        90,
	"SCAM":             85,
	"DRAINER":          85,
	"SANCTIONED":       85,
	"MONEY_LAUNDERING": 80,
	"DARKNET":          75,
	"EXTREMISM":        70,
	"FROZEN":           60,
}

// Verification factors (tenths — integer math keeps round() exact at
// half boundaries where float64 would drift, e.g. 85×0.7 = 59.5→60).
// Reported stays reported; only evidence upgrades a finding to verified.
var verificationFactorsV1 = map[string]int{
	VerificationVerified:   10,
	VerificationReported:   7,
	VerificationUnverified: 5,
	VerificationDisputed:   0,
}

// Confidence factors (tenths) — applies to labels AND provider
// observations.
var confidenceFactorsV1 = map[string]int{
	ConfidenceHigh:    10,
	ConfidenceMedium:  9,
	ConfidenceLow:     8,
	ConfidenceUnknown: 7,
}

const (
	directSanctionsFlatScore = 100

	exposureCapV1 = 20
	otherCapV1    = 30

	// Provider-reported exposure weights before the reported factor.
	exposureHop1WeightV1 = 10
	exposureHop2WeightV1 = 5
)

// providerScoreBandV1 maps the max provider score to its other-block
// contribution: ≥80 → 20, ≥50 → 10, else 0. Band, never a sum.
func providerScoreBandV1(score int) int {
	switch {
	case score >= 80:
		return 20
	case score >= 50:
		return 10
	default:
		return 0
	}
}
