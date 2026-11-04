# Devin Review — Round 4: Post-Remediation Re-Review (Phase 5, Risk Scoring)

**Date**: 2026-11-04
**Scope**: Same uncommitted change set as rounds 1–3 (`git status` re-verified:
6 modified tracked files + `internal/{db/screenings.sql.go,scoring/,screening/}`,
`migrations/000007_screenings.{up,down}.sql`, `queries/screenings.sql`,
`tests/screenings_{api,persistence}_test.go`). R3→R4 delta is confined to
`internal/screening/{handler.go,service.go,handler_test.go}` (new file) and the
`queries/screenings.sql` header comment.
**Risk level**: R3 (schema change + risk-decision surface + idempotent write API).
**Prior review**: `review-round-3.md` (CHANGES_REQUESTED, R3-M1 + 4 nits).
**Claimed fixes**: `remediation-round-3.md`.

## Verdict: APPROVED

## Executive Summary

The single Round-3 minor (R3-M1) is verifiably fixed — `pollOrConflict` now
takes the request hash and re-checks `claim.RequestHash != hash` on every
250 ms tick, answering `409 IDEMPOTENCY_CONFLICT` before any replay. I audited
every exit branch of the claim state machine for a residual wrong-payload
replay path and found none: `request_hash` is immutable per row (no UPDATE
touches it), `screening_result_id` attaches by claim row ID inside the
result-insert transaction, and the hash check is ordered before the
`ScreeningResultID.Valid` check on a single fetched row — no TOCTOU between
check and use. All four advisory nits are remediated in code. The full gate
is green on fresh `-count=1` runs executed by this reviewer against the live
docker-compose Postgres.

## Verification Evidence (run by this reviewer, fresh `-count=1`)

| Command | Result |
|---|---|
| `go build ./...` | Exit 0 |
| `go vet ./...` | Exit 0 |
| `gofmt -l .` | Empty output |
| `python3 -c "import yaml; yaml.safe_load(open('api/openapi.yaml'))"` | Parses OK |
| `go test -count=1 -v -run 'TestHandler_Poll' ./internal/screening/` | Both PASS — `TestHandler_PollRejectsReclaimedDifferentHash` 0.75s, `TestHandler_PollVanishingClaim_Conflict` 0.74s (first-tick exits, not deadline burn) |
| `go test -count=1 ./...` | All packages ok; `tests` 88.283s; `internal/screening` 10.152s; `internal/enrichment` 9.353s |
| `go test -race -count=1 ./internal/scoring/ ./internal/screening/` | ok 1.056s / 18.124s |
| `docker ps` + `pg_isready -h 127.0.0.1 -p 5433` | `check-wallet-postgres-1` up, accepting connections |

## R3 Claim-by-Claim Verification

| Claim | Verified | Evidence |
|---|---|---|
| R3-M1: `pollOrConflict` takes `hash`, re-checks `RequestHash` per tick → 409 | **YES** | `handler.go:212` signature `(c, clientID, key, hash)`; `handler.go:235-240` re-checks `claim.RequestHash != hash` → `CodeIdempotencyConflict`, ordered **before** the `ScreeningResultID.Valid` replay at :241 — a completed foreign claim gets CONFLICT, never a replay. Sole production caller `handler.go:184` passes `hash`. |
| Tests exist and pass | **YES** | `handler_test.go:22-47` (`...ReclaimedDifferentHash`) and `:51-76` (`...VanishingClaim_Conflict`), both on the real `check_wallet_screening_test` DB via `testutil.RequireTestPoolDSN`. Both discriminating: under the pre-fix code the first test would burn the full 30 s deadline and return IN_FLIGHT (its 0.75 s runtime confirms early exit on the new branch). |
| Nit: `Enrich` receives `raw` for `rawAddress` | **YES** | `service.go:185` — `s.d.Enrichment.Enrich(egctx, net, raw, req.Address, …)`. More than cosmetic: `enrichment.Service.Enrich` (`enrichment/service.go:93-117`) uses `rawAddress` for both `res.Address` and its internal `GetOrCreate`, so the enrichment result now carries the original representation too. |
| Nit: `persist` nil-guard | **YES** | `service.go:384-386` — `if s.d.Pool == nil || s.d.Queries == nil { return fmt.Errorf("no persistence configured") }`, identical to `persistFailed` at :279. |
| Nit: `findings` marshaled via `orEmpty` | **YES** | `service.go:398` — `json.Marshal(orEmpty(m.findings))` with a comment naming the array column contract. The nil case (all dims usable-but-empty → `m.findings == nil`) now stores `[]`, matching `persistFailed`'s `[]byte("[]")`. |
| Nit: `queries/screenings.sql` documents 5-min vs 30-s | **YES** | `queries/screenings.sql:1-8` header: "The 5-minute staleness bound deliberately exceeds the 30 s pipeline deadline (service.Deps.Deadline) with a wide margin — a live owner attaches within seconds; a 5-minute-old open claim can only be a dead owner." |

## New-change audit (prompt items)

1. **`pollOrConflict` signature — all callers updated?**
   Yes. Grep confirms exactly three callsites: the production call at
   `handler.go:184` (passes `hash` derived from `requestHash(canon)` at :102)
   and the two test callsites. No stale signature remains; `go build`/`vet`
   clean.

2. **Can the initial hash check still be bypassed (claim created after the
   first read, before the first tick)?**
   No. Enumerated every interleaving:

   - *Release→reclaim before tick 1*: the tick re-fetches by
     `(client_id, key)` — the reclaimed row is what gets read → hash
     mismatch → 409. This was the R3-M1 hole; now closed.
   - *Reclaim under the SAME hash*: same hash ⇒ same canonical request ⇒
     replaying its result serves the caller's own payload. Correct.
   - *Hash check vs `ScreeningResultID` ordering*: the mismatch branch runs
     first (`handler.go:235` before :241), so a *completed* foreign claim
     still yields CONFLICT — the truthful "key was rebound" answer.
   - *Immutability*: `request_hash` is written only at INSERT; the only
     UPDATE (`AttachScreeningResultToRequest`) sets `screening_result_id`
     by row ID inside the result transaction. A row read with hash H was
     born with hash H, and its attached result was computed under H's
     canonical request — a valid `ScreeningResultID` on a hash-matching row
     can never point at a different payload's result.
   - *Vanished claim*: `ErrNoRows → break` → `IN_FLIGHT` (`handler.go:229`).
   - *Stale path*: `DeleteStaleScreeningRequest` is ID-scoped and re-guards
     openness+staleness inside the DELETE (`queries/screenings.sql:55-58`);
     `n == 0 → continue` re-reads and re-applies the hash check at
     `handler.go:171` before any poll/replay. A fresh reclaimed claim is
     not stale, so the takeover delete cannot touch it — the poller sees it
     and 409s.
   - *TOCTOU within a tick*: `RequestHash` and `ScreeningResultID` come from
     the same fetched struct — the check and the replay decision are
     consistent by construction. Result rows are immutable, so the
     subsequent `replay` fetch by result ID cannot be redirected.
   - *Cross-client*: claims are keyed `(client_id, idempotency_key)`; a
     different client's key is a disjoint claim — by design.

3. **Do the tests drive the intended branches?**
   Yes. `...ReclaimedDifferentHash` seeds a foreign-hash claim and polls —
   the first tick read must take the new `RequestHash != hash` branch (409
   CONFLICT); under the old code it would poll 30 s to IN_FLIGHT.
   `...VanishingClaim_Conflict` deletes the claim before polling — first
   read hits `ErrNoRows → break` → `IN_FLIGHT`, regression-guarding the
   adjacent branch the fix sits next to. Caveat noted for honesty, not a
   finding: the tests exercise the *decision branch*, not the literal
   release→reclaim→complete interleaving — appropriate for a unit test,
   since per-tick re-read + re-check covers all interleavings by
   construction.

4. **`orEmpty(m.findings)` — does any consumer read `findings` as nullable
   JSONB expecting null?**
   No. `Findings` is `[]byte` on `db.ScreeningResult` (`models.go:182`);
   readers are `GetScreeningResultByID`/`ListScreeningResultsByWallet`, and
   the API serves `row.Result` — nothing unmarshals `row.Findings` in the
   codebase (grep-verified; `tests/screenings_persistence_test.go:59,178`
   writes literals only). `[]` is strictly safer for any future array
   consumer.

5. **Test isolation sweep**: no `t.Parallel()` in the package; the handler
   tests' fixed `api_clients` names (`poll-hash`, `poll-vanish`) and keys
   (`k`, `kv`) are safe — `RequireTestPoolDSN` → `TruncateAll` clears
   `api_clients`/`screening_requests` per test, and the package uses a
   private `check_wallet_screening_test` DB. `TestService_ClaimAttachInTx`'s
   key `"k"` lives under a different `client_id` — no unique-key collision.

## Findings

### [Blocker]
None.

### [Major]
None.

### [Minor]
None.

### [Nit]
None.

## Rejected candidates (checked, not findings)

- **Reclaimed same-hash claim completing mid-poll → replay**: same hash
  implies byte-identical canonical request; replay is semantically correct.
- **`Enrich` errgroup ordering vs `GetOrCreate`**: `run`'s wallet upsert
  (step 1) is sequential before the errgroup, and the upsert is
  first-seen-wins — the `raw` fix removes the fragility regardless of order.
- **`Deps.Deadline` exceeding 5 min**: `New` defaults `<=0 → 30s` and
  `cmd/api/main.go` does not set `Deadline`; the header comment now pins
  the invariant for future editors.
- **Handler tests insert without cleanup**: `TruncateAll` runs per test via
  `RequireTestPoolDSN`; repeated `-count` runs are safe.

## Required Actions

None — approve and proceed.

## Positive observations (verified, not assumed)

- The R3-M1 fix is minimal and correctly placed: hash check *inside* the
  loop before the replay check, reusing the same `IDEMPOTENCY_CONFLICT`
  envelope as the initial-read branch — consistent client-visible semantics
  for "key rebound to a different payload."
- Both new tests run in ~0.75 s each — they exit on the first tick, proving
  the branch is hit immediately rather than at deadline.
- `remediation-round-3.md` claims match the code exactly; no over-claiming.
- The R3-N1 fix turned out to be load-bearing beyond its advisory status:
  `Enrich`'s `rawAddress` feeds both the response `address` field and
  enrichment's own wallet upsert.
