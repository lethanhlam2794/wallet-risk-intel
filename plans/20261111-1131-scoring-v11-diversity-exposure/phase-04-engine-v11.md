---
phase: 4
title: "Engine v1.1 dispatch + golden v1 parity"
status: done
priority: P1
dependencies: [1]
---

# Phase 4: Engine v1.1 dispatch + golden v1 parity

## Overview

Add `EvaluateFor(fs, policy)` dispatching between the untouched v1
body and the new `evaluateV11` — source-address dedupe across bases,
per-source `30+7×(n−1)` formula, flat-21 reported-only sources, generic
v1-weight provider signals, cap 70, service-candidate suppression, and
the new informational kinds — plus a golden corpus proving v1 output is
byte-identical.

## Requirements

- Functional:
  - `Evaluate(fs)` stays the v1 entry — byte-identical behavior for
    every existing caller/test. Internally `= evaluateV1(fs)`.
  - `EvaluateFor(fs FindingSet, policy string) Verdict`:
    `PolicyV1` → `evaluateV1`; `PolicyV1_1` → `evaluateV11`;
    unknown → `Verdict{PolicyVersion: policy, RiskLevel: RiskUnknown,
    Score: nil, Reasons: [ReasonNotEvaluated]}` — never fabricates a
    score under an unregistered policy (defense in depth; callers
    pre-validate).
  - `evaluateV11` — same direct/other/informational handling as v1
    (shared helpers stay shared: `weighted`, `dedupe`,
    `providerScoreBandV1`, `orderReasons`, `level`, `appendFlag`,
    `zeroReason`), divergent ONLY in the exposure fold:
    - **Direction gate (A1)**: only `KindExposure` findings with
      `Direction=="INBOUND"` or unknown/empty direction enter source
      grouping; `Direction=="OUTBOUND"` can never join a group or score
      (defense-in-depth — the mapper already routes provider outbound
      to `KindPossibleVictimContact`; onchain sources are INBOUND by
      construction).
    - Group `KindExposure` findings by normalized `SourceAddress`
      (lowercased; empty-address findings are never grouped).
      Within a group, highest basis wins:
      `onchain_computed` > `provider_reported`; the winner scores,
      losers emit informational `EXPOSURE_INFORMATIONAL` reasons with
      `Detail="deduped_by_<winning_basis>"`, weight 0 — never stacked.
      **Winner-scores-zero rule (A9)**: an onchain-informational winner
      (n=0/dust/unpriced) still suppresses provider +21 — observed
      onchain transfers refute a provider materiality claim; when that
      winner carries `coverage="partial"` the suppression `Detail`
      notes the partial-coverage caveat explicitly.
    - `BasisOnchainComputed` ∧ `MeaningfulCount≥1` ∧ `TaintKind≠""` ∧
      `MaxHops≥1` → `w = 30 + 7×(n−1)`, code `EXPOSURE_ONCHAIN_SOURCE`.
      **`MaxHops` rule (A8)**: onchain sources carry `Hops=1`; scored
      iff `fs.MaxHops ≥ 1` — `MaxHops=0` → all informational. The
      depth-2 receiver check is taint evidence, not a scored hop, so
      `MaxHops=1` still evaluates chain-tainted sources fully.
      (`MeaningfulCount=0` or `Reason` set → informational weight 0,
      code `EXPOSURE_INFORMATIONAL`, `Detail=f.Reason`.)
    - `BasisProviderReported` ∧ `SourceAddress≠""` ∧ direction
      eligible ∧ `MaxHops≥1` ∧ `(f.Hops==0 || f.Hops ≤ fs.MaxHops)` →
      flat `21`, code `EXPOSURE_PROVIDER_SOURCE`. Explicit provider hop
      counts beyond the requested bound score 0 — identification
      changes the weight, never the bound (single semantic shared with
      the generic path); unknown provider hops (`Hops==0`) treated as
      adjacency-eligible since the provider named a specific source.
    - `BasisProviderReported` ∧ `SourceAddress==""` ∧ direction
      eligible → v1 generic path (`exposureWeight(f, fs.MaxHops)` —
      7/4 honoring MaxHops), code `EXPOSURE_PROVIDER`.
    - `Finding.VerificationStatus` passthrough: the engine reports the
      status the analyzer stamped (sanctions→verified, label→its own)
      — it never upgrades (A6 engine-side).
    - Candidate service-classified = `fs.CandidateServiceClassified`
      (dedicated input channel — NOT `KindLabel` inspection, A4) → ALL
      exposure weights forced 0 (each source reason keeps its code at
      weight 0) + one `SERVICE_CLASSIFIED_CANDIDATE` informational
      reason.
    - `KindProceedsReceiver` / `KindPossibleVictimContact` → weight-0
      reasons `PROCEEDS_RECEIVER` / `POSSIBLE_VICTIM_CONTACT`.
    - `exposure = min(70, Σ weights)`; `Blocks.Exposure.Capped = sum>70`.
    - `score = min(100, directMax + exposure + min(30, other))` —
      same shape as v1, `PolicyVersion = "v1.1.0"`.
  - `HasUsableData=false` → same NULL-score contract as v1 (blocks
    zeroed, reasons still listed).
- Non-functional:
  - Deterministic: findings sorted before grouping; reason order via
    existing `orderReasons`; no map-iteration leakage into output.
  - Pure function — no IO, no clock, no new imports beyond `sort`.

## Architecture

`engine.go` gets a thin dispatch function and keeps `Evaluate`'s body as
`evaluateV1`; ALL v1.1 logic lives in a new `engine_v11.go` (exposure
fold + verdict assembly reusing shared helpers). This keeps the v1 diff
at zero and the v1.1 surface auditable as one file.

Golden parity: `internal/scoring/testdata/v1_golden.json` holds
`{name, findingset, verdict_json}` entries generated from the CURRENT
v1 engine for a broad corpus (empty, each kind, dedup edge cases,
exposure mixes, caps, no-usable-data). `TestGoldenV1` asserts
`EvaluateFor(fs, "v1.0.0")` marshals byte-identical to the stored
verdict for every entry. The corpus file is generated once by a
`go test -run TestGoldenV1 -update`-style flag (standard pattern) and
reviewed into git — never regenerated silently.

## Related Code Files

- Create: `apps/api/internal/scoring/engine_v11.go`,
  `apps/api/internal/scoring/engine_v11_test.go`,
  `apps/api/internal/scoring/testdata/v1_golden.json`,
  `apps/api/internal/scoring/golden_test.go`
- Modify: `apps/api/internal/scoring/engine.go` (rename body →
  `evaluateV1`, add `Evaluate` alias + `EvaluateFor` dispatch — the
  only tolerated edit)
- Modify: `apps/api/internal/screening/service.go` (call
  `scoring.EvaluateFor(fs, policy)`)

## Implementation Steps (TDD)

1. Red: `engine_v11_test.go` — every acceptance criterion:
   - 1 source n=1 → +30; n=2 → +37; n=6 → +65; n=7 → 70 cap;
     2 sources n=1 → 60; 3 sources → 70; 50 txs → 70 cap;
   - provider-only source → +21 (any count); same source both bases →
     onchain formula NOT 51; dedupe informational reasons emitted;
   - **direction gate (A1)**: `KindExposure{Direction:"OUTBOUND",
     SourceAddress}` → never grouped, never scored; unknown-direction
     provider source → eligible +21; `KindPossibleVictimContact` →
     informational reason;
   - **MaxHops rule (A8)**: onchain source at `MaxHops=0` → w0;
     `MaxHops=1` → full formula incl. chain-tainted;
   - **winner-zero (A9)**: onchain n=0 winner suppresses provider +21
     of same address → exposure stays 0; partial-coverage winner's
     `Detail` carries the caveat;
   - below-floor → informational `below_materiality_floor` w0;
     unpriced → `unpriced_asset` w0;
   - chain-taint source scores identically to direct (+30…);
   - service-classified candidate (`fs.CandidateServiceClassified`) →
     all exposure w0 + `SERVICE_CLASSIFIED_CANDIDATE`; a mere
     provider-observation "EXCHANGE" without the flag does NOT suppress;
   - cap via diversity (3×30) and frequency (n≥7);
   - generic provider signal (no SourceAddress) → 7/4 inside same block;
   - zero meaningful inbound → exposure subtotal 0, informational
     reason present;
   - `VerificationStatus` passthrough — reported-backed finding's
     reason keeps `reported`, never upgraded;
   - v1.1 verdict carries `policy_version:"v1.1.0"`, blocks exposure
     cap fields correct.
2. Red: `golden_test.go` — corpus file present, all entries
   byte-identical under v1 selection; `EvaluateFor` unknown → nil score.
3. Red: `service_test.go` — `EvaluateFor` receives resolved policy;
   v1 request on v1.1-configured service still scores v1 (and vice
   versa); persisted `scoring_policy_version` follows resolution;
   **envelope parity (A5)**: a v1-selected Result serializes with zero
   new keys vs pre-change baseline.
4. Green: implement dispatch + `evaluateV11` + golden corpus.
5. Full scoring suite green; gofmt/vet clean.

## Success Criteria

- [ ] All prompt acceptance criteria map to named tests, all green
- [ ] `git diff internal/scoring/engine.go` shows ONLY
      rename+dispatch (no weight/logic drift)
- [ ] Golden test proves `EvaluateFor(·,"v1.0.0")` ≡ stored v1 output
- [ ] Shuffle-determinism test extended to v1.1 (same FindingSet →
      byte-identical Verdict)
- [ ] `EvaluateFor` on `"v9.9.9"` → `score=null, risk_level=UNKNOWN`

## Risk Assessment

- v1.1 dedupe grouping non-determinism → sort findings by (address,
  basis-rank, DedupKey) before grouping; output order via
  `orderReasons`.
- Same address both bases: "highest basis wins" must not stack 30+21 →
  explicit group-winner logic + regression test asserting NOT 51.
- `MeaningfulCount` spoofed by malformed finding (n<0 / missing basis)
  → guard: onchain basis requires `TaintKind≠""` ∧ `n≥1` ∧
  `Direction` INBOUND/empty ∧ `MaxHops≥1`, else informational — engine
  never trusts fields blindly.
- Direction-less legacy findings (constructed before the field existed)
  → unknown direction is eligible (adjacency semantics), positively
  OUTBOUND never is — test both.
- Exposure block over/under-flow on huge source counts → int math,
  cap applied post-sum; no overflow risk at 256-source bound.
