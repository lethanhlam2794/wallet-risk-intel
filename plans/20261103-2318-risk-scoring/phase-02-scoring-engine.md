---
phase: 2
title: "Scoring engine — versioned policy v1"
status: completed
priority: P1
dependencies: [1]
---

# Phase 2: Scoring engine — versioned policy v1

## Overview

Pure deterministic package `internal/scoring`:
`Evaluate(FindingSet) → Verdict`. Zero IO, zero provider/db/HTTP types —
input is the normalized finding set the screening service assembles.

## Requirements

- Functional:
  - `PolicyV1` constant, version string `v1.0.0`, closed tables:
    - base weights: RANSOMWARE 95, HACK 90, PHISHING 90, TERRORISM 90,
      SCAM 85, DRAINER 85, SANCTIONED 85, MONEY_LAUNDERING 80,
      DARKNET 75, EXTREMISM 70, FROZEN 60; unknown/dynamic categories
      → OTHER → 0 with informational reason (never a silent weight).
    - verification factor: verified 1.0 / reported 0.7 / unverified 0.5 /
      disputed 0.
    - confidence factor: high 1.0 / medium 0.9 / low 0.8 / unknown 0.7
      (spec §10.1 "Data Confidence" dimension — applies to labels AND
      provider observations; OFAC direct match is flat 100, official).
    - `weight = round(base × verif × conf)` — deterministic rounding.
  - Formula + precedence (fixed order):
    `score = min(100, direct_max + min(20, exposure) + min(30, other))`.
    direct_max = MAX of direct weights (never sum).
  - `DIRECT_SANCTIONS` flag emitted by an OFAC direct match independent
    of the numeric score (survives even if it were the only finding).
  - **Single-block-membership**: a finding is scored in exactly one
    block — provider observations score once via the direct-block
    table (or the 0-weight classification path); they never reappear
    in the other block.
  - Exposure findings: `basis` must be `provider_reported` (P5);
    `hops==1` 10 pts, `hops==2` 5 pts — both × reported factor (0.7);
    `hops>2` → informational reason only, never scored.
  - Other block: max provider_score band only (≥80→+20, ≥50→+10;
    max not sum) + future non-observation signals (none in P5).
  - Category→category_type resolution: FindingSet carries the resolved
    `category_type` (screening maps via `risk_categories` DB truth —
    dynamic upstream categories land at OTHER).
  - Dedup key: `{kind}:{category}:{source}:{ref}` — labels key by
    fingerprint; provider observations by source+identity; cross-source
    mirrors are distinct findings by design (block caps bound residual
    stacking — documented limitation).
  - Reason objects `{code, category, verification_status, weight,
    evidence_ref}` — closed `code` enum: DIRECT_SANCTIONS_MATCH,
    DIRECT_LABEL, LABEL_CLASSIFICATION, PROVIDER_OBSERVATION,
    PROVIDER_SCORE, EXPOSURE_PROVIDER, CROSS_CHAIN_LISTING,
    SNAPSHOT_STALE, PROVIDER_FAILED, NOT_EVALUATED.
  - `Verdict`: score(*int — NULL-able), risk_level, flags,
    per-block subtotals + cap markers, ordered reasons.
  - `risk_level`: CRITICAL ≥80 / HIGH 60–79 / MEDIUM 30–59 / LOW 0–29 /
    UNKNOWN (score NULL).
- Non-functional: reproducible — same FindingSet + policy ⇒ identical
  Verdict; deterministic reason ordering (sorted, no map iteration);
  no clock/RNG dependence.

## Architecture

`internal/scoring/`: `policy.go`, `model.go`, `engine.go` + tests.
§10.1 dimension→mechanism table lives in `policy.go` doc comment.

## Related Code Files

- Create: `internal/scoring/policy.go`, `internal/scoring/model.go`,
  `internal/scoring/engine.go`, `internal/scoring/engine_test.go`,
  `internal/scoring/policy_test.go`

## Implementation Steps

1. Model types (FindingSet/Finding/Verdict/Reason).
2. PolicyV1 tables + version + dimension-map doc.
3. Evaluate: dedupe → classify findings into blocks → direct max →
   capped additive blocks → score/level/flags/reasons.
4. TDD: table-driven + property tests.

## Success Criteria

- [x] `go test ./internal/scoring/...` green incl. property tests:
  bounds 0–100, determinism (same input → byte-identical verdict),
  no-double-count, sanctions-flag independence, reported<verified,
  confidence ordering (high>medium>low>unknown), disputed=0,
  gambling/mixer/exchange=0, hops>2 unscored, block caps hold,
  provider SANCTIONED observation scored via same category table
- [x] Policy version `v1.0.0` asserted in test

## Risk Assessment

- Weights are spec-illustrative — versioned policy makes changes a new
  version, never a silent edit.
- Unknown upstream categories → OTHER(0) + informational reason —
  explicit, never silently weighted.
