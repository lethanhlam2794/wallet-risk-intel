# Stage 4 Remediation — Round 2

**Date**: 2026-11-04
**Responds to**: `reports/review-round-2.md` — verdict CHANGES_REQUESTED.
Round 2 verified all 9 round-1 remediation claims true and found 5 new
minors (R2-M1…R2-M5) in code that moved this round. All 5 are fixed in
code with regression coverage.

## R2-M1 — `dedupe` score tiebreak kept the LOWEST provider score → FIXED

`internal/scoring/engine.go`: the final tiebreak (`ProviderScore`) now
orders descending (`si > sj`) — same-key `KindProviderScore` findings
differ only on this axis (status is hardcoded `reported`), so
lowest-first ordering always kept the weakest evidence — the same
mechanism M1 removed, one clause down.

Regression: `TestEvaluate_DedupKeepsHighestProviderScore` — same-`DedupKey`
score pair {30, 75} in both input orders → `blocks.other.subtotal == 10`
(band(75)); lowest-first would leave band(30) = 0.

## R2-M2 — `persistFailed` persisted raw `cause.Error()` → FIXED

`internal/screening/service.go`: the persisted `fanout.error.message` is
now the static sanitized string `"screening persist failed"` (same style
as `foldProviders`' `"enrichment fan-out failed"`). The `cause`
parameter was removed from the signature — raw driver/schema detail can
no longer reach the client-replayable `result` blob.

Regression: `TestService_PersistFailed_AuditRow` updated to assert the
exact sanitized message.

## R2-M3 — unbounded claim-release delete → FIXED

`internal/screening/handler.go`: `DeleteScreeningRequest` on pipeline
failure now runs under `context.WithTimeout(context.Background(), 10s)`
— pool acquisition can no longer hang forever under DB distress, which
is exactly when this path executes.

## R2-M4 — `enrichErr → fanout` emission path untested → FIXED

`internal/screening/service_test.go`:
`TestService_EnrichFanoutError_ReportedOnFanoutField` — an enricher fake
returning `err` yields `provider_status.fanout.status == "ERROR"`,
empty `providers` map (no pseudo-provider keys), and `PARTIAL` overall
status (sanctions leg usable).

## R2-M5 — original address representation lost in screening path → FIXED

`internal/screening/model.go` + `service.go` + `handler.go`:
`CanonicalRequest` gains `RawAddress string` (`json:"-"` — excluded from
the idempotency hash; two spellings of one address remain one request).
The handler populates it from the raw `req.Address`; `run` passes
`raw` → `GetOrCreate(address, normalized)` so `wallets.address` keeps
the original representation like the wallet endpoint does. Programmatic
callers without raw input fall back to normalized (previous behavior).

Regression: `TestService_RawAddressPreserved` — a mixed-case
`RawAddress` flows to `wallets.address` verbatim while
`normalized_address` stays lowercase.

## Advisory nits (round 2) — disposition

- JSONB `null` findings column → already `[]byte("[]")` (non-null).
- `labels_collected` (data_completeness) vs `collected` (block) — dual
  semantics are intentional: collection count vs usable-emitted count;
  documented in the mapper comment added in round 1.
- Mid-iteration cancel → 500 — inherent to sync request semantics;
  documented.
- `claim_stale` flag boundary — boundary value itself untested; the flag
  is exercised by `TestScreenings_*` claim flows and shares the
  byte-identical predicate with the guarded delete (reviewer-verified).
- Engine flag-with-NULL-score residual — `Blocks{}` zeroing covers the
  self-contradicting shape; flags stay informational.

## Verification evidence (fresh, post-remediation)

| Command | Result |
|---|---|
| `go build ./...` | Exit 0 |
| `go vet ./...` | Exit 0 |
| `gofmt -l .` | Empty |
| `go test -count=1 -run '…new tests…' ./internal/{screening,scoring}/` | all PASS |
| `go test -count=1 ./...` | all ok (tests/ 82.703s) |
| `go test -race -count=1 ./internal/scoring/ ./internal/screening/` | ok 1.060s / 9.777s |

New tests this round: `TestEvaluate_DedupKeepsHighestProviderScore`,
`TestService_EnrichFanoutError_ReportedOnFanoutField`,
`TestService_RawAddressPreserved`.
