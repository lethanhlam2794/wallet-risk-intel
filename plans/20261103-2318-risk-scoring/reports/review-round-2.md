# Devin Review — Round 2: Post-Remediation Re-Review (Phase 5, Risk Scoring)

**Date**: 2026-11-04
**Scope**: All pending changes (uncommitted): `migrations/000007_screenings.{up,down}.sql`,
`queries/screenings.sql`, `internal/db/screenings.sql.go`, `internal/scoring/`,
`internal/screening/`, `tests/screenings_{api,persistence}_test.go`, plus diffs to
`api/openapi.yaml`, `cmd/api/main.go`, `internal/httpserver/server.go`,
`internal/middleware/errors.go`, `internal/platform/metrics/metrics.go`,
`internal/db/models.go`.
**Risk level**: R3 (schema change + risk-decision surface + idempotent write API).
**Prior review**: `review-round-1.md` (CHANGES_REQUESTED). **Claimed fixes**:
`remediation-round-1.md` (M1–M3, N1–N6 all remediated in code).

## Verdict: CHANGES_REQUESTED

## Executive Summary

Every Round-1 finding is verifiably remediated: M1 (explicit strength ranks),
M2 (`persistFailed` audit row, unlinked from claim, claim released for retry),
M3 (two-layer `dim.Usable` gate + `Blocks{}` zeroing), N1 (DB-computed
`claim_stale`), N2 (`fanout` named field), N3 (JSON identity), N6 (409
envelope on ctx.Done). All claimed regression tests exist, are honest
(row-level assertions, both input orders, real test DB), and pass on fresh
runs. The full gate is green.

However, the remediation code itself contains five new defects the fixes
introduced or resurfaced — the most notable being that the M1 comparator fix
still ranks evidence backwards on its final tiebreak axis (provider score:
`si < sj` keeps the *weakest* score, the identical mechanism M1 removed one
clause above). All are cheap, low-blast-radius fixes in code that moved this
round.

## Verification Evidence (run by this reviewer, fresh `-count=1`)

| Command | Result |
|---|---|
| `go build ./...` | Exit 0 |
| `go vet ./...` | Exit 0 |
| `gofmt -l .` | Empty output |
| `python3 -c "import yaml; yaml.safe_load(open('api/openapi.yaml'))"` | Parses OK (v0.3.0) |
| `go test -count=1 -v -run 'TestEvaluate_DedupKeepsStrongestEvidence\|TestEvaluate_NoUsableData_NullScore' ./internal/scoring/` | Both PASS (0.011s) |
| `go test -count=1 -v -run 'TestService_PersistFailed_AuditRow\|TestService_UnusableDimension_EmitsNoFindings' ./internal/screening/` | Both PASS on real `check_wallet_screening_test` (0.978s) |
| `go test -count=1 ./...` | All packages ok; `tests` 85.820s |
| `go test -race -count=1 ./internal/scoring/ ./internal/screening/` | ok 1.063s / 8.543s |

## Claim-by-claim verification

| Claim | Verified | Evidence |
|---|---|---|
| M1: explicit strength ranks, not lexical | **YES** | `engine.go:206-269`: `verificationRank` (verified4>reported3>unverified2>disputed1>default0), `confidenceRank` (high4>medium3>low2>unknown1>default0), comparator descending (`vi > vj`, `ci > cj`). All 4 statuses + 4 confidences covered; OOV values rank 0 (lowest, no crash). Test asserts verified survives both input orders (score 85, weight 85) — passes. |
| M2: `persistFailed` audit row unlinked from claim | **YES** | `service.go:256-260, 270-340`: on `persist` error only, inserts FAILED row (`risk_score` NULL via `pgtype.Int2{}`, `risk_level` UNKNOWN, `scoring_policy_version` `v1.0.0`, `findings` `[]`) via fresh `context.WithTimeout(context.Background(), 10s)`; never touches `screening_requests`, so the guarded `DeleteScreeningRequest` (`AND screening_result_id IS NULL`) still releases the claim for retry. `runAndRespond` releases on `context.Background()` (see Finding 3). Test asserts row-level FAILED/NULL/UNKNOWN/version + stored `result.fanout.error` — passes. Audit-unlinked-from-claim is the right design: linking would poison idempotency (a retry would replay FAILED forever). |
| M3: mapper gates on `dim.Usable`; engine zeroes `Blocks` | **YES** | `mapper.go:113` (`g.sanctionsLookup != nil && dim.Usable` gates entries/findings/evidence/matched), `mapper.go:197-202` (labels items copied only under `dim.Usable`, else `[]`); `engine.go:143-155` (`else` branch sets `RiskUnknown` + `Blocks{}`). `TestService_UnusableDimension_EmitsNoFindings` feeds rows under UNAVAILABLE → INSUFFICIENT_DATA, NULL score, empty entries/items, `scoring.Blocks{}`, no `DIRECT_SANCTIONS` flag — passes. `TestEvaluate_NoUsableData_NullScore` extended with `assert.Equal(Blocks{}, v.Blocks)` — passes. No consumer relies on nonzero subtotals with NULL score (`persistFailed` emits zeroed blocks; OpenAPI `BlockScore` has no nonzero contract). |
| N1: `claim_stale` in DB; `staleClaimAfter` removed | **YES** | `queries/screenings.sql:30` `claimed_at < NOW() - INTERVAL '5 minutes' AS claim_stale`; generated `ClaimStale bool` field (`screenings.sql.go:64,81,96`); handler branches on `existing.ClaimStale` (`handler.go:182`); `staleClaimAfter` absent from all code (grep). Predicate is byte-identical to `DeleteStaleScreeningRequest`'s (`claimed_at < NOW() - INTERVAL '5 minutes'`): same literal interval, same strict `<`, both DB-time on a `TIMESTAMPTZ DEFAULT NOW()` column — boundary behavior identical, and the SELECT→DELETE re-check is monotone-safe. |
| N2: `fanout` field replaces `_enrichment` | **YES** | `model.go:227` `Fanout *ProviderStatusEntry` (`omitempty`); `mapper.go:238-242` emits it on wholesale fan-out failure with a **sanitized static** message; `service.go:294` emits it in `persistFailed`; OpenAPI `ScreeningProviderStatus.fanout` documented (`api/openapi.yaml:1387`). `_enrichment` gone from all code. |
| N3: identity JSON-encoded | **YES** | `mapper.go:279-280` `json.Marshal([]string{o.Category, o.Label, o.Entity})` — collision-free; pipe-join gone. |
| N6: 409 envelope on ctx.Done | **YES** | `handler.go:212-218`: `middleware.RespondError(c, 409, CodeIdempotencyInFlight, …)` on `<-ctx.Done()`. |

## Detailed Findings

### [Blocker]
None.

### [Major]
None — no reachable invariant violations.

### [Minor]

**R2-M1 — `dedupe` provider-score tiebreak keeps the *weakest* evidence — the
M1 mechanism persists on the final comparator axis** (`internal/scoring/engine.go:217-225`).
After the new rank clauses, the comparator ends with `return si < sj` —
ascending `ProviderScore` — so among same-`DedupKey` findings the **lowest**
score sorts first and survives dedup. For `KindProviderScore` findings this is
the *only* axis they can differ on (`VerificationStatus` is hardcoded
`"reported"` at `mapper.go:304`, `Ref` is `src+":score"`, `Category` empty) —
so a duplicate same-source score of 30 vs 90 dedups to 30, and `otherMax`/the
`other` block band silently drops (band 0 instead of 20). The function's own
doc now claims "keeping the strongest-evidence representative" and warns that
inverted ranking "would discount the strongest evidence to zero" — yet the
score axis does exactly that. Same failure mechanism M1 fixed for status and
confidence, latent today (requires a duplicated `Source` in
`enrich.Providers` — contract drift), false-negative direction for a risk
tool. The neighboring `Hops <` tiebreak is correctly oriented (fewer hops =
stronger evidence = higher weight), which makes the score clause's inversion
stand out. Fix: `si > sj` + a same-key regression assertion.

**R2-M2 — `persistFailed` persists raw `cause.Error()` into the
client-replayable `result` blob** (`internal/screening/service.go:294-297`).
`Message: cause.Error()` lands verbatim in `provider_status.fanout.error`
*and* inside the stored `result` JSON, which `GET /screenings/{id}` serves
byte-identically to any authenticated client that holds the UUID. No secrets
leak (pgx omits passwords), but driver error detail — constraint names, table
names, host/user/database in connect errors — becomes durable, contract-bound
data. Engineering standard §2 forbids serving DB driver details to clients;
`foldProviders` one file over already uses the safe pattern (static
`"enrichment fan-out failed"`, `mapper.go:241`). Latent today — the FAILED
row's UUID is minted internally and disclosed nowhere (500 response, not
logged, not claim-linked) — but `ListScreeningResultsByWallet` exists
precisely for the planned history endpoint that would serve these blobs.
Fix: sanitize `cause` to a bounded/static message (e.g.
`"persist failed"` or a classified code), matching the mapper's own
precedent.

**R2-M3 — Claim-release delete runs on an unbounded
`context.Background()`** (`internal/screening/handler.go:263-269`). On
pipeline failure the claim release uses `context.Background()` with no
timeout; `pgxpool` connection acquisition blocks until context done — under
pool exhaustion/DB distress (exactly when this error path runs) the handler
goroutine can hang indefinitely, accumulating goroutines per failed
screening. §8: every out-of-process call MUST have an explicit timeout —
`persistFailed` right beside it correctly uses `WithTimeout(10s)`. Intent
(survive dead request ctx) is correct; it just needs the same bound.
(Missed in Round 1; flagged now as the code moved.)

**R2-M4 — The `enrichErr → provider_status.fanout` emission path has zero
test coverage.** Grep-verified: every `fakeEnrich`/`screeningEnricher` use
passes `res:` only — no test returns an `Enrich` error, so the only
production site that emits the new `fanout` field on a real fan-out failure
(`mapper.go:233-245`: `g.enrichErr || g.enrich == nil` → `Fanout` +
`degraded++`) is never exercised. `TestService_PersistFailed_AuditRow`
asserts `Fanout` but through a different writer. G-006/P-047: a fixture must
drive the branch it claims to cover — one `fakeEnrich{err: …}` test
asserting `Fanout.Status=="ERROR"` + `PARTIAL` closes it.

**R2-M5 — Screening-created wallets lose the original address
representation** (`internal/screening/service.go:140`). `run` calls
`Wallets.GetOrCreate(ctx, net.ID, req.Address, req.Address)` where
`req.Address` is the *canonical* (normalized lowercase) form — the raw input
is dropped at the handler boundary (`canon.Address = normalized`). The
`wallets.address` column is documented "original representation preserved"
(`wallet/service.go:19`; AGENTS §6) and `ON CONFLICT` keeps first-seen — so a
wallet first observed via `POST /screenings` stores the normalized form in
`address`, while the wallet-lookup path preserves the submitted form;
`GET /wallets` then shows divergent representations for the same wallet
depending on creation path. Fix: carry the raw address into the pipeline
(e.g. `RunMeta`/`CanonicalRequest.RawAddress`) and pass it as the `address`
argument.

### [Nit]

- **R2-N1** — `m.findings` can be `nil` when all requested dimensions are
  usable but evidence-free → `json.Marshal(nil)` stores JSONB `null` in
  `screening_results.findings` while every other writer stores `[]`
  (`service.go:384`, `persistFailed` uses `[]byte("[]")`). Cosmetic storage
  inconsistency; `orEmpty(m.findings)`-style fix. (API surface unaffected —
  `res.Scoring.Reasons` is `orEmpty`'d.)
- **R2-N2** — `data_completeness.labels_collected` (raw gather count,
  `mapper.go:161`) vs `direct_labels.collected` (gated items,
  `mapper.go:220`) diverge under unusable-with-rows drift (5 vs 0);
  same-name fields with different semantics, undocumented in OpenAPI.
  Defensible as telemetry-vs-evidence but worth a description line.
- **R2-N3** — `pollOrConflict` mid-iteration cancel: ctx done *between* the
  select tick and `GetScreeningRequestByKey` → query error → `InternalError`
  500 instead of the truthful 409 (N6's fix covers only select-observed
  cancels). Same window exists in `claimAndRun`'s loop reads. Bounded
  consequence; check `errors.Is(err, context.Canceled)` → 409 if tightening.
- **R2-N4** — `claim_stale` boundary flag itself is only indirectly tested:
  `TestScreenings_StaleClaimTakeoverGuard` proves the DELETE predicate at
  SQL level but no test asserts `GetScreeningRequestByKey.ClaimStale` flips
  at 5 min (aging `claimed_at` then reading would pin the regression).
- **R2-N5** — Engine-level residual: `FindingSet{HasUsableData:false}` +
  a `sanctions_direct` finding yields `flags:[DIRECT_SANCTIONS]` beside
  `score:null`/`blocks:0` — unreachable via the mapper gate (which now
  blocks upstream), but the engine contract would be clearer zeroing flags
  too, or documenting flags as observed-evidence reports. Also:
  `reasons[].weight` still reports would-be contribution (e.g. 85) beside
  `blocks.direct.subtotal:0` — intentional per the new comment, noted.

## Required Actions to Approve

1. **R2-M1**: Flip the provider-score dedup tiebreak to `si > sj` (strongest
   score survives) and add a same-key differing-score regression assertion.
2. **R2-M2**: Sanitize the persisted cause in `persistFailed` — static or
   bounded message, never raw driver error text in the replayable blob.
3. **R2-M3**: Bound the claim-release delete (`context.WithTimeout(
   context.Background(), ~10s)`), consistent with `persistFailed`.
4. **R2-M4**: Add one test driving `Enrich` hard-failure → `Fanout`
   populated + `PARTIAL` (closes the only-emitter coverage gap on N2).
5. **R2-M5**: Preserve the raw submitted address into
   `wallets.address` on the screening path (carry raw through the seam).
6. Nits R2-N1…R2-N5 are advisory; R2-N4 is the cheapest real test gain.

## Positive observations (verified, not assumed)

- All nine remediation claims verified line-by-line; zero claim inflation.
- New tests are honest: `DedupKeepsStrongestEvidence` tests both input
  orders; `UnusableDimension` drives the exact contract-drift path and
  asserts row+response level; `PersistFailed_AuditRow` uses
  `ListScreeningResultsByWallet` on the real DB (N5's "exercised" claim is
  also true — `tests/screenings_persistence_test.go:88-92` uses it for
  history ordering).
- `claim_stale` predicate is byte-identical to the guarded delete —
  no skew between the read decision and the atomic re-check.
- `persistFailed`'s unlinked-from-claim design is correct: the guarded
  `DeleteScreeningRequest` (`AND screening_result_id IS NULL`) handles the
  ambiguous-commit case safely — if `tx.Commit` errored after a real commit,
  the claim is attached (delete no-ops), retry replays the real result, and
  the FAILED row is at worst a misleading audit artifact.
- Provider-status `VerificationStatus` pass-through is safe today:
  `internal/provider/provider.go:90` contracts `"reported"` and all three
  adapters hardcode it (chainabuse: "community reports never auto-verify").
- OpenAPI GET explicitly documents "any reader may fetch any result UUID
  (results are not client-private)" — the unscoped `GetByID` is a documented
  capability-URL design, not an accidental IDOR.
- `Reason.VerificationStatus` has `omitempty` — empty-status reasons
  (`NOT_EVALUATED` etc.) omit the field, keeping the OpenAPI enum intact.
- Status vocabulary hygiene: emitted provider statuses ⊂ the OpenAPI
  `ScreeningProviderEntry.status` enum (11 values incl. `ERROR` used by
  both fanout sites); `DISABLED` excluded from fold but visible.
- Sanctions/labels services emit the same `AVAILABLE`/`STALE`/`UNAVAILABLE`
  vocabulary the mapper switches on (`sanctions/model.go:16-18`,
  `label/service.go:285-291`).
- `screened_at`/claim/`claim_stale` all use DB-or-UTC clocks consistently;
  the SELECT-time vs DELETE-time staleness predicates cannot diverge.
