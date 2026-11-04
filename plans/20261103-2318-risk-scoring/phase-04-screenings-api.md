---
phase: 4
title: "Screenings API surface"
status: completed
priority: P1
dependencies: [3]
---

# Phase 4: Screenings API surface

## Overview

`POST /api/v1/screenings` + `GET /api/v1/screenings/{id}` — claim-first
idempotency, validation, metrics wiring, OpenAPI contract, cmd/api
wiring.

## Requirements

- Functional:
  - `POST /api/v1/screenings`: body per §9.6; HTTP **201** + full §9.7
    result + `Location: /api/v1/screenings/{id}` (documented deviation
    from spec's `202`+`PENDING` example — plan §3.2).
  - **Idempotency-Key protocol** (plan §3.3 — full state machine):
    - absent/empty/whitespace key → no idempotency, always new row.
    - malformed (len>255 or charset outside `^[A-Za-z0-9._~-]+$`) →
      `400 BAD_REQUEST`.
    - new key → claim → pipeline → single-tx result+claim-attach.
    - conflict → load claim → **`request_hash` compared FIRST**:
      different hash → `409 IDEMPOTENCY_CONFLICT` in every claim
      state (terminal/in-flight/stale); same hash → branch below.
    - `result_id` set + same hash → `200` + stored body +
      `Idempotent-Replay: true`.
    - `result_id` NULL + `claimed_at` fresh → bounded poll
      (250 ms × ≤30 s) → terminal replay or `409 IDEMPOTENCY_IN_FLIGHT`.
    - `result_id` NULL + `claimed_at` stale (≥5 min) →
      takeover: DELETE + re-claim + run.
  - `GET /api/v1/screenings/{id}`: UUID → stored body byte-identical;
    unknown → `404 NOT_FOUND`.
  - Validation: `wallet.ResolveAndNormalize`; `max_hops` 0–2
    (reject-not-clamp); options defaults per plan §3.6.
  - Roles: reader+admin (inherited /api/v1 chain).
  - New error codes in `middleware/errors.go`: `IDEMPOTENCY_CONFLICT`,
    `IDEMPOTENCY_IN_FLIGHT`.
  - Metrics/log wiring per §3.9.
- Non-functional: thin handler; stable error envelope; request_id +
  screening_id on all logs; synchronous latency documented.

## Architecture

`internal/screening/handler.go` (Create + GetByID + idempotency
plumbing calling service persist seam). `httpserver.Deps` gains
`Screening *screening.Service`; `cmd/api` constructs it.
`api/openapi.yaml`: both endpoints, request/response schemas incl.
`Idempotency-Key` header param, 201/200/400/404/409 responses,
synchronous contract + >30 s client-timeout guidance, `screenings`
tag, version bump, remove "screenings intentionally absent".

## Related Code Files

- Create: `internal/screening/handler.go`, `internal/screening/handler_test.go`
- Modify: `internal/httpserver/server.go`, `cmd/api/main.go`,
  `api/openapi.yaml`, `internal/middleware/errors.go`

## Implementation Steps

1. Handler: bind+validate → resolve → claim/attach idempotency →
   service → 201 + Location.
2. GET handler: UUID parse → fetch → byte-faithful stored body or 404.
3. Error codes + metrics + wiring.
4. openapi.yaml update.
5. Integration tests (full list below).

## Success Criteria

- [x] `go build ./...` + `go test ./...` + `go vet ./...` green;
  `gofmt -l .` empty
- [x] **Required test list (Critic amendment 11)**:
  1. Concurrent identical POSTs → exactly one screening; loser replays
     or gets bounded 409 — defined behavior verified
  2. Same key + different payload → `409 IDEMPOTENCY_CONFLICT`
  3. **No published snapshots → `INSUFFICIENT_DATA`, `risk_score`
     NULL** — acceptance-critical unknown≠safe
  4. Sanctioned wallet + `include_sanctions=false` → no
     `DIRECT_SANCTIONS` flag + `evaluated:false`
  5. All providers failed + sanctions OK → `PARTIAL` + flag + typed
     statuses
  6. Disputed label → 0; revoked label → excluded
  7. Client disconnect mid-screening → row still terminal
  8. Replay returns stored result byte-identically +
     `Idempotent-Replay: true`
  9. Malformed/oversized key → 400; absent key → works
  10. GET unknown UUID → 404; anon → 401
- [x] openapi.yaml parses; contract matches implementation

## Risk Assessment

- Sync POST holds request ~25 s worst case — documented + openapi
  guidance.
- Claim takeover on 5-min staleness — owner presumed dead; takeover
  deletes+reclaims atomically.
- `GET` results readable by any reader — documented non-private scope.
