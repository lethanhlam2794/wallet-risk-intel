---
title: "Warm Index Queue v1 — signal-prioritized background deep-indexing"
status: completed
created: 2026-11-05
feature_slug: warm-index-queue
blockedBy: []
risk_level: R3
source_prompt: docs/prompts/20261105-2155-codex-warm-index-queue.md
---

# Warm Index Queue v1

## Overview

A bounded, feature-flagged (`WARM_INDEX_ENABLED`, default **off**) background
lane that deep-indexes wallets which already produced risk signals during
tracing, draining one item per cycle through a quota-aware worker so leftover
free-tier provider quota is spent without ever starving interactive requests.

Interactive indexing (`transaction.Service.EnsureIndexed` + singleflight +
budgets + killswitch) is **untouched** — the warm lane consumes the same
public seam with its own background budget and extended `MaxPages`.

## Verified Anchors (scouted, do not re-derive)

- `internal/transaction/service.go:179` — `EnsureIndexed(ctx, net, normalized, EnsureOptions)`;
  `EnsureOptions{Refresh, Budget *Budget, MaxPages}` (`model.go:43`);
  `NewBudget(n)` (`model.go:79`, n≤0 = unlimited).
- Provider fetch failures surface as `*IndexOutcome{IndexStatus:"failed", FetchErr}`
  with **nil error** (`service.go:563-565`) — drain must read the outcome, not
  just `err`. Infra errors return `(nil, err)`.
- `StatusChecker` iface `Active(ctx, code) bool` (`service.go:83`);
  `SetStatusChecker` (`service.go:122`); impl `providerconfig.NewStatusCache(q, 30s, …)`
  (`status_source.go:44`).
- `provider.TxProvider` iface: `Name()`, `Enabled(net)`, `HeadBlock`, `LookupTransfers`
  (`provider/provider.go:282-287`). Per-network failover chains live in
  `Service.adapters[net.Code]` — **restricting the chain restricts spending**.
- `internal/tracing/service.go:421-444` — hop-1 items loop over `rootSet.order`;
  `s.signals(cp, labelIdx[cp], lookups[cp])` (`:1014`) returns `[]MatchedSignal`;
  kinds `sanctions_direct|label|listing` (`model.go:31-35`); label `Category` =
  `risk_categories.code` (seeded `migrations/000005_labels.up.sql:16-37` —
  SCAM/PHISHING/HACK/RANSOMWARE/MIXER/GAMBLING/EXCHANGE all present).
- `cmd/worker/main.go` — one-shot `-source` ingestion CLI (not a daemon);
  graceful shutdown to be added for drain mode. `internal/ingestion/locks.go`
  advisory-lock precedent (not needed — SKIP LOCKED is the queue).
- `internal/providerconfig` — `NewKeystore(cfg.ProviderConfigKEK)` +
  `ResolveAll(ctx,cfg,q,keys)` required before building keyed adapters
  (cmd/api/main.go:116-124).
- cmd/api/main.go:421-435 — provider registry (`map[code]TxProvider`, 13 codes);
  budget map `:447-461`; data_sources boot check `:466-473`;
  `transaction.New(..., adapters, wallets, transaction.Config{...}, log)` `:475-491`.
- `internal/config/config.go` — `envOr/envInt/envInt64/envDur` helpers; **no
  `envBool` exists** (add one).
- Migration head = `000022` → **`000023` is free**; FK convention
  `REFERENCES blockchain_networks(id)` (000001:15-16).
- `screening_results.id` is `UUID` (000007:15-16) → `source_screening_id UUID NULL`.
- `network.Resolver` resolves by **code** only (`resolver.go:39-46`) — drainer
  maps `network_id → Network` via `resolver.List()` scan at construction.
- `wallet.NormalizeEVM` (`internal/wallet/address.go:28`) — normalized =
  lowercase 0x-prefixed 20-byte hex.
- Test DB: `testutil.RequireTestPoolDSN(t, "…/check_wallet_warmindex_test")`
  (per-package DB convention, `transaction/service_test.go:22`).
- `sqlc` v1.30.0 installed; `make sqlc` regenerates `internal/db`.
  `google/uuid v1.6.0` already in go.mod.
- Docker dev stack (postgres :5433, redis :6380) — `make deps-up` if down.

## Design Contract Decisions (v1)

| # | Decision | Rationale |
|---|---|---|
| D1 | **Per-provider single-member `transaction.Service` lanes.** For each allowlisted provider code, build `map[netCode][]TxProvider{net:[p]}` at worker boot; drain selects the first allowlisted provider (in `WARM_INDEX_PROVIDERS` order) that is `Enabled(net)` AND killswitch-active AND under daily cap. | Contract reads "the **first** enabled provider" + per-provider quota → exact per-provider call attribution; failover can never spend a non-selected provider's quota; interactive lane byte-identical. |
| D2 | **Claim = one atomic statement.** `UPDATE warm_queue SET status='claimed', claimed_at=now() WHERE id=(SELECT id FROM warm_queue WHERE status='pending' ORDER BY priority DESC, id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING …`. Lease sweep (`claimed_at < now()−15m → pending`) runs as a separate UPDATE at cycle start. | Cross-process safe; matches contract ordering (claim → eligibility check → release-if-none → index). |
| D3 | **Outcome table keyed on `out.ProviderCalls` (pc)** — the honest "attempted upstream call" discriminator (incremented on the HeadBlock attempt itself): `pc>0 && status∈{fresh,partial}` → `done`, `ledger+=pc`. `pc>0 && status=failed` → `attempts+1` (→`dead` at ≥3), `ledger+=pc`. `pc==0` any status (served-fresh/partial, cooldown-served failed, disabled, indexing-claim, BudgetExhausted) → release `pending`, **no bump**, `ledger+=0`. `(nil,err)`: `ctx.Err()` → release pending under detached ctx (shutdown) else `attempts+1`, log `infra` vs `fetch`. | ProviderCalls>0 *is* the contract's "attempted upstream call"; cooldown-served `failed` can't phantom-bump. |
| D3a | **`EnsureOptions.Refresh = true` on every drain.** Without it, `ensure():265` serves the just-indexed `partial`+fresh row (the *exact* warm-queue input — interactive MaxPagesHopN=2 leaves `partial`) and the item completes with zero deepening. Refresh bypasses freshness only (FAILED `NegativeTTL` cooldown holds, model.go:44) and re-scan is watermark-incremental `[wm−tail+1,head]` — cheap for fresh rows, genuinely deep for partial ones (MaxPages=10 vs 2). | Blocking fix from Critic round 1. |
| D4 | **Enqueue upsert.** `INSERT … ON CONFLICT (network_id,address) WHERE status IN ('pending','claimed') DO UPDATE SET priority=GREATEST(priority,EXCLUDED.priority), reason=CASE WHEN EXCLUDED.priority>priority THEN EXCLUDED.reason ELSE reason END, updated_at=now()`. done/dead rows don't block re-enqueue (fresh signal = fresh cycle). | Idempotent dedupe keyed to the partial unique index; monotone priority. |
| D5 | **Enqueue hook in `tracing` via consumer-defined `Enqueuer` iface + `SetEnqueuer(e, log)`** (mirrors `SetMetrics`). Decision fn `warmEnqueuePlan(sigs)` (pure, table-tested): `sanctions_direct` sig → (100,"sanctions_direct_match"); `label` sig Category∈{SCAM,PHISHING,HACK,RANSOMWARE,MIXER} → (50,"label:<CAT>"); foreign-network `label` sig (sig.Network set) with risky Category → (50,"label:<CAT>@<net>") — prioritization input, never a direct-listing claim; `listing` sigs, GAMBLING/EXCHANGE, DRAINER/MONEY_LAUNDERING/TERRORISM/DARKNET/EXTREMISM/FROZEN (contract-narrowed set), sentinels → **no enqueue**. Called per hop-1 cp inside `len(sigs)>0` branch, skipping `sentinels[cp]`; `screeningID`=nil (no producer in v1); enqueue runs under `context.WithTimeout(context.WithoutCancel(ctx),2s)`; fail-open: errors logged, never returned. | Acyclic ownership; contract's risky-label set is deliberately narrower than the seeded vocabulary — recorded as a decision, not a surprise. |
| D6 | **Worker-local provider factory** in `cmd/worker` — a `switch` over provider codes constructing only allowlisted adapters (same constructor args as cmd/api, minus metrics hooks — worker has no scrape endpoint). Unknown code → boot error naming it. `ProviderLane.Code = Adapter.Name()` (identity chain: checker/ledger/data_sources/budget all key on `Name()`). | Avoids refactoring `buildTransactions` (zero diff on interactive path). |
| D7 | **Config envs**: `WARM_INDEX_ENABLED` (`strconv.ParseBool` vocabulary — `1/true/false`… garbage → boot error), `WARM_INDEX_PROVIDERS` ("blockscout,blockscout_pro"), `WARM_INDEX_DAILY_CAP` (200), `WARM_INDEX_POLL_INTERVAL` (60s), `WARM_INDEX_ITEM_BUDGET` (8), `WARM_INDEX_MAX_PAGES` (10). Constants: lease 15m, maxAttempts 3, priorities 100/50. `.env.example` documents all. Effective per-drain spend = `min(itemBudget, cfg.Budget[laneName])` — documented. | Contract-fixed values stay constants; tunables stay envs. |
| D8 | **Address guard**: DB `CHECK (address ~ '^0x[0-9a-f]{40}$')` + `wallet.NormalizeEVM(addr)==addr` in Enqueue; invalid → error (caller logs, fail-open). `network_id` unresolvable via `resolver.List()` → `dead` + error log (permanently unprocessable). | Invariant at DB boundary + app layer. |
| D9 | **cmd/worker `-mode ingest|warmdrain`** (default `ingest` byte-identical). `warmdrain`: disabled flag → log+exit 0; else **own** `signal.NotifyContext` (never `run()`'s OFAC+30m timeout); minimal deps — config → migrate → pool → queries → resolver → keystore/`ResolveAll` → lanes → drainer (no Redis, no fetchers). Mid-drain cancel → release claim via detached ctx. | Own ctx prevents the ~40m inherited timeout killing the daemon. |
| D10 | **Lease reset = failed attempt.** `ResetExpiredWarmClaims` sets `attempts=attempts+1` and `status = CASE WHEN attempts+1>=3 THEN 'dead' ELSE 'pending' END` — a poison pill (panic/OOM mid-drain) reaches `dead` in 3 strikes instead of re-claiming forever; graceful SIGTERM releases *without* bump (true crashes only). `todayUTC` computed once per cycle for check+add. | Prevents infinite reset loops; contract silent → explicit decision. |

## Phase Roadmap

| Phase | Title | Depends | Files |
|---|---|---|---|
| 01 | Schema + sqlc queries | — | `migrations/000023_*`, `queries/warm_index.sql`, `internal/db` (gen) |
| 02 | `internal/warmindex` package (enqueue + drainer + ledger) | 01 | `internal/warmindex/*` |
| 03 | Tracing enqueue hook | 02 | `internal/tracing/warm.go`, `service.go` |
| 04 | `cmd/worker` warmdrain + config envs + **cmd/api wiring** | 02,03 | `cmd/worker/*`, `internal/config/config.go`, `cmd/api/main.go`, `.env.example` |

## Acceptance Criteria (from prompt — hard tests)

- Concurrent drains never claim the same item (SKIP LOCKED).
- Duplicate `(net,addr)` enqueue → one row, monotone priority.
- Ledger at cap → cycle skipped, zero provider calls.
- Killswitch-inactive provider → item untouched, stays `pending`.
- Crash mid-claim → lease expiry returns it to `pending`.
- `EnsureIndexed` failure → `attempts+1`, masked-addr log, no panic; ≥3 → `dead`.
- GAMBLING/EXCHANGE neighbor → no enqueue; enqueue error → screening unaffected.
- `gofmt -l .` empty, `go build`, `go vet`, `go test ./...` all clean.

## Risk Assessment

- **R3 — background lane touches live indexing path**: mitigated by dedicated
  per-provider lanes (D1), killswitch parity, and the default-off flag.
- Partial-unique-index `ON CONFLICT` inference must name the same `WHERE`
  clause — `make sqlc` success is a phase-01 gate (early detection).
- `warm_quota_ledger` increments after `EnsureIndexed` returns; a crash between
  the call and the ledger write — or a `(nil,err)` after calls were spent —
  under-counts (calls already burned). Accepted v1: bounded by
  `WARM_INDEX_ITEM_BUDGET`, documented in code.
- One bounded drain pass can still leave a busy wallet `partial` (BSC
  `WindowCap` = 100k blocks/attempt) yet marks it `done` — v1 semantics are
  "one deeper pass per enqueue", not "complete history"; documented.
- Poison-pill items (panic/OOM mid-drain) are bounded by lease-reset attempts
  (D10); `selectLane`-miss cycles log at info so operators see *why* items
  wait instead of silently churning claim→release.
- No `data_sources` row for a warm provider → drain-time infra error; the
  worker mirrors cmd/api's boot check and fails fast instead (loud > silent).

## Stage 1 Debate Summary

**Devin Critic session:** `rightful-lemon` (2 rounds). **Verdict: `PLAN_CONSENSUS_REACHED`** (round 2, 3 non-blocking nits absorbed into the docs).

### Challenges raised (round 1)

- **[A1, blocking, conceded]** Producer never wired: `SetEnqueuer` existed but nothing called it — `Exposures` runs only inside cmd/api. → Phase-04 now owns `cmd/api/main.go` wiring (always-wire + Queue-level flag no-op).
- **[A2, blocking, conceded]** Deepening defect: without `Refresh:true`, `ensure():265` serves the just-indexed `partial`+fresh row → `done` with zero upstream calls. → D3a mandates Refresh; regression test added.
- **[A3, conceded]** Outcome mapping ambiguous: `Fetched=false,status=failed` covered both cooldown-served (0 calls) and head-failure (1 call). → decision table re-keyed on `ProviderCalls` (incremented on the HeadBlock attempt itself) — critic judged this "genuinely better" than its own `Fetched` suggestion.
- **[A4–A6, decided]** unresolvable `network_id`→`dead`; lease-reset bumps `attempts` (D10 — contract-silent, recorded deviation); detached-ctx release; `WithoutCancel`+2s enqueue; sentinel skip; foreign `label` sigs enqueue as `label:<CAT>@<net>`; `ProviderLane.Code=Adapter.Name()`; own `signal.NotifyContext`; `ParseBool`; verbatim `ReorgTail`/`WindowCap`; lane-miss info log; `CHECK` on address; sqlc grammar as phase-01 gate.

### Residual nits (absorbed)

- `pc>0 && other status` catch-all → `attempts+1` (node-lag/`head<fetchFrom` spends a call without coverage).
- Per-drain ctx bound pinned: `DrainTimeout=5m < LeaseDuration=15m`.
- `buildWarmLanes` signature carries `pool`+`resolver` honestly.

### Session audit

| Role | Session | Rounds |
|---|---|---|
| Devin Critic | `rightful-lemon` | 2 (round-1 output truncated mid-run by read-only mode; resumed → full verdict + consensus) |
