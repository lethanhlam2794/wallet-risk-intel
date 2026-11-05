package sanctions

// reasonFor builds the spec §8.3 reason block. OFAC publishes no per-entity
// reason text, so officialReason is always nil for this source — callers
// pass it only when a source genuinely ships one. Programs alone yield
// PROGRAM_CONTEXT_ONLY; nothing yields NOT_AVAILABLE with a null summary.
// No text is ever synthesized.
func reasonFor(officialReason *string, programCount int) Reason {
	if officialReason != nil && *officialReason != "" {
		return Reason{Summary: officialReason, Status: ReasonOfficialExplicit}
	}
	if programCount > 0 {
		return Reason{Summary: nil, Status: ReasonProgramContextOnly}
	}
	return Reason{Summary: nil, Status: ReasonNotAvailable}
}
