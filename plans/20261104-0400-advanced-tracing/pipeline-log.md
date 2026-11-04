# Pipeline log — Phase 7 Advanced Tracing (codex-vibe --auto)

## STAGE_1 — plan + adversarial debate: PASSED

- Plan: `plan.md` v3 (self-contained) + `phase-01-frontier-and-scoring.md`
  + `phase-02-crosschain-and-api.md`.
- Critic debate: **7 rounds → PLAN_CONSENSUS_REACHED**
  (reports/critique-round-{1..7}.md + rebuttal-round-{1..5}.md).
- Cumulative findings applied: 4 blockers (B1 frontier ambiguity,
  B2 scoring vocabulary, B3 unreachable null, B1′ visited off-by-one),
  16 majors, ~30 minors/nits, 11 test gaps → 35 pinned tests.
- Key pinned mechanics: Reading-A frontier (clean intermediaries seed
  deeper); two-book cpAgg/frontier split; dedup snapshot ≤ d−2
  post-direction-filter at level 1; materialized-vs-scheduled
  coverage domains; forward-DFS tuple-order enumeration with
  reachability mask; exposure-v1 = min(100, Σ base×vf×cf×decay),
  per-term integer rounding, null-if-all-zero; cross-chain =
  foreign labels only, kind:"label"+network, never consider(),
  resolver-scoped ANY($2); max_hops 1–5 (screenings stays 0–2);
  bit-identical at max_hops≤2 incl. direction interplay.
- Critic write-scope verified: only `reports/` written.

## STAGE_2 — Devin Cook: dispatched

## STAGE_2 — Devin Cook: PASSED (independently verified)

- Implemented plan v3 end-to-end: per-depth expansion loop, two-book
  cpAgg/frontier, dedup snapshot, forward-DFS paths, coverage
  vocabulary, exposure-v1 scorer, cross-chain labels, all wiring.
- 35/35 pinned tests PASS + Phase-6 suite green (bit-identical ≤2).
- 1 documented deviation: loop-top `budget.Exhausted()` pull-gate
  removed (§6-28 otherwise inexpressible; identical accounting via
  `out.BudgetExhausted`) — for Review scrutiny.
- Independent verify: gofmt clean, `go build`, `go vet`,
  `go test ./internal/tracing ./tests` — all green.

## STAGE_3 — Devin Review: dispatched (round 1)

## STAGE_3 — Devin Review: PASSED (round 1 APPROVED)

- Independent review ran its own gates: tests/race/vet/gofmt all
  green; cook's budget-gate deviation verified clean (never-attempted
  accounting preserved via `out.BudgetExhausted`; free-path
  resolutions bounded; runtime-pinned by BeyondPrefixGlimpse).
- Verdict APPROVED: 0 blockers, 0 majors, 2 minors + 4 nits — all
  documentation-level.

## STAGE_4 — Remediation + saturation: PASSED

- All round-1 findings applied (`reports/remediation-round-1.md`).
- Bonus structural fix found during remediation: duplicate
  `ErrorEnvelope` + 18 invalid `nullable:` usages → OpenAPI now
  redocly-valid (0 errors).
- Gates re-run: gofmt/vet/build/tests/OpenAPI all green.

## STAGE_5 — E2E verification: in progress

## STAGE_5 — E2E verification: PASSED

Real API (:8091, dev DB + Redis, providers disabled) against a
SQL-seeded 3-hop graph root→A→B→C with a local SCAM label on C and a
BSC HACK label (checksum-case original address):

- `GET /exposures?max_hops=3` → 1 item: C, `indirect_hop`, `hops=3`,
  `category=SCAM` (local wins; foreign HACK skipped `consider()`),
  signals = local SCAM + foreign HACK with `network:"bsc"` +
  `listed_address` = checksum-case ORIGINAL representation.
- `exposure_score=29` — exact arithmetic: SCAM 70×0.25×1.0×1.0→18 +
  HACK 70×0.25×0.7×0.9→11 = 29; `exposure_policy_version:"exposure-v1"`.
- Path evidence: 3-edge path, `path_verified:false`,
  `temporal_order_valid:true`, real tx hashes; `transaction_evidence`
  deduped.
- Coverage: `counterparties_total=2`, `nodes_examined=2`,
  `frontier_by_hop {2:1,3:1}`, `nodes_examined_by_hop {2:1,3:1}`,
  `max_depth_reached=3`, `node_indexed_until` spans both attempted
  positions, `provider_calls=0` (fresh-serve).
- `max_hops=1` → total=1, remaining=1, `max_depth_reached=0`,
  `frontier_by_hop={2:1}` — Phase-6 materialized-frontier parity.
- `max_hops=0|6` → 400 BAD_REQUEST.
- `direction=in` → 0 items/0 counterparties (hop-1 filter correct);
  `direction=out` → same 3-hop item (deep edges unfiltered).
- Unknown wallet → `index_status=disabled`, `provider_status=disabled`,
  `provider_calls=0`, `items:[]`, `exposure_policy_version` emitted.
- SQL: seeded wallets stay `fresh` rows_indexed=0; unknown wallet row
  written `status=disabled,error=disabled`.
- Latency ~24 ms for the 3-hop traced response.
- Captures: `docs/feature-maps/assets/live-exposures-3hop.json` +
  `live-exposures-advanced-disabled.json`.

## STAGE_6 — guidebook + feature map: in progress

## STAGE_6 — guidebook + feature map + final report: PASSED

- `docs/guidebook-transaction-tracing.md` + feature map updated for
  3–5 hop tracing, exposure-v1, cross-chain labels, per-hop coverage;
  live captures linked.
- PHASE 7 COMPLETE — 6/6 stages.
