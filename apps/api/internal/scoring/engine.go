package scoring

import (
	"sort"
)

// Evaluate scores a FindingSet under PolicyV1. Pure and deterministic:
// findings are sorted before dedup and all output ordering is fixed.
func Evaluate(fs FindingSet) Verdict {
	findings := dedupe(fs.Findings)

	var reasons []Reason
	var flags []string
	directMax := 0
	exposureSum := 0
	otherMax := 0
	var otherBest *Finding

	for i := range findings {
		f := findings[i]
		switch f.Kind {
		case KindSanctionsDirect:
			// Official direct match: flat 100, factors bypassed —
			// an OFAC listing is not discounted by confidence.
			directMax = max(directMax, directSanctionsFlatScore)
			flags = appendFlag(flags, FlagDirectSanctions)
			reasons = append(reasons, Reason{
				Code:               ReasonDirectSanctionsMatch,
				Category:           f.Category,
				VerificationStatus: VerificationVerified,
				Weight:             directSanctionsFlatScore,
				EvidenceRef:        f.Ref,
			})
		case KindLabel:
			base, scored := baseWeightsV1[f.Category]
			if !scored {
				// Classification, not a crime: service/entity/other
				// categories and unknown codes carry no weight.
				reasons = append(reasons, Reason{
					Code:               ReasonLabelClassification,
					Category:           f.Category,
					VerificationStatus: f.VerificationStatus,
					Weight:             0,
					EvidenceRef:        f.Ref,
				})
				continue
			}
			w := weighted(base, f.VerificationStatus, f.Confidence)
			directMax = max(directMax, w)
			reasons = append(reasons, Reason{
				Code:               ReasonDirectLabel,
				Category:           f.Category,
				VerificationStatus: f.VerificationStatus,
				Weight:             w,
				EvidenceRef:        f.Ref,
			})
		case KindProviderObservation:
			// Same category table as labels — provider-reported, so the
			// reported factor applies; never reappears in the other
			// block (single-block membership).
			w := 0
			if base, scored := baseWeightsV1[f.Category]; scored {
				w = weighted(base, f.VerificationStatus, f.Confidence)
				directMax = max(directMax, w)
			}
			reasons = append(reasons, Reason{
				Code:               ReasonProviderObservation,
				Category:           f.Category,
				VerificationStatus: f.VerificationStatus,
				Weight:             w,
				EvidenceRef:        f.Ref,
			})
		case KindExposure:
			w := exposureWeight(f, fs.MaxHops)
			exposureSum += w
			reasons = append(reasons, Reason{
				Code:               ReasonExposureProvider,
				Category:           f.Category,
				VerificationStatus: f.VerificationStatus,
				Weight:             w,
				EvidenceRef:        f.Ref,
			})
		case KindProviderScore:
			if f.ProviderScore != nil && *f.ProviderScore > otherMax {
				otherMax = *f.ProviderScore
				best := f
				otherBest = &best
			}
		case KindCrossChainListing:
			reasons = append(reasons, zeroReason(ReasonCrossChainListing, f))
		case KindSnapshotStale:
			reasons = append(reasons, zeroReason(ReasonSnapshotStale, f))
		case KindProviderFailed:
			reasons = append(reasons, zeroReason(ReasonProviderFailed, f))
		case KindNotEvaluated:
			reasons = append(reasons, zeroReason(ReasonNotEvaluated, f))
		default:
			// Unknown kinds are surfaced, never silently dropped.
			reasons = append(reasons, zeroReason(ReasonNotEvaluated, f))
		}
	}

	// Other block: the maximum provider score maps to its band — max,
	// never sum. The contributing finding carries the weight; every
	// other provider score is reported at 0.
	for i := range findings {
		f := findings[i]
		if f.Kind != KindProviderScore {
			continue
		}
		w := 0
		if otherBest != nil && f.DedupKey() == otherBest.DedupKey() {
			w = providerScoreBandV1(otherMax)
		}
		rs := Reason{
			Code:        ReasonProviderScore,
			Category:    f.Category,
			Weight:      w,
			EvidenceRef: f.Ref,
		}
		rs.VerificationStatus = f.VerificationStatus
		reasons = append(reasons, rs)
	}

	exposureSub := min(exposureCapV1, exposureSum)
	otherSub := min(otherCapV1, providerScoreBandV1(otherMax))

	v := Verdict{
		PolicyVersion: PolicyV1,
		Flags:         flags,
		Blocks: Blocks{
			// Direct block is max-of-weights — no cap rule exists in v1,
			// so Capped stays false by design. Other.Capped is computed
			// against the pre-cap value so it binds correctly when
			// further signals join the block (band ≤ 20 < cap 30 today).
			Direct:   BlockScore{Subtotal: directMax},
			Exposure: BlockScore{Subtotal: exposureSub, Capped: exposureSum > exposureCapV1},
			Other:    BlockScore{Subtotal: otherSub, Capped: providerScoreBandV1(otherMax) > otherCapV1},
		},
		Reasons: orderReasons(reasons),
	}

	if fs.HasUsableData {
		score := min(100, directMax+exposureSub+otherSub)
		v.Score = &score
		v.RiskLevel = level(score)
	} else {
		// No usable dimension: NULL score, UNKNOWN level — and block
		// subtotals stay zero so a self-contradicting shape like
		// blocks.direct.subtotal:100 beside risk_score:null can never
		// leak under upstream contract drift. Reasons still report
		// what was observed; they carry no scoring claim.
		v.RiskLevel = RiskUnknown
		v.Blocks = Blocks{}
	}
	return v
}

// weighted applies weight = round(base × verification × confidence)
// in integer tenths: (base × vf × cf + 50) / 100 rounds half-up.
// Missing/unknown statuses degrade to unverified/unknown — never
// upgraded, never silently zero (except disputed, which IS zero).
func weighted(base int, verification, confidence string) int {
	vf, ok := verificationFactorsV1[verification]
	if !ok {
		vf = verificationFactorsV1[VerificationUnverified]
	}
	cf, ok := confidenceFactorsV1[confidence]
	if !ok {
		cf = confidenceFactorsV1[ConfidenceUnknown]
	}
	return (base*vf*cf + 50) / 100
}

// exposureWeight scores provider-reported hops: 1→10, 2→5, each × the
// reported factor (0.7). hops>2, hops>maxHops, and non-provider bases
// are informational only — listed, never scored.
func exposureWeight(f Finding, maxHops int) int {
	if f.Basis != BasisProviderReported || f.Hops > maxHops {
		return 0
	}
	switch f.Hops {
	case 1:
		return (exposureHop1WeightV1*verificationFactorsV1[VerificationReported] + 5) / 10
	case 2:
		return (exposureHop2WeightV1*verificationFactorsV1[VerificationReported] + 5) / 10
	default:
		return 0
	}
}

// dedupe sorts findings by a total order and drops duplicate dedup
// keys, keeping the strongest-evidence representative: explicit
// strength ranks (verified > reported > unverified > disputed, then
// high > medium > low > unknown confidence) — never lexical order,
// which alphabetically ranks "disputed" first and would discount the
// strongest evidence to zero.
func dedupe(in []Finding) []Finding {
	out := make([]Finding, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := out[i].DedupKey(), out[j].DedupKey()
		if ki != kj {
			return ki < kj
		}
		if vi, vj := verificationRank(out[i].VerificationStatus),
			verificationRank(out[j].VerificationStatus); vi != vj {
			return vi > vj
		}
		if ci, cj := confidenceRank(out[i].Confidence),
			confidenceRank(out[j].Confidence); ci != cj {
			return ci > cj
		}
		if out[i].Hops != out[j].Hops {
			return out[i].Hops < out[j].Hops
		}
		// Highest provider score survives — the tiebreak axis on which
		// same-key KindProviderScore findings differ at all.
		si, sj := 0, 0
		if out[i].ProviderScore != nil {
			si = *out[i].ProviderScore
		}
		if out[j].ProviderScore != nil {
			sj = *out[j].ProviderScore
		}
		return si > sj
	})
	deduped := out[:0]
	var prev string
	for i, f := range out {
		if i > 0 && f.DedupKey() == prev {
			continue
		}
		prev = f.DedupKey()
		deduped = append(deduped, f)
	}
	return deduped
}

// verificationRank orders evidence strength for dedup representatives;
// unknown/missing statuses rank at the bottom — never upgraded.
func verificationRank(status string) int {
	switch status {
	case VerificationVerified:
		return 4
	case VerificationReported:
		return 3
	case VerificationUnverified:
		return 2
	case VerificationDisputed:
		return 1
	default:
		return 0
	}
}

// confidenceRank mirrors verificationRank for the confidence axis.
func confidenceRank(confidence string) int {
	switch confidence {
	case ConfidenceHigh:
		return 4
	case ConfidenceMedium:
		return 3
	case ConfidenceLow:
		return 2
	case ConfidenceUnknown:
		return 1
	default:
		return 0
	}
}

// orderReasons fixes reason order: weight desc, then code, category,
// ref — biggest contributors first, fully deterministic.
func orderReasons(rs []Reason) []Reason {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Weight != rs[j].Weight {
			return rs[i].Weight > rs[j].Weight
		}
		if rs[i].Code != rs[j].Code {
			return rs[i].Code < rs[j].Code
		}
		if rs[i].Category != rs[j].Category {
			return rs[i].Category < rs[j].Category
		}
		return rs[i].EvidenceRef < rs[j].EvidenceRef
	})
	return rs
}

func zeroReason(code string, f Finding) Reason {
	return Reason{
		Code:               code,
		Category:           f.Category,
		VerificationStatus: f.VerificationStatus,
		Weight:             0,
		EvidenceRef:        f.Ref,
	}
}

func appendFlag(flags []string, flag string) []string {
	for _, f := range flags {
		if f == flag {
			return flags
		}
	}
	return append(flags, flag)
}

func level(score int) string {
	switch {
	case score >= 80:
		return RiskCritical
	case score >= 60:
		return RiskHigh
	case score >= 30:
		return RiskMedium
	default:
		return RiskLow
	}
}
