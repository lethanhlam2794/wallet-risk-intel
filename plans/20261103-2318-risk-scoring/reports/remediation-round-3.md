# Stage 4 Remediation — Round 3

**Date**: 2026-11-04
**Responds to**: `reports/review-round-3.md` — verdict CHANGES_REQUESTED.
Round 3 verified all 5 R2 fixes empirically (incl. live-DB inspection of
`wallets.address` and the stored `request` JSONB). One new minor
(R3-M1) + 4 advisory nits. All remediated in code.

## R3-M1 — `pollOrConflict` never re-checked `request_hash` → FIXED

`internal/screening/handler.go`: `pollOrConflict` now takes the
request's `hash` and re-checks `claim.RequestHash != hash` on every
250 ms tick. Previously, an owner failure → claim release → same-key
re-claim under a DIFFERENT payload could land its result mid-poll and be
replayed under `Idempotent-Replay` for this request — a wrong-payload
replay. The mismatch now answers `409 IDEMPOTENCY_CONFLICT`
(same code as the initial-read branch).

Regression: `TestHandler_PollRejectsReclaimedDifferentHash` — real claim
row under a foreign hash → first poll tick returns 409 CONFLICT.
`TestHandler_PollVanishingClaim_Conflict` covers the adjacent
vanished-claim → `IDEMPOTENCY_IN_FLIGHT` branch. Both run against the
real test DB.

## Advisory nits — all addressed

- **`Enrich` received normalized for `rawAddress`** → passes `raw`
  (`service.go`) — consistent with the wallet path now that
  `CanonicalRequest.RawAddress` exists.
- **Stale-claim 5-min literal vs `Deps.Deadline` 30 s** → documented in
  `queries/screenings.sql` header: the bound deliberately exceeds the
  pipeline deadline with wide margin (a live owner attaches in seconds;
  5-min-old open claim can only be a dead owner).
- **`persist` missing nil-guard** → added
  `if s.d.Pool == nil || s.d.Queries == nil` (same guard as
  `persistFailed`).
- **JSONB `null` findings on the success path** →
  `json.Marshal(orEmpty(m.findings))` — the column contract is an
  array; empty now stores `[]` like the FAILED audit row.

## Verification evidence (fresh)

| Command | Result |
|---|---|
| `go build ./...` | Exit 0 |
| `go vet ./...` | Exit 0 |
| `gofmt -l .` | Empty |
| `go test -count=1 -run TestHandler_Poll ./internal/screening/` | 2 PASS |
| `go test -count=1 ./...` | all ok (tests/ 86.421s) |
| `go test -race -count=1 ./internal/scoring/ ./internal/screening/` | ok 1.058s / 11.298s |

New tests this round: `TestHandler_PollRejectsReclaimedDifferentHash`,
`TestHandler_PollVanishingClaim_Conflict` (`handler_test.go`, new file).
