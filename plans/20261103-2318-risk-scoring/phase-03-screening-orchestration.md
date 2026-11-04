---
phase: 3
title: "Screening orchestration service"
status: completed
priority: P1
dependencies: [2]
---

# Phase 3: Screening orchestration service

## Overview

`internal/screening` service: resolve wallet → gather inputs honoring
options → map to FindingSet → score → compose §9.7 result → persist
terminal row → return. Runs under `context.WithoutCancel` + bounded
deadline so client disconnects still land the audit row.

## Requirements

- Functional:
  - **All 14 §9.7 fields** in the result model: `screening_id`,
    `network`, `address`, `status`, `risk_score`, `risk_level`,
    `direct_sanctions`, `direct_labels`, `exposures`, `scoring`,
    `evidence`, `provider_status`, `data_completeness`, `screened_at`.
  - `evidence` block: flattened reference list joining reasons to
    proof — `{kind, ref}` where ref = `wallet_labels.id`,
    `sanctions_entities.id`/`sanctions_wallets.id`, provider observation
    `{source, fetched_at}`; cross-chain listings appear as their own
    informational entries.
  - **Availability fold** (never error-only): AVAILABLE→usable,
    STALE→usable-degraded, UNAVAILABLE→not-usable,
    NOT_REQUESTED→excluded. Statuses: COMPLETED / PARTIAL /
    INSUFFICIENT_DATA (score NULL, level UNKNOWN) / FAILED — fold per
    plan §3.5.
  - `include_*` semantics per plan §3.6 matrix — `evaluated:false`
    markers, never empty-as-clean.
  - **Labels**: collect ALL active labels via `ListLabels` cursor loop
    (page cap 200 → loop to exhaustion; hard cap 1000 + `truncated`
    marker in data_completeness if hit).
  - **Exposures**: provider-reported only; every item
    `basis: "provider_reported"`; filter `hops ≤ max_hops`;
    `hops > 2` informational-only; `coverage` block =
    `{tracing: "NOT_IMPLEMENTED", max_hops_requested, disabled_by_request?}`;
    NEVER under `transaction_evidence`.
  - `provider_status` JSONB: per-source status map (OK/typed
    failure/DISABLED) + sanctions/labels snapshot data_status.
    **DISABLED excluded from the status fold** (configuration, not
    failure — mirrors `enrichment.aggregate`) but reported verbatim.
  - `data_completeness`: per-dimension availability + staleness +
    label truncation.
  - Persistence: INSERT terminal `screening_results` row (request,
    result-JSON body, findings, provider_status, policy version,
    explicit `screened_at`) — result INSERT + claim UPDATE in ONE tx
    (claim plumbing lands via phase-04 handler seam; service exposes
    the persist step).
  - Metrics + logs: `screening_requests_total{status}`,
    `screening_duration_seconds`, `screening_partial_total`;
    `screening_id` + `request_id` log fields (spec §17).
- Non-functional: enrichment may be nil → local data still answers;
  context budget = enrichment deadline for the provider leg; local
  lookups unblocked by provider timeouts (gathered in parallel where
  cheap).

## Architecture

`internal/screening/`: `model.go` (§9.7 blocks), `service.go`
(orchestrator + persist), `mapper.go` (domain→FindingSet + result
composition). Interface seams for sanctions/label/enrichment
collaborators so unit tests can fake them.

## Related Code Files

- Create: `internal/screening/model.go`, `internal/screening/service.go`,
  `internal/screening/mapper.go`, `internal/screening/service_test.go`
- Modify: `internal/platform/metrics/metrics.go` (screening counters)

## Implementation Steps

1. §9.7 result model + availability fold helpers.
2. Gather: sanctions lookup, label cursor-loop, enrichment fan-out —
   each contributing its availability signal.
3. Mapper → FindingSet (dedup keys, evidence refs, category_type via
   risk_categories lookup).
4. Score → compose result → fold status → persist (tx seam).
5. Metrics + structured logging.
6. TDD unit tests (fake collaborators) + integration test on test pool.

## Success Criteria

- [x] `go test ./internal/screening/...` green
- [x] No published snapshots → `INSUFFICIENT_DATA` + `risk_score` NULL
  persisted — **the unknown≠safe acceptance case**
- [x] Sanctioned wallet + `include_sanctions=false` → no
  `DIRECT_SANCTIONS` flag + `evaluated:false` (must not read clean)
- [x] All providers failed + sanctions OK → `PARTIAL` + flag + typed
  provider_status
- [x] >200 labels fixture → all scored (cursor loop), no silent drop
- [x] Disputed label → 0; revoked label → excluded
- [x] Client disconnect → terminal row still lands (WithoutCancel)
- [x] Provider exposure with hops>2 → listed informational, unscored
- [x] `go build ./...` green

## Risk Assessment

- Enrichment nil/unavailable → PARTIAL at most; never a hard dep.
- Result JSON serialized once, stored as `json` type → byte-faithful.
- `screened_at` explicit at insert — column and body agree; replays
  preserve original.
