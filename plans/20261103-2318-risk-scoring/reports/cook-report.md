# Cook Report — Phase 5: Risk Scoring & Screening API

Plan: `plans/20261103-2318-risk-scoring/plan.md`
Date: 2026-11-03
Status: all four phases implemented; full suite green; **nothing committed**
(uncommitted working tree per instructions).

## Phase 1 — Screening persistence

- `migrations/000007_screenings.up.sql` / `.down.sql`: `screening_results`
  (terminal rows; `result json` for byte-faithful replay; no `PENDING`
  persisted — CHECK constraint on `screening_status`) and
  `screening_requests` (claim rows; UNIQUE `(client_id, idempotency_key)`,
  `request_hash`, nullable `screening_result_id`, `claimed_at`).
- `queries/screenings.sql` → sqlc-generated `internal/db/screenings.sql.go`
  (never hand-edited): claim insert, claim load, result insert,
  guarded attach (`screening_result_id IS NULL`), stale-claim guarded
  delete, plain open-claim delete, result fetch by UUID, results by
  wallet, risk-category resolution.
- Migration verified up/down on `check_wallet` and `check_wallet_test`.
- Integration tests: claim/result attach round-trip, unique-conflict
  `23505`, stale-claim takeover guard.

## Phase 2 — `internal/scoring` (pure engine, policy v1.0.0)

- `model.go`, `policy.go`, `engine.go` — no IO, no provider/HTTP/DB deps.
- Deterministic integer-tenth verification/confidence factors with
  half-up rounding (`(base*vf*cf + 50) / 100`) — avoids float64 boundary
  artifacts (e.g. `85×0.7 → 60`, not 59).
- Single-block membership (direct max / exposure cap 20 / other cap 30),
  dedupe by fingerprint and `{kind}:{category}:{source}:{ref}`,
  `DIRECT_SANCTIONS` flag independent of score, unknown categories →
  `OTHER` weight 0 with informational reason, classifications
  (GAMBLING/MIXER/EXCHANGE/entity types) score 0, exposures only with
  `provider_reported` basis and hops ≤ `MaxHops` ∧ ≤ 2, provider-score
  max band (+20/+10, never summed), deterministic reason ordering.
- 17 engine + 4 policy test functions: bounds, determinism
  (byte-identical output), no double counting, sanctions flag,
  verification/confidence ordering, disputed zero, classification zero,
  hops > 2 informational, caps, policy version.

## Phase 3 — `internal/screening` orchestration

- `model.go` (14-field §9.7 result), `mapper.go` (inputs → FindingSet),
  `service.go` (gather → fold availability → score → persist).
- Availability fold on `data_status`: AVAILABLE usable, STALE
  usable+degraded, UNAVAILABLE unusable, NOT_REQUESTED excluded;
  `DISABLED` providers excluded from failure counts but reported verbatim.
- Labels cursor loop page 200, cap 1000 + `truncated` flag.
- Exposures provider-reported only, `basis: provider_reported`, filtered
  by `max_hops`, hops > 2 informational, `coverage.tracing:
  NOT_IMPLEMENTED`.
- Persist seam: wallet `GetOrCreate` FK → result+claim-attach in ONE tx;
  `context.WithoutCancel` + internal deadline so client disconnects still
  land the terminal row; nil enrichment does not block local answers.
- Metrics: `screening_requests_total{status}`,
  `screening_duration_seconds`, `screening_partial_total`; logs carry
  `request_id` + `screening_id`.
- 11 service test functions over interface fakes on a dedicated test DB.

## Phase 4 — Screenings API

- `internal/screening/handler.go`: POST (201 + `Location`) and GET
  (stored body verbatim, 404 unknown UUID). Claim-first state machine:
  absent/whitespace key → no idempotency; malformed key → `400`;
  unique-conflict → hash compare FIRST → `409 IDEMPOTENCY_CONFLICT` on
  mismatch; same-hash terminal → `200` + `Idempotent-Replay: true`;
  same-hash fresh claim → poll 250 ms ≤ 30 s → replay or
  `409 IDEMPOTENCY_IN_FLIGHT`; stale claim (≥5 min) → guarded delete +
  reclaim; pipeline failure releases the open claim.
- `middleware/errors.go`: `IDEMPOTENCY_CONFLICT`, `IDEMPOTENCY_IN_FLIGHT`.
- `httpserver.Deps.Screening` + routes registered; `cmd/api` constructs
  the service.
- `api/openapi.yaml` 0.2.0 → 0.3.0: `screenings` tag, both paths,
  `Idempotency-Key` header, 201/200/400/401/404/409 responses,
  `ScreeningRequest`/`ScreeningResult` + 13 sub-schemas, synchronous
  contract + >30 s client-timeout guidance, "screenings absent" text
  removed, spec §9.6 `202`→`201` deviation documented. YAML verified
  parseable.
- 13 API test functions covering all 10 required scenarios plus
  `max_hops` rejection, network/address validation, and the full §9.7
  response shape.

## Verification

- `go build ./...` — clean
- `go vet ./...` — clean
- `gofmt -l .` — empty
- `go test ./...` — all packages pass (`tests` 77.8 s incl. API suite)
- `python3 yaml.safe_load(api/openapi.yaml)` — parses; 14 paths, 38 schemas

New test functions: 48 (scoring 21, screening 11, persistence 3, API 13).

## Files

Created: `migrations/000007_screenings.{up,down}.sql`,
`queries/screenings.sql`, `internal/db/screenings.sql.go` (generated),
`internal/scoring/{model,policy,engine}.go` + `*_test.go`,
`internal/screening/{model,mapper,service,handler}.go` + `service_test.go`,
`tests/{screenings_persistence_test,screenings_api_test}.go`.

Modified: `internal/db/models.go` (generated), `internal/middleware/errors.go`,
`internal/platform/metrics/metrics.go`, `internal/httpserver/server.go`,
`cmd/api/main.go`, `api/openapi.yaml`, phase frontmatter/checklists.

## Deviations

- Spec §9.6 `202`/`PENDING` example → `201` + synchronous result;
  documented in OpenAPI and phase files (plan §3.2 sanctioned deviation).
- `DeleteScreeningRequest` query added to `queries/screenings.sql` beyond
  the listed set — needed for the open-claim release path on pipeline
  failure; the stale takeover keeps its own guarded delete.
- `FindingSet.MaxHops` added to the scoring package so the engine itself
  enforces the hop bound deterministically.

## Invariants verified by tests

unknown ≠ safe (`INSUFFICIENT_DATA` + NULL score with no snapshots);
sanctions exclusion is explicit (`evaluated:false`, `NOT_REQUESTED`);
provider failure → `PARTIAL` + typed statuses, `DISABLED` ≠ failure;
disputed labels score 0; revoked labels excluded; exposures are
`provider_reported` only and never prove ownership; replay is
byte-identical; `PENDING` never persisted.
