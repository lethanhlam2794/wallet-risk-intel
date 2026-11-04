---
phase: 1
title: "Screening persistence schema"
status: completed
priority: P1
dependencies: []
---

# Phase 1: Screening persistence schema

## Overview

Migration `000007_screenings` — `screening_results` (spec §7.14 verbatim
+ `request`/`result` columns) and `screening_requests` (claim-first
idempotency table) — plus the sqlc query set.

## Requirements

- Functional:
  - `screening_results`: `id UUID PK`, `wallet_id BIGINT FK wallets`,
    `risk_score SMALLINT NULL CHECK(0–100 OR NULL)`, `risk_level
    VARCHAR(30)`, `screening_status VARCHAR(30)`,
    `scoring_policy_version VARCHAR(50)`, `findings JSONB []`,
    `provider_status JSONB {}`, `screened_at TIMESTAMPTZ`, plus
    `request JSONB` (canonical request) and `result JSON`
    (**type `json` not `jsonb`** — jsonb re-normalizes key
    order/whitespace/numerics and would break byte-faithful replay;
    the GET contract requires byte-identical stored body).
  - CHECK `screening_status` ∈ COMPLETED/PARTIAL/INSUFFICIENT_DATA/
    FAILED — spec's four values only; PENDING is never persisted
    (claim row's NULL result_id expresses in-flight state).
  - CHECK `risk_level` ∈ LOW/MEDIUM/HIGH/CRITICAL/UNKNOWN.
  - `screening_requests`: `id BIGSERIAL`, `client_id BIGINT FK
    api_clients(id)`, `idempotency_key TEXT` with
    `CHECK (char_length(idempotency_key) BETWEEN 1 AND 255)`,
    `request_hash TEXT` (sha256 of canonical request),
    `screening_result_id UUID NULL REFERENCES screening_results(id)`,
    `claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`,
    `UNIQUE(client_id, idempotency_key)`.
    `screening_result_id IS NULL` = claim in-flight (or orphaned by a
    dead owner — stale ≥5min claims are takeover-eligible per §3.3).
  - Indexes: `screening_results(wallet_id, screened_at DESC)` — no
    separate `(client_id)` index: `UNIQUE(client_id, idempotency_key)`
    already serves leftmost-prefix client lookups.
- Non-functional: reversible down migration; sqlc regenerates cleanly.

## Architecture

Established convention: `migrations/` + `queries/*.sql` → `internal/db`
via `sqlc generate` (pgx/v5, emit_empty_slices; UUID → pgtype.UUID).

## Related Code Files

- Create: `migrations/000007_screenings.up.sql`,
  `migrations/000007_screenings.down.sql`, `queries/screenings.sql`
- Modify: `internal/db/` generated files (via `sqlc generate`)

## Implementation Steps

1. Up/down migration per above (down drops both tables, order-safe).
2. `queries/screenings.sql`: InsertScreeningResult (single-row terminal
   insert returning id), GetScreeningResultByID,
   ListScreeningResultsByWallet (history),
   InsertScreeningRequest (claim), GetScreeningRequestByKey
   (client_id+key), UpdateScreeningRequestResult (attach result_id),
   DeleteScreeningRequest (stale-claim takeover).
3. `sqlc generate`; `go build ./...` green.

## Success Criteria

- [x] `migrate up` + `migrate down` clean on dev + `_test` databases
- [x] `sqlc generate` produces `internal/db/screenings.sql.go`
- [x] `go build ./...` green
- [x] Integration test: claim→result-attach round-trip via test pool;
  claim conflict observable via 23505

## Risk Assessment

- Extra columns beyond spec §7.14 — additive only; debated and accepted.
- `result JSON` byte fidelity vs `jsonb` — `json` chosen deliberately
  (Critic amendment 2); `request` may stay `jsonb` since `request_hash`
  is computed pre-storage.
- Claim lifecycle keeps `screening_requests` unbounded — MVP note,
  retention deferred.
