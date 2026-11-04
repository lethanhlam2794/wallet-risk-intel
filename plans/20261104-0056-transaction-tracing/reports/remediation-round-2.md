# Remediation — review-round-2 (Phase 6)

Verdict in: **CHANGES_REQUESTED** — 1 major (N1), 2 minors (N2, N3),
2 nits (N4, N5). All accepted and fixed.

## N1 (major) — zero-page fetch over-claims `fetch_to`

`internal/transaction/service.go`: the implicit-stream arm
(`streamSeen == 0`) now requires `!budgetStop` for the complete-window
claim. A budget stop before page 1 (head consumed the last request)
leaves `token=""` with zero fetched rows — coverage falls back to the
prior watermark instead of `fetchTo`. Same index-honesty invariant as
B1, closed on the other arm of the switch.

## N2 — recordCap only detected on full pages

`internal/provider/chainscan/adapter.go`: the cap check moved ahead of
`!more` — cumulative `(*cur-1)*size+len(rows) >= recordCap` marks the
stream `Capped` on ANY page (full or short, dividing or not). A short
page landing exactly on the 10k boundary can no longer be read as
stream end. Trade-off (declared): a stream with exactly 10,000 records
is conservatively unfinished — it refetches the tail window rather than
over-claiming coverage.

## N3 — M2 regression didn't reach the M2 branch

`internal/tracing/service_test.go`: `TestExposures_InfraErrorPropagates`
now injects the fault deterministically — a stub `hook` closes the pool
inside the hop-2 node's `LookupTransfers`, so the DB error surfaces
exactly on the `EnsureIndexed` error branch (gctx alive, non-context
error → `return err` → 500), instead of killing the hop-0 index first.

## N4 — ProviderCalls arithmetic

`internal/transaction/service.go`: `out.ProviderCalls` now accumulates
`page.UpstreamCalls` per step on top of the prepaid head call — the
value is the real upstream call count (1 + Σ calls), not a steps
approximation.

## N5 — dead field

`ixState.claimed` removed.

## Gate re-run

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./internal/tracing ./internal/transaction ./internal/provider/chainscan` | pass |
| `go test -race` (same pkgs, run in r1) | clean — re-run pending for r3 |

## Round-3 follow-up (non-blocking findings, closed pre-Stage-5)

- **O1 (minor):** added `TestEnsureIndexed_ZeroPageBudgetStop` —
  budget=1 (head consumes pool) ⇒ `status=partial`, watermark stays
  below fetchTo, 0 rows, 0 lookup calls. The bug now fails a test.
- **O2 (minor, pre-existing):** `max_provider_requests` reporting under
  `max_hops=1` is causal-free — scheduled for Phase 7 cleanup.
- **O3 (nit):** pinned the ascending-order contract on `TransferPage`
  (provider.go) — the per-stream max−1 watermark formally depends on it.
