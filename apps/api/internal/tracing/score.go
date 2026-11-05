package tracing

import (
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/sanctions"
)

// ExposurePolicyV1 is the only exposure scoring version (phase 7,
// plan §4). It is a per-item evidence weight — never folded into
// screening_results.risk_score and never a verdict on its own.
const ExposurePolicyV1 = "exposure-v1"

// score = min(100, Σ_rows base(kind, code) × decay(hops) × vf × cf)
//
// Every term rounds per-row INTEGER half-up under the fixed denominator
// 10(vf) × 10(cf) × 16(decay) = 1600 — the three-factor extension of
// engine.go weighted()'s integer-tenths rule. The Σ is literal over
// evidence rows (no per-kind dedup, no mirror-source collapsing);
// min(100) is the only bound — a declared Σ-additive deviation from
// Phase-5's max-block. A sum of zero means every signal is
// informational-only → exposure_score is null (never 0 on the wire).
const exposureDenom = 1600
const exposureHalf = 800

// decay(hops) in sixteenths: hop1 1.0, hop2 0.5, hop3 0.25, hop4
// 0.125, hop5 0.0625 — monotone decreasing (spec §11 reduced weight).
var exposureDecayNum = [5]int{16, 8, 4, 2, 1}

// Verification factors (tenths) — Phase-5 semantics: reported stays
// reported; only evidence upgrades verification; disputed contributes 0.
var exposureVF = map[string]int{
	"verified":   10,
	"reported":   7,
	"unverified": 5,
	"disputed":   0,
}

// Confidence factors (tenths) — Phase-5 parity.
var exposureCF = map[string]int{
	"high":    10,
	"medium":  9,
	"low":     8,
	"unknown": 7,
}

// labelEvidence is one label row feeding matched_signals and the
// exposure sum — local and foreign-network rows share the shape;
// ForeignNet ≠ "" marks the foreign row (skips consider(), carries
// network + listed_address provenance on the wire signal).
type labelEvidence struct {
	CategoryCode string
	CategoryType string
	Label        string
	Verification string
	Confidence   string
	EntityID     pgtype.Int8
	EntityName   pgtype.Text
	Source       string
	ForeignNet   string // foreign network code; "" = request network
	ListedAddr   string // foreign wallets.address original representation
}

// exposureLabelBase implements base("label", code) — the code exception
// precedes the tier: SANCTIONED labels score 85 (community claim, not an
// OFAC listing — distinct from sanctions_direct by kind); the rest map
// by category_type: MALICIOUS_ACTIVITY → 70; every other tier → 0
// (SERVICE_TYPE/ENTITY_TYPE/LEGAL/OTHER are classifications only).
func exposureLabelBase(code, categoryType string) int {
	if code == CatSanctioned {
		return 85
	}
	if strings.EqualFold(categoryType, "MALICIOUS_ACTIVITY") {
		return 70
	}
	return 0
}

func exposureDecay(hops int) int {
	if hops < 1 {
		return exposureDecayNum[0]
	}
	if hops > len(exposureDecayNum) {
		return exposureDecayNum[len(exposureDecayNum)-1]
	}
	return exposureDecayNum[hops-1]
}

// exposureScore computes the exposure-v1 weight for one item over the
// same evidence rows that produced matched_signals.
func exposureScore(hops int, labels []labelEvidence,
	lookup *sanctions.WalletLookup) *int {

	d := exposureDecay(hops)
	sum := 0
	term := func(base, vf, cf int) {
		sum += (base*vf*cf*d + exposureHalf) / exposureDenom
	}
	if lookup != nil {
		for range lookup.Sanctions {
			term(100, 10, 10) // sanctions_direct: flat 100, vf=cf=1.0
		}
		for range lookup.CrossChainObservations {
			term(20, 10, 10) // listing (cross-chain): 20, vf=cf=1.0
		}
		for range lookup.UnresolvedNetworkListings {
			term(20, 10, 10)
		}
	}
	for _, l := range labels {
		vf, ok := exposureVF[l.Verification]
		if !ok {
			vf = exposureVF["unverified"]
		}
		cf, ok := exposureCF[l.Confidence]
		if !ok {
			cf = exposureCF["unknown"]
		}
		term(exposureLabelBase(l.CategoryCode, l.CategoryType), vf, cf)
	}
	if sum == 0 {
		return nil
	}
	if sum > 100 {
		sum = 100
	}
	return &sum
}
