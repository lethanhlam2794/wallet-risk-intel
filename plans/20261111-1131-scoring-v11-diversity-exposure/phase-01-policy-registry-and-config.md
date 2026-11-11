---
phase: 1
title: "Policy registry, model extensions, config + request plumbing"
status: done
priority: P1
dependencies: []
---

# Phase 1: Policy registry, model extensions, config + request plumbing

## Overview

Register `v1.1.0` beside `v1.0.0` in the scoring package, extend `Finding`
with the evidence fields v1.1 carries, add `SCORING_POLICY` boot config
(fail-closed), and thread `options.policy_version` through the request path
so per-request A/B selection reaches the service with 400-on-unknown
validation.

## Requirements

- Functional:
  - `scoring.PolicyV1_1 = "v1.1.0"` registered beside untouched `PolicyV1`.
  - `scoring.KnownPolicy(v string) bool` + `scoring.PolicyVersions` ordered
    registry — the single source of truth for validation.
  - New basis `BasisOnchainComputed = "onchain_computed"`.
  - New informational kinds: `KindProceedsReceiver = "proceeds_receiver"`,
    `KindPossibleVictimContact = "possible_victim_contact"` (weight 0).
  - `Finding` optional fields (omitempty): `SourceAddress`, `Direction`
    ("INBOUND"|"OUTBOUND"), `MeaningfulCount int`, `TxHashes []string`,
    `Coverage string` ("complete"|"partial"), `Reason string`,
    `TaintKind string` ("direct"|"chain"|"").
  - `Reason` optional field `Detail string` (passthrough of
    Finding.Reason for explainability).
  - New reason codes: `ReasonExposureOnchainSource`,
    `ReasonExposureProviderSource`, `ReasonExposureInformational`,
    `ReasonProceedsReceiver`, `ReasonPossibleVictimContact`,
    `ReasonServiceClassifiedCandidate`.
  - Config: `ScoringPolicy string` ← `SCORING_POLICY` env, default
    `v1.0.0`; `Load` errors when not `KnownPolicy` (fail-closed).
  - `OptionsIn.PolicyVersion *string`,
    `Options.PolicyVersion string `json:"policy_version,omitempty"`` —
    REQUIRED `,omitempty` (A5): all existing Options fields marshal
    unconditionally; a bare field changes every canonical hash and turns
    cross-deploy same-key replays into 409s. Empty → service default.
    `CanonicalRequest.Options` carries it → idempotency hash covers it.
  - Handler validation order: `opts.PolicyVersion` check runs BEFORE
    `requestHash`/claim insert (handler.go:94-102 region) — unknown →
    400 with no claim side-effects.
  - `screening.Deps.Policy string`; `New` defaults empty → `PolicyV1`.
    `persistFailed` stamps the run's resolved policy, not a hardcoded v1.
- Non-functional:
  - v1 code paths/constants byte-untouched (no edits to `baseWeightsV1`,
    `exposureCapV1`, `Evaluate` body).
  - Additive-only JSON fields — persisted findings JSONB stays readable.

## Architecture

- Registry lives in `internal/scoring/policy_v11.go` (new file) with
  `PolicyV1_1`, `KnownPolicies`/`KnownPolicy`, and ALL v1.1 constants
  (`exposureCapV1_1=70`, `exposureSourceBaseV1_1=30`,
  `exposureSourceFreqV1_1=7`, `exposureReportedSourceV1_1=21`,
  `meaningfulFloorUSDV1_1=100`, `serviceClassificationsV1_1`,
  `directTaintCategoriesV1_1`) — one file = one auditable policy unit.
- `model.go` additions only (new kinds/basis/fields/codes).
- `config.go`: `ScoringPolicy: envOr("SCORING_POLICY", scoring.PolicyV1)`
  then `if !scoring.KnownPolicy(cfg.ScoringPolicy) { return nil, fmt.Errorf(...) }`.
  Config importing `internal/scoring` is in-module and has no cycle.
- `handler.go`: after `Resolve()`, when `opts.PolicyVersion != ""` &&
  `!scoring.KnownPolicy(opts.PolicyVersion)` →
  `RespondError(400, CodeBadRequest, "options.policy_version is not a registered scoring policy")`.
- `service.go`: `Deps.Policy`; in `run()` →
  `policy := req.Options.PolicyVersion; if policy == "" { policy = s.d.Policy }`;
  pass into evaluation (phase 4) and `persistFailed`.

## Related Code Files

- Create: `apps/api/internal/scoring/policy_v11.go`,
  `apps/api/internal/scoring/policy_v11_test.go`
- Modify: `apps/api/internal/scoring/model.go`,
  `apps/api/internal/scoring/policy.go` (registry declaration site only —
  keep v1 constants untouched),
  `apps/api/internal/config/config.go`,
  `apps/api/internal/config/config_test.go`,
  `apps/api/internal/screening/model.go`,
  `apps/api/internal/screening/handler.go`,
  `apps/api/internal/screening/handler_test.go`,
  `apps/api/internal/screening/service.go` (Deps + persistFailed policy arg),
  `apps/api/cmd/api/main.go` (`Policy: cfg.ScoringPolicy`)

## Implementation Steps (TDD)

1. Red: `policy_v11_test.go` — `PolicyV1_1=="v1.1.0"`, `KnownPolicy`
   true for both versions/false for `"v1.2.0"`/`""`, constants exact
   (70/30/7/21/100), taint set equals the proposal §3.1 list,
   `TestEvaluateFor_UnknownPolicy_NullScore` placeholder contract.
2. Red: `config_test.go` — default `v1.0.0`, `SCORING_POLICY=v1.1.0`
   accepted, `=bogus` → `Load` error, `=v2.0.0` → error.
3. Red: `handler_test.go` — `options.policy_version:"v9.9.9"` → 400;
   `"v1.1.0"` passes validation (fails later at service or passes with
   fake); hash differs between v1.0.0 and v1.1.0 canonical requests.
4. Green: implement registry + model fields + config + handler +
   Deps.Policy + persistFailed policy propagation.
5. Refactor/check: `gofmt`, `go build ./...`, `go vet ./...`.

## Success Criteria

- [ ] `go test ./internal/scoring ./internal/config ./internal/screening` green
- [ ] `SCORING_POLICY=bogus` → `config.Load` returns error naming the var
- [ ] Unknown per-request policy → HTTP 400 before wallet resolution work
- [ ] `scoring.PolicyV1`, `baseWeightsV1`, `exposureCapV1`, `Evaluate`
      diff-empty vs HEAD
- [ ] `git diff` on `engine.go` in this phase: zero (dispatch lands in ph.4)

## Risk Assessment

- Registry drift (config accepts a version engine can't score) → single
  `KnownPolicies` map consumed by config, handler, and `EvaluateFor`.
- persistFailed stamping wrong version → policy passed explicitly, unit
  test asserts persisted `scoring_policy_version` matches run policy.
- Options field breaking idempotency for existing clients → pointer-typed
  `OptionsIn`; absent → `""` → canonical hash unchanged for current clients.
