# Stage 4 Remediation — Round 1

**Date**: 2026-11-04
**Responds to**: `reports/review-round-1.md` — verdict CHANGES_REQUESTED
(M1, M2 required; M3 recommended; N1–N6 advisory).

All findings were remediated in code rather than re-documented away.
Fresh verification evidence at the bottom.

## M1 — `dedupe` kept the weakest-evidence representative → FIXED

`internal/scoring/engine.go`: the comparator no longer orders
`VerificationStatus`/`Confidence` lexicographically (alphabet would keep
`disputed`/`low` first). Two explicit rank functions now implement the
documented policy — `verificationRank` (verified 4 > reported 3 >
unverified 2 > disputed 1 > unknown 0) and `confidenceRank`
(high 4 > medium 3 > low 2 > unknown 1 > missing 0) — descending, so the
dedup loop's first-per-key survivor is always the strongest evidence.

Regression coverage: `TestEvaluate_DedupKeepsStrongestEvidence` feeds a
same-`DedupKey` pair (verified+high vs disputed+low SCAM) in both input
orders and asserts the verified representative survives (score 85, not 0;
exactly one reason, `verified`, weight 85).

## M2 — FAILED `screening_status` never persisted → FIXED (plan §3.5 delivered)

`internal/screening/service.go`: on `persist` failure — the only
post-resolution error path — the service now calls `persistFailed`, a
best-effort **audit-only** FAILED row writer:

- Fresh bounded context (`context.Background()` + 10 s) because the
  pipeline ctx may already be dead (deadline/drift).
- Row carries `screening_status=FAILED`, `risk_score` NULL,
  `risk_level=UNKNOWN`, `scoring_policy_version=v1.0.0`, empty findings,
  and `provider_status.fanout.error` holding the failure message.
- **Intentionally not attached to the claim**: the claim is still
  released by the handler, so a same-key retry can re-claim — the FAILED
  row preserves the audit fact without poisoning idempotency.
- If the audit insert itself fails (DB down — the common case for a
  persist failure), it logs a warn and the original 500 still returns.

Regression coverage: `TestService_PersistFailed_AuditRow` invokes
`persistFailed` against the real test DB and asserts row-level FAILED,
NULL score, UNKNOWN level, policy version, and the stored result body's
fanout error.

## M3 — blocks/findings emitted for unusable dimensions → FIXED

Two-layer guard:

1. **Mapper** (`internal/screening/mapper.go`): `foldSanctions` and
   `foldLabels` now emit findings/entries/items only when
   `dim.Usable == true`. Rows that surface under `UNAVAILABLE` (upstream
   contract drift) no longer read as scoring evidence — entries/items
   stay empty, `matched` stays false, no findings are appended.
   (`data_completeness.labels_collected` still reports the raw
   collection count — collection metadata, not scoring evidence.)
2. **Engine** (`internal/scoring/engine.go`): when
   `FindingSet.HasUsableData=false`, `Verdict.Blocks` is zeroed, so a
   shape like `blocks.direct.subtotal:100` beside `risk_score:null` can
   never leak. Reasons still report what was observed (informational).

Regression coverage: `TestService_UnusableDimension_EmitsNoFindings`
(fake sanctions/labels return rows under `UNAVAILABLE` →
`INSUFFICIENT_DATA`, NULL score, empty entries/items, zero blocks, no
`DIRECT_SANCTIONS` flag); `TestEvaluate_NoUsableData_NullScore` extended
with `assert.Equal(Blocks{}, v.Blocks)`.

## Nits

- **N1 (app-vs-DB clock) → FIXED.** `GetScreeningRequestByKey` now
  returns `claim_stale` computed as
  `claimed_at < NOW() - INTERVAL '5 minutes'` in DB time; the handler
  branches on `existing.ClaimStale`, so clock skew can never pick the
  wrong poll-vs-takeover path. `staleClaimAfter` constant removed
  (dead). sqlc regenerated.
- **N2 (`_enrichment` pseudo-key) → FIXED.** `ProviderStatus` gains a
  named `fanout` sibling field (`*ProviderStatusEntry`, omitempty); a
  wholesale fan-out failure is reported there, never inside the
  per-source map — cannot collide with a real source code. OpenAPI
  `ScreeningProviderStatus` documents `fanout`.
- **N3 (pipe-join identity) → FIXED.** `ProviderEvidence.Identity` /
  observation ref identity is now a `json.Marshal` of
  `[category, label, entity]` — collision-free regardless of separator
  characters inside component fields.
- **N4 (dead cap flags) → addressed by documentation.** `Other.Capped`
  already computes `preCap > cap` (dead today because band ≤ 20 < cap
  30, correct when further signals join the block); `Direct.Capped`
  stays false by design — the direct block has no cap rule in v1
  (max-of-weights). Both semantics are now documented at the emission
  site.
- **N5 (`ListScreeningResultsByWallet` unused) → kept, now exercised.**
  It is consumed by `tests/screenings_persistence_test.go` (history
  ordering) and the new `TestService_PersistFailed_AuditRow`; it exists
  for the planned per-wallet history endpoint. Not dead code.
- **N6 (silent poll return on ctx.Done) → FIXED.** `pollOrConflict` now
  emits the truthful `409 IDEMPOTENCY_IN_FLIGHT` envelope instead of a
  bare return.

## Verification evidence (fresh, post-remediation)

| Command | Result |
|---|---|
| `go build ./...` | Exit 0 |
| `go vet ./...` | Exit 0 |
| `gofmt -l .` | Empty |
| `go test -count=1 ./internal/scoring/` | ok 0.013s |
| `go test -count=1 ./internal/screening/` | ok 6.408s |
| `go test -count=1 ./...` | all packages ok (tests/ 81.205s) |
| `go test -race -count=1 ./internal/scoring/ ./internal/screening/` | ok 1.058s / 39.792s |
| `python3 yaml.safe_load(api/openapi.yaml)` | parses; 14 paths, v0.3.0 |

New tests added this round: `TestEvaluate_DedupKeepsStrongestEvidence`,
`TestService_UnusableDimension_EmitsNoFindings`,
`TestService_PersistFailed_AuditRow`, plus the `Blocks{}` assertion in
`TestEvaluate_NoUsableData_NullScore`.
