---
phase: 2
title: "internal/warmindex — queue + quota-aware drainer"
status: completed
priority: P1
dependencies: [1]
---

# Phase 2: `internal/warmindex` package

## Overview

Core package owning the queue write path (`Enqueue`) and the drain logic
(`Drainer`) — claim ordering, lease expiry, provider eligibility, daily-cap
ledger, outcome mapping, masked logging. UI-free; all business rules live here.

## Requirements

- `Enqueue(ctx, netID int64, addr string, priority int, reason string,
  screeningID *uuid.UUID) error` — idempotent upsert; validates
  `wallet.NormalizeEVM(addr) == addr`; **no-op (nil) when disabled**.
- `Drainer.RunOnce(ctx) (workDone bool, err error)` — one cycle
  (`todayUTC` computed **once** for both quota check and add):
  1. `ResetExpiredWarmClaims(now − lease, MaxAttempts)` (lease=15m const;
     reset **bumps attempts** per D10 — poison pills reach `dead`).
  2. `ClaimNextWarmItem` → none: return false.
  3. Resolve `network.Network` from `network_id` (resolver.List scan);
     **unresolvable → `dead` + error log** (permanently unprocessable).
  4. `selectLane`: first provider in `providers` order where
     `adapter.Enabled(net)` && `checker.Active(code)` &&
     `ledger.calls < cap` → none: `ReleaseWarmClaim` + info log
     (`"no warm lane under cap/active"`, provider/network/attempt) —
     item stays `pending`, no attempt bump.
  5. `EnsureIndexed` under a `DrainTimeout`-bounded ctx (**5m const —
     strictly < LeaseDuration** so a slow-but-legit drain can't be
     lease-swept mid-flight by a peer) with
     `EnsureOptions{Refresh: true, Budget: NewBudget(itemBudget),
     MaxPages: maxPages}` — **Refresh is mandatory** (D3a): the input is by
     construction a just-indexed `partial`+fresh row; without Refresh the
     freshness gate serves it and nothing ever deepens.
  6. Outcome mapping per the **D3 decision table** below; ledger
     `+= out.ProviderCalls` when >0 via `AddWarmQuotaCalls(code,todayUTC,calls)`.
  7. `ctx.Err()` mid-drain → release to `pending` under a **detached ctx**
     (`context.WithoutCancel` + 5s timeout — release under the dead ctx would
     strand the claim for a lease), return err.
- `Drainer.Run(ctx)` — ticker loop at `pollInterval`; `RunOnce` per tick;
  exits on ctx done; **immediate exit when disabled** (log + return).
- Masked addr in logs: `maskAddr("0x1234…cdef")` — first 6 + last 4 chars.
- Log fields on failures: `provider_code`, `network`, `addr` (masked),
  `attempts`, `kind` (`infra`|`fetch`), `error` — never api keys/env values.

## Outcome Decision Table (evaluation order pinned — Critic A3)

`pc := out.ProviderCalls` (0 when `out == nil`) — **the** "attempted upstream
call" discriminator (incremented on the HeadBlock attempt itself).

| Condition | Action |
|---|---|
| `(nil, err)` && `ctx.Err() != nil` | release→`pending` (detached ctx), propagate — shutdown |
| `(nil, err)` non-ctx | `attempts+1` (`dead` at ≥3), log `kind=infra`, no ledger data |
| `pc == 0` (served fresh/partial, cooldown-served `failed`, `disabled`, live `indexing` claim, `BudgetExhausted`, `not_indexed`) | release→`pending`, **no bump**, `ledger+=0` — *never attempted* |
| `pc > 0` && `IndexStatus ∈ {fresh, partial}` | `done`, `ledger+=pc` |
| `pc > 0` && `IndexStatus == failed` (`FetchErr` set) | `attempts+1` (`dead` at ≥3), `ledger+=pc`, log `kind=fetch` |
| `pc > 0` && any other status (e.g. `head < fetchFrom` node-lag/>tail-reorg release — call spent, no terminal coverage) | `attempts+1` (`dead` at ≥3), `ledger+=pc` |

Note: with `Refresh=true` the served-`fresh|partial` branch is unreachable in
practice; the `pc==0` rule still guards it honestly.

## Architecture

```text
warm_queue ──claim(SKIP LOCKED)──▶ selectLane(allowlist order,
   │                               Enabled && Active && calls<cap)
   │                                    │none → release→pending
   │                                    ▼
   │                    per-provider transaction.Service
   │                    (single-member chain → EnsureIndexed)
   │                                    │
   ▼                                   ▼
 done ◀──fresh/partial── outcome ──failed──▶ attempts+1 → dead(≥3)/pending
                                    └─ ledger[code,today] += ProviderCalls
```

Constants: `LeaseDuration=15m`, `DrainTimeout=5m` (< lease, per nit-2),
`MaxAttempts=3`, `PrioritySanctionsMatch=100`,
`PriorityRiskyLabel=50` (exported — tracing maps signals to them).

Config struct:
`{Enabled bool; Providers []string; DailyCap int; PollInterval, ItemBudget int; MaxPages int}`.

`ProviderLane{Code string /* = Adapter.Name() — checker/ledger/data_sources/budget all key on it */; Adapter provider.TxProvider; Tx *transaction.Service}`
— caller (cmd/worker) builds one lane per allowlisted provider with a
**single-member** adapter chain per network (D1); `checker` = shared
`transaction.StatusChecker` (StatusCache satisfies it).

Budget note: `attemptFetch` applies `cfg.Budget[laneName]` **on top of** the
per-item `Budget` — effective per-drain spend = `min(itemBudget, laneBudget)`.
Lanes are built with the same `TXINDEX_BUDGET_*` values; document the clamp in
code (a `WARM_INDEX_ITEM_BUDGET > TXINDEX_BUDGET_*` silently clamps).

## Related Code Files

- Create: `internal/warmindex/warmindex.go` (package doc + Config + constants),
  `queue.go` (Queue/Enqueue), `drain.go` (Drainer/selectLane/maskAddr),
  `queue_test.go`, `drain_test.go`
- Modify: none

## Implementation Steps (TDD)

1. RED: `queue_test.go` — dedupe (same net,addr → 1 row), priority bump
   monotone, done/dead row doesn't block re-enqueue, invalid addr rejected,
   disabled → no-op. Uses `testutil.RequireTestPoolDSN(t,
   "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_warmindex_test?sslmode=disable")`.
2. GREEN: `Queue`/`Enqueue` via `EnqueueWarmItem`.
3. RED: `drain_test.go` — claim ordering (priority DESC, id ASC);
   concurrent claims via two Drainers never double-claim (distinct ids or one
   empty); lease expiry (claimed_at backdated → reclaimed **with attempts+1**);
   dead-letter at 3 fails; ledger-cap skip (seed `calls ≥ cap` → RunOnce false,
   row still pending, zero stub calls); killswitch skip (fake checker → stays
   pending, zero calls); outcome mapping with a stub `provider.TxProvider` +
   real `transaction.Service` (stub style of `failover_test.go`).
   **Regression test for A2**: seed a fresh `partial` `wallet_index_state`
   row for the warm provider (as the interactive lane just left it) → drain →
   assert the stub observed real `LookupTransfers` calls (deepening happened).
   Cooldown regression: seed `failed` row inside `NegativeTTL` → drain →
   pending, `attempts` unchanged, zero calls.
4. GREEN: `Drainer` implementation.
5. REFACTOR: constants/comments tidy; `go vet` clean.

## Success Criteria

- [x] All acceptance-criteria tests pass against the real test DB.
- [x] `Enqueue` is a no-op when disabled and fail-open by contract (returns
  error — callers log; Enqueue itself never panics).
- [x] Failed upstream drains never reach `done`; `attempts` only bumps on
  attempted indexing.
- [x] `go test ./internal/warmindex/ -race` green.

## Risk Assessment

- Crash between EnsureIndexed and ledger write — or a `(nil,err)` after calls
  were spent — under-counts quota (calls already burned). Accepted v1:
  bounded by item budget; documented in code comment.
- `selectLane` cap check races two workers → slight over-spend possible;
  accepted (daily cap is soft quota, not a hard SLO).
- Stub TxProvider must satisfy the real interface — reuse the
  failover-test stub pattern, never mock `transaction.Service` itself.
- A long-running legit drain can't realistically exceed the 15m lease
  (itemBudget ≈ 8 calls ≪ 15m), so D10's attempt bump only fires on true
  crashes/kills.
