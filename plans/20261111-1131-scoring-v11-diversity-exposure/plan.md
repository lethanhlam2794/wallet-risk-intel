---
plan: scoring-v11-diversity-exposure
title: "Scoring Policy v1.1 — Diversity-Weighted Exposure"
status: pending
created: 2026-11-11
source-prompt: docs/prompts/20261110-1724-codex-exposure-v11-diversity-weighted.md
binding-contract: docs/exposure-scoring-v1.1-proposal.md
blockedBy: []
---

# Scoring Policy v1.1 — Diversity-Weighted Exposure

## Overview

Implement scoring policy `v1.1.0` per `docs/exposure-scoring-v1.1-proposal.md`
(the binding contract): replace v1's flat hop-weighted provider exposure with
**distinct-risky-source weighting** computed from the indexed `transactions`
table at screening time. v1.1 is a NEW policy version selected via
`SCORING_POLICY` (default `v1.0.0`) or per-request `options.policy_version`;
v1 output stays byte-identical under v1 selection.

Formula (v1.1): `score = min(100, direct_max + min(70, exposure) + min(30, other))`
where `exposure = Σ per_source`, `per_source = 30 + 7×(n−1)` for
onchain-verified tainted sources (n = confirmed meaningful inbound transfers
R→X priced ≥ $100 floor), flat `+21` for provider-reported-only sources, and
v1 hop weights for generic provider signals without an identified
counterparty. Hop distance is retired as a scoring axis under v1.1.

## Architecture

```
POST /screenings (options.policy_version?)
        │
        ▼
screening.Service.run ── policy = req override | cfg.ScoringPolicy
        │
        ├─► (unchanged) sanctions / labels / enrichment gather
        │
        ├─► NEW exposureAnalyzer (v1.1 ∧ include_exposure only)
        │     indexed txs only — NO live indexing, NO provider calls
        │     candidate inbound scan → per-source tx sets
        │     static price table → meaningful (≥$100) filter
        │     BulkMatchLabels + BulkMatchSanctionedAddresses → direct-taint
        │     receiver-check scan over source wallet ids → chain-taint (depth 1)
        │     outbound scan → possible_victim_contact
        │     ──► KindExposure(BasisOnchainComputed) findings + evidence
        │     ──► KindProceedsReceiver / KindPossibleVictimContact (info)
        │
        ▼
scoring.EvaluateFor(fs, policy) ── dispatch
        ├─ PolicyV1   → evaluateV1   (existing body, untouched)
        └─ PolicyV1_1 → evaluateV11  (new: source dedupe, per-source
                                     formula, cap 70, service-candidate
                                     suppression, informational kinds)
        ▼
persist screening_results.scoring_policy_version = "v1.1.0"
```

Layering preserved: `scoring` stays a pure engine (no IO); all DB access lives
in the screening-side analyzer via new sqlc queries; provider quirks stay in
their adapters.

### Key design decisions (post-debate revision — Critic round 1 incorporated)

- **Indexed data only.** The analyzer reads whatever the `transactions`
  index already holds for the candidate AND its inbound sources — it never
  calls `transaction.EnsureIndexed`/providers inside the 30 s screening
  deadline. **Chain-taint is bounded by source index coverage** (a source
  never indexed has no readable inbound rows → receiver-check
  unverifiable → `coverage:"partial"` + explicit unchecked count — A2).
  Rollout consequence (documented): under v1.1, a candidate with no usable
  index coverage marks the onchain leg absent and the exposure dimension
  degraded → `PARTIAL`, not silent "clean" (honesty invariant);
  `warmindex` enqueue recommendation adopted as mitigation (see A2 note
  in phase-03).
- **Direction gate (A1).** v1.1 scores INBOUND evidence only. Provider
  `direction` is normalized at the mapper (lowercase `inbound`/`outbound`
  → `INBOUND`/`OUTBOUND`); `OUTBOUND` provider items are emitted as
  `KindPossibleVictimContact` (weight 0) and can never join a source
  group. Unknown/empty direction → eligible for the flat-21/generic
  paths (provider asserted adjacency without claiming direction — the
  contract's prohibition targets positively-identified outbound flows).
  Deliberate divergence: v1 keeps scoring provider items at hop weights
  regardless of direction (frozen); v1.1 corrects it.
- **Direct-taint set** = proposal §3.1 list (SANCTIONED, HACK, RANSOMWARE,
  PHISHING, TERRORISM, SCAM, DRAINER, MONEY_LAUNDERING, DARKNET, EXTREMISM,
  FROZEN — same 11 codes as `baseWeightsV1` keys) + active sanctions
  listing. Taint requires a non-revoked label in the set whose
  `verification_status != 'disputed'` — a contested classification must
  not drive a 30–70-point block when the engine itself scores disputed
  at 0 (A7). `reported`/`unverified`/`verified` all taint; the finding's
  `VerificationStatus` carries the BEST taint-evidence status (sanctions
  listing → `verified`; label-only → that label's status — never
  inflated to `verified` by the onchain tx evidence, A6).
- **Service-classified candidate gate (A4)** is evaluated by the
  ANALYZER over the candidate's own bulk-matched labels and carried into
  the engine as `FindingSet.CandidateServiceClassified` — a dedicated
  input channel, independent of `include_labels` and never fabricated
  as a `KindLabel` finding (that would contradict
  `data_completeness.labels: NOT_REQUESTED`).
- **Static price table** keyed by `(network, asset)`; native = chain coin
  (ETH/BNB), tokens = normalized contract address. Pinned micro-USD prices +
  decimals; meaningful iff `amount_raw × price_micro ≥ floor_micro × 10^decimals`
  in `math/big` integer math — no float money math, no oracle.
  `SCORING_ASSET_PRICES` JSON env extends the allowlist at boot (fail-closed
  on malformed; network keys validated against the resolver's active
  codes — EVM-scoped by deliberate constraint since `transactions` is
  EVM-only today; documented that extension changes v1.1 outcomes →
  treat as deployment-pinned config).
- **Policy registry** in `scoring`: `KnownPolicies = {v1.0.0, v1.1.0}` +
  `KnownPolicy(v)` — config validates at boot, handler validates the
  per-request override (400 on unknown, before `requestHash`/claim
  insert), `EvaluateFor` returns a score-nil UNKNOWN verdict on an
  unregistered policy (defense in depth, never fabricates a score).
- **Idempotency**: `Options.PolicyVersion` (`json:",omitempty"`) rides
  inside `CanonicalRequest` → request hash automatically separates A/B
  screenings AND stays byte-identical for clients that never send it.
- **Dedupe winner rule (A9)**: source groups keyed by
  `(normalized SourceAddress, direction=INBOUND)`; winner =
  `onchain_computed` > `provider_reported`. An onchain-informational
  winner (n=0, dust-only) still wins — observed onchain transfers
  refute a provider materiality claim; when that winner's
  `coverage="partial"` the suppression reason's `Detail` notes the
  partial-coverage caveat instead of hiding it.
- **`ExposureItem.Scored` under v1.1 (A13)** = emit-time eligibility
  (`tainted ∧ n≥1 ∧ INBOUND` for onchain; existing hops rule for
  provider) — the mapper cannot know engine-side dedupe outcomes;
  dedupe losers stay listed with weight-0 informational reasons.
- **§15 gate status (A15)**: the proposal file still declares
  "PROPOSED — pending §15 review"; the approval this plan relies on is
  the operator's issuance of implementation prompt
  `20261110-1724-codex-exposure-v11-diversity-weighted.md` (operator
  notes: "Supersedes: model changed after operator review"). This is
  recorded here per AGENTS.md §15, and any deviation discovered during
  implementation still hits the prompt's stop conditions.

### Bounded-evaluation constants (v1.1, all named in `policy_v11.go`)

`exposureCapV1_1=70`, `exposureSourceBaseV1_1=30`,
`exposureSourceFreqV1_1=7`, `exposureReportedSourceV1_1=21`,
`meaningfulFloorUSDV1_1=100`, `exposureScanLimitV1_1=5000` (rows/scan,
matches `TracingMaxEdges` precedent), `exposureMaxSourcesV1_1=256`,
`exposureMaxDepth2SendersV1_1=512` (A10), `exposureEvidenceTxsV1_1=10`.

## Phase Roadmap

| # | Phase | Status | Depends on |
|---|-------|--------|------------|
| 1 | Policy registry, model extensions, config + request plumbing | pending | — |
| 2 | sqlc queries + static price/meaningful evaluator | pending | 1 |
| 3 | Onchain exposure analyzer + screening/mapper wiring | pending | 1, 2 |
| 4 | Engine v1.1 dispatch + golden v1 parity | pending | 1 (parallel w/ 2-3) |
| 5 | OpenAPI contract + full verification gates | pending | 1-4 |

## Risk Matrix

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| NUMERIC→float money math silently distorts floor | med | high | big.Int product compare only; unit tests at exact boundary |
| Source detection unbounded on high-degree wallets | med | high | scan LIMIT + max sources evaluated; overage → coverage=partial |
| Receiver-check fan-out (N queries) | med | med | ONE query over `to_wallet_id = ANY(source_ids)`; single bulk taint check |
| `coverage:"complete"` claimed on missing index | low | high | no index-state row or stale/partial/truncated → partial, always |
| v1 regression via shared-code edits | low | high | v1 constants/paths untouched; golden verdict corpus byte-compare |
| reported≠verified via onchain basis inflating | med | high | finding status = best taint evidence (sanctions→verified, label→own status); tx evidence verifies transfers only, never upgrades taint status |
| Reproducibility break via env price extension | low | med | documented deployment-pinned; boot-validated; default = pure pinned table |
| `policy_version` bypasses registry | low | med | handler 400 + EvaluateFor score-nil fallback |

## Debate Summary (Stage 1)

_To be filled after Devin Critic consensus._

## Success Criteria (end-state)

- `cd apps/api && sqlc generate && gofmt -l . && go build ./... && go vet ./... && go test ./...` → all clean
- All acceptance criteria in prompt §"Failure/scenario requirements" covered by tests
- `SCORING_POLICY=v1.0.0` → verdict JSON byte-identical to golden corpus
- `SCORING_POLICY` unknown → `config.Load` error; `options.policy_version` unknown → HTTP 400
- `screening_results.scoring_policy_version` = `v1.1.0` on v1.1 runs
