# Devin Review — Round 1: Risk Scoring & Screening API (Spec Phase 5)

**Date**: 2026-11-04
**Scope**: All pending changes — `migrations/000007_screenings.{up,down}.sql`,
`queries/screenings.sql`, `internal/db/screenings.sql.go`, `internal/scoring/`,
`internal/screening/`, `tests/screenings_{api,persistence}_test.go`, plus diffs
to `api/openapi.yaml`, `cmd/api/main.go`, `internal/httpserver/server.go`,
`internal/middleware/errors.go`, `internal/platform/metrics/metrics.go`,
`internal/db/models.go`.
**Risk level**: R3 (schema change + risk-decision surface + idempotent write API).

## Verdict: CHANGES_REQUESTED

## Executive Summary

The implementation is high quality: the idempotency claim state machine is
implemented exactly per plan §3.3 (hash-check first, single-tx attach, bounded
poll, guarded stale takeover), the availability fold keys on `data_status`
rather than error-only (unknown ≠ safe holds end-to-end), verif×conf scaling
with disputed=0 preserves reported ≠ verified, DISABLED is correctly excluded
from the fold while remaining visible, exposures are `provider_reported`-only
and never scored beyond hop/eligibility bounds, `result` is `json` type and
replay is verified byte-identical end-to-end, and `context.WithoutCancel` is
bounded by its own 30 s deadline with no dangerous value leakage (repo uses no
`context.WithValue`). Tests are honest — every required phase-04 case exists
and asserts real semantics (row-level NULL, byte-identity, concurrent claim
races, disconnect persistence). Build/vet/gofmt/test/-race all pass on fresh
(non-cached) runs.

Two findings require action before approval: (1) the engine's dedup
representative ordering inverts its documented strongest-evidence policy —
latent today but a real logic defect in an invariant-adjacent function; (2) an
undisclosed plan deviation — §3.5 commits FAILED rows "persisted when a wallet
row exists" but no code path can ever write one, and the cook report does not
list it among deviations. Both are cheap to resolve (a comparator fix or
comment correction; a best-effort FAILED insert or an explicit plan amendment).

## Verification Evidence

All commands run by this reviewer in the working tree:

| Command | Result |
|---|---|
| `go build ./...` | Exit 0 |
| `go vet ./...` | Exit 0 |
| `gofmt -l .` | Empty output |
| `go test ./...` | Exit 0 (all packages ok) |
| `go test -count=1 ./internal/scoring/ ./internal/screening/` | Exit 0 (0.011s / 5.402s — fresh) |
| `go test -count=1 -run 'TestScreenings' -v ./tests/` | Exit 0 (48.1s — all 13 API + 3 persistence tests PASS) |
| `go test -race -count=1 ./internal/scoring/ ./internal/screening/` | Exit 0 |
| `go test -race -count=1 -run 'TestScreenings_Concurrent|TestScreenings_Replay' ./tests/` | Exit 0 |
| `python3 yaml.safe_load(api/openapi.yaml)` | Parses; 14 paths, 38 schemas, version 0.3.0 |
| `psql ... check_wallet_test` | `schema_migrations`=7; `result` column is `json`; CHECKs and UNIQUE present as designed |

## Required-list audit (phase-04 amendment 11)

| # | Required test | Present & honest |
|---|---|---|
| 1 | Concurrent identical POSTs | `TestScreenings_ConcurrentIdentical` — 8 goroutines, asserts exactly 1 result row + 1×201, losers replay-or-409 |
| 2 | Key + different payload → 409 | `TestScreenings_KeyPayloadMismatch` — address AND options variants |
| 3 | No snapshots → INSUFFICIENT_DATA + NULL | `TestScreenings_InsufficientData_NoSnapshots` — asserts `risk_score` JSON null AND persisted `risk_score IS NULL` at row level |
| 4 | Sanctioned + include_sanctions=false | `TestScreenings_SanctionsExcluded` — no flag + `evaluated:false` + NOT_REQUESTED, counter-case flags 100 |
| 5 | All providers failed → PARTIAL | `TestScreenings_AllProvidersFailed_Partial` — typed statuses incl. DISABLED excluded |
| 6 | Disputed→0; revoked excluded | `TestScreenings_DisputedRevokedLabels` — row-level assertions |
| 7 | Disconnect → terminal row | `TestScreenings_ClientDisconnect_StillLands` — real HTTP client cancel + row poll |
| 8 | Replay byte-identical | `TestScreenings_ReplayByteIdentical` — 201 vs 200 vs GET byte-equality + header |
| 9 | Malformed/oversized key 400; absent works | `TestScreenings_KeyValidation` — incl. whitespace-only |
| 10 | GET unknown → 404; anon → 401 | `TestScreenings_GetByID_NotFoundAndAuth` |

Plus `max_hops` rejection, INVALID_NETWORK/INVALID_ADDRESS codes, and the full
§9.7 field-shape test. Persistence tests cover claim→attach atomicity, 23505,
and the guarded stale-delete (fresh/stale/completed branches). Scoring tests
cover determinism (40 seeded shuffles → byte-identical verdict), caps, direct
MAX-never-sum, single-block membership, provider SANCTIONED via same table,
hops>2 unscored. No weakened/skipped/deleted tests found.

## Detailed Findings

### [Blocker]
None.

### [Major]
None — no reachable invariant violations found.

### [Minor]

**M1 — `dedupe` keeps the weakest-evidence representative, contradicting its
documented policy** (`internal/scoring/engine.go:185-221`). The comparator
orders `VerificationStatus` and `Confidence` lexicographically ascending and
the dedup loop keeps the *first* element per DedupKey. Alphabetically
`"disputed" < "reported" < "unverified" < "verified"` and `"low" < "medium"`,
so when same-key findings differ in status/confidence the engine keeps the
*weakest* — the doc comment claims "keeping the strongest-evidence
representative (verified > reported > unverified > disputed, then
confidence)". Unreachable today (every DedupKey embeds a unique per-row ref:
`wallet_label:{id}`, `sanctions_entity:{id}`, per-source observation
identity), but if upstream ever emits duplicate identities with differing
statuses — e.g., a provider repeating an observation — verified evidence is
silently discounted to 0, a false-negative direction for a risk tool. Either
the comparator or the contract comment is wrong; reconcile by ordering on an
explicit strength rank.

**M2 — FAILED `screening_status` is never persisted — undisclosed deviation
from plan §3.5.** Plan: "FAILED: internal error (DB/pipeline) — persisted when
a wallet row exists". Implementation: `mapGathered` only yields
COMPLETED/PARTIAL/INSUFFICIENT_DATA; post-resolution errors → 500 +
claim-release. No code path inserts FAILED, so the CHECK's FAILED vocabulary
and `screening_requests_total{status="FAILED"}` cover it only at
metrics level. Practically near-vacuous (the only post-resolution failure
point is `persist` itself, which cannot record its own failure), but it is a
plan commitment not delivered — and it does not appear in the cook report's
Deviations list. Resolve by either (a) best-effort second-tx FAILED insert on
pipeline error, or (b) explicit plan/cook-report amendment documenting that
FAILED is metrics-only and claim-release covers retry.

**M3 — `Verdict.Blocks` populated when `Score` is NULL; findings emitted
independent of dimension usability** (`internal/scoring/engine.go:128-146`,
`internal/screening/mapper.go:108-149,188-208`). `Evaluate` fills block
subtotals unconditionally; the mapper emits sanctions/label findings whenever
the lookup returned rows, regardless of the fold's usable determination. With
current collaborators unreachable (UNAVAILABLE ⇒ no rows), but under contract
drift a response could read `blocks.direct.subtotal:100` beside
`risk_score:null` + `INSUFFICIENT_DATA` — a self-contradicting verdict shape.
Recommend gating finding emission on the dimension's usable flag (or zeroing
blocks when `HasUsableData=false`).

### [Nit]

- **N1** — `time.Since(existing.ClaimedAt.Time)` compares app clock against a
  DB-written timestamp (`handler.go:183`); skew picks the wrong branch
  (poll-vs-takeover). Consequence bounded — the guarded delete re-verifies
  staleness in DB time — so at worst a wasted ≤30 s poll or a no-op delete.
  Prefer comparing in SQL (e.g., select `claimed_at < NOW() - interval` as a
  computed flag).
- **N2** — `provider_status.providers["_enrichment"]` synthetic key on hard
  fan-out failure (`mapper.go:223-227`): not documented in openapi and could
  collide with a future provider of that name. Consider a top-level field or a
  clearly-namespaced key.
- **N3** — `ProviderEvidence.Identity` pipe-join (`mapper.go:262`):
  `cat|label|entity` collides when component fields contain `|` → wrongful
  dedup of distinct observations. Edge case; a length-prefixed or escaped
  encoding removes it.
- **N4** — `Blocks.Other.Capped` can never be true (band max 20 < cap 30) and
  `Blocks.Direct.Capped` is never set (`engine.go:131-134`) — dead flag
  values; forward-looking at best.
- **N5** — `ListScreeningResultsByWallet` generated but has no consumer
  (planned history query; no endpoint serves it). Harmless sqlc artifact;
  noted for completeness.
- **N6** — `pollOrConflict` returns without writing when
  `c.Request.Context().Done()` fires (`handler.go:213-215`) — a still-connected
  client whose ctx is canceled (e.g., shutdown) gets a bare 200 rather than an
  envelope. Minor.

## Required Actions to Approve

1. **M1**: Fix `dedupe` representative selection to match the documented
   strongest-evidence policy (explicit verification/confidence rank), or
   correct the comment and add a regression test for a same-key,
   differing-status pair.
2. **M2**: Reconcile the FAILED-persistence gap — implement a best-effort
   FAILED row post-resolution, or amend plan §3.5 + cook-report Deviations to
   state FAILED is metrics-only by design.
3. **M3 (recommended, not blocking)**: Gate finding emission on usable, or
   zero `Blocks` when score is NULL.
4. Nits are advisory; N1 is the cheapest real robustness improvement.

## Positive observations (verified, not assumed)

- Idempotency: claim-first INSERT → 23505 → hash-check FIRST in every claim
  state → replay/bounded-poll(250 ms×30 s)/guarded-stale-takeover; result
  INSERT + claim attach in ONE tx; claim release on pipeline failure via
  `context.Background()`; only-owned-claim deletes (`id` + `IS NULL`
  predicates). Races traced: concurrent claim, poll-vs-release,
  takeover-vs-complete — all resolve safely within 3 bounded attempts.
- `result` column is `json` — verified in live DB (`\d screening_results`);
  POST 201 body, replay 200, and GET are byte-identical (test-verified).
- `context.WithoutCancel` + own 30 s deadline; no `context.WithValue` exists
  in the repo — nothing dangerous propagates.
- CHECK vocabulary matches spec exactly (4 statuses, 5 levels, score 0–100|NULL);
  PENDING is never persisted; down migration drops in FK order.
- Metrics `screening_requests_total{status}`, `screening_duration_seconds`,
  `screening_partial_total` per §17; logs carry `request_id` + `screening_id`;
  no credentials/keys logged anywhere in the diff.
- OpenAPI matches implementation (201/200/400/401/404/409, both paths,
  `Idempotency-Key`, reason-code enums vs `scoring` constants, provider-status
  vocabulary vs `enrichment.statusFor`); 202→201 deviation documented.
- 14 §9.7 fields all present; `evaluated:false`/`NOT_REQUESTED` never read as
  clean; cross-chain + unresolved listings are informational-only
  (`CROSS_CHAIN_LISTING` weight 0, never the direct flag).
- Test isolation: `tests/` uses `check_wallet_test`, `internal/screening` uses
  package-private `check_wallet_screening_test`; `TruncateAll` enumerates
  tables dynamically so the new tables are covered.
