package scoring

import (
	"sort"
	"strings"
)

// EvaluateFor dispatches a FindingSet to a registered scoring policy.
// PolicyV1 (and empty) runs the frozen v1 evaluator; PolicyV1_1 runs
// diversity-weighted exposure. Unknown policies fail closed: UNKNOWN
// level, NULL score, NOT_EVALUATED — never a fabricated score.
func EvaluateFor(fs FindingSet, policy string) Verdict {
	switch policy {
	case PolicyV1_1:
		return evaluateV11(fs)
	case PolicyV1, "":
		return Evaluate(fs)
	default:
		return Verdict{
			PolicyVersion: policy,
			RiskLevel:     RiskUnknown,
			Reasons: []Reason{{
				Code:   ReasonNotEvaluated,
				Detail: "unknown scoring policy",
			}},
		}
	}
}

// evaluateV11 scores under policy v1.1: the direct/other blocks are
// identical to v1; the exposure block is rebuilt around distinct
// risky-source weighting with meaningful-transfer frequency.
func evaluateV11(fs FindingSet) Verdict {
	findings := dedupe(fs.Findings)

	var reasons []Reason
	var flags []string
	directMax := 0
	otherMax := 0
	var otherBest *Finding

	// Exposure fold — group INBOUND source-addressed findings, dedupe
	// basis. OUTBOUND findings can never join a group (A9): they route
	// to the ungrouped informational path, so they can never suppress
	// an inbound sibling's weight either.
	winners := map[string]Finding{}
	var winnerOrder []string
	var ungrouped, suppressed []Finding
	for i := range findings {
		f := findings[i]
		if f.Kind != KindExposure {
			continue
		}
		addr := strings.ToLower(f.SourceAddress)
		if addr == "" || f.Direction == "OUTBOUND" {
			ungrouped = append(ungrouped, f)
			continue
		}
		cur, ok := winners[addr]
		if !ok {
			winners[addr] = f
			winnerOrder = append(winnerOrder, addr)
			continue
		}
		if exposureWinnerRank(f) > exposureWinnerRank(cur) {
			winners[addr] = f
			suppressed = append(suppressed, cur)
		} else {
			suppressed = append(suppressed, f)
		}
	}
	sort.Strings(winnerOrder)
	sort.Slice(suppressed, func(i, j int) bool {
		return suppressed[i].Ref < suppressed[j].Ref
	})

	exposureSum := 0
	exposureReasons := []Reason{}
	appendExp := func(code string, f Finding, w int, detail string) {
		exposureReasons = append(exposureReasons, Reason{
			Code:               code,
			Category:           f.Category,
			VerificationStatus: f.VerificationStatus,
			Weight:             w,
			EvidenceRef:        f.Ref,
			Detail:             detail,
		})
	}

	// Service-classified candidate: every source reason keeps its code
	// at weight 0 plus ONE explicit informational reason (phase-04).
	svcGate := fs.CandidateServiceClassified
	if svcGate {
		appendExp(ReasonServiceClassifiedCandidate, Finding{
			Ref: "candidate"}, 0,
			"candidate service-classified; exposure suppressed")
	}
	for _, addr := range winnerOrder {
		f := winners[addr]
		w := 0
		var code string
		switch {
		case f.Direction == "OUTBOUND":
			code = ReasonExposureInformational // defense: never scores
		case f.Basis == BasisOnchainComputed:
			code = ReasonExposureOnchainSource
			if f.MeaningfulCount >= 1 && f.TaintKind != "" &&
				fs.MaxHops >= 1 {
				w = exposureSourceBaseV1_1 +
					exposureSourceFreqV1_1*(f.MeaningfulCount-1)
			}
		case f.Basis == BasisProviderReported:
			code = ReasonExposureProviderSource
			// B3: identification changes the weight, never the bound —
			// a named source with unknown hops (0) is adjacency-
			// eligible; explicit hops beyond max_hops score 0.
			if fs.MaxHops >= 1 && (f.Hops == 0 || f.Hops <= fs.MaxHops) {
				w = exposureReportedSourceV1_1
			}
		default:
			code = ReasonExposureInformational
		}
		if w == 0 && code == ReasonExposureOnchainSource {
			code = ReasonExposureInformational
		}
		if svcGate {
			w = 0
		}
		exposureSum += w
		appendExp(code, f, w, f.Reason)
	}
	// Suppressed losers stay listed at weight 0 — the dedupe detail
	// names the winning basis; partial winner coverage is surfaced.
	for _, f := range suppressed {
		addr := strings.ToLower(f.SourceAddress)
		w := winners[addr]
		detail := "suppressed_by_" + basisTag(w.Basis) + "_source"
		if w.Coverage == "partial" {
			detail += ";winner_coverage_partial"
		}
		appendExp(ReasonExposureInformational, f, 0, detail)
	}
	// Ungrouped findings: generic (sourceless) provider exposures keep
	// v1 hop weights; OUTBOUND findings are informational only.
	for _, f := range ungrouped {
		w := 0
		code := ReasonExposureProvider
		if f.Direction == "OUTBOUND" {
			code = ReasonExposureInformational
		} else if !svcGate {
			w = exposureWeight(f, fs.MaxHops)
		}
		exposureSum += w
		appendExp(code, f, w, f.Reason)
	}

	for i := range findings {
		f := findings[i]
		switch f.Kind {
		case KindSanctionsDirect:
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
		case KindProviderScore:
			if f.ProviderScore != nil && *f.ProviderScore > otherMax {
				otherMax = *f.ProviderScore
				best := f
				otherBest = &best
			}
		case KindProceedsReceiver:
			reasons = append(reasons, Reason{
				Code:               ReasonProceedsReceiver,
				Category:           f.Category,
				VerificationStatus: f.VerificationStatus,
				Weight:             0,
				EvidenceRef:        f.Ref,
				Detail:             f.Reason,
			})
		case KindPossibleVictimContact:
			reasons = append(reasons, Reason{
				Code:               ReasonPossibleVictimContact,
				Category:           f.Category,
				VerificationStatus: f.VerificationStatus,
				Weight:             0,
				EvidenceRef:        f.Ref,
				Detail:             f.Reason,
			})
		case KindExposure:
			// handled by the exposure fold above
		case KindCrossChainListing:
			reasons = append(reasons, zeroReason(ReasonCrossChainListing, f))
		case KindSnapshotStale:
			reasons = append(reasons, zeroReason(ReasonSnapshotStale, f))
		case KindProviderFailed:
			reasons = append(reasons, zeroReason(ReasonProviderFailed, f))
		case KindNotEvaluated:
			reasons = append(reasons, zeroReason(ReasonNotEvaluated, f))
		default:
			reasons = append(reasons, zeroReason(ReasonNotEvaluated, f))
		}
	}
	reasons = append(reasons, exposureReasons...)

	for i := range findings {
		f := findings[i]
		if f.Kind != KindProviderScore {
			continue
		}
		w := 0
		if otherBest != nil && f.DedupKey() == otherBest.DedupKey() {
			w = providerScoreBandV1(otherMax)
		}
		reasons = append(reasons, Reason{
			Code:               ReasonProviderScore,
			Category:           f.Category,
			VerificationStatus: f.VerificationStatus,
			Weight:             w,
			EvidenceRef:        f.Ref,
		})
	}

	exposureSub := min(exposureCapV1_1, exposureSum)
	otherSub := min(otherCapV1, providerScoreBandV1(otherMax))

	v := Verdict{
		PolicyVersion: PolicyV1_1,
		Flags:         flags,
		Blocks: Blocks{
			Direct:   BlockScore{Subtotal: directMax},
			Exposure: BlockScore{Subtotal: exposureSub, Capped: exposureSum > exposureCapV1_1},
			Other:    BlockScore{Subtotal: otherSub, Capped: providerScoreBandV1(otherMax) > otherCapV1},
		},
		Reasons: orderReasons(reasons),
	}

	if fs.HasUsableData {
		score := min(100, directMax+exposureSub+otherSub)
		v.Score = &score
		v.RiskLevel = level(score)
	} else {
		v.RiskLevel = RiskUnknown
		v.Blocks = Blocks{}
	}
	return v
}

// exposureWinnerRank orders same-source exposure findings: an
// onchain-computed basis always outranks provider-reported — even when
// the onchain winner scores zero (winner-zero suppression).
func exposureWinnerRank(f Finding) int {
	if f.Basis == BasisOnchainComputed {
		return 2
	}
	if f.Basis == BasisProviderReported {
		return 1
	}
	return 0
}

func basisTag(basis string) string {
	if basis == BasisOnchainComputed {
		return "onchain"
	}
	return "provider"
}
