# Warm Index Queue v1 — Implementation Report

**Plan**: `plans/20261105-2205-warm-index-queue/plan.md`
**Contract**: `docs/prompts/20261105-2155-codex-warm-index-queue.md`
**Status**: all four phases implemented, verified, and marked `status: completed`.
No commits were made; the review gate audits `git diff`.

## What shipped

A bounded, feature-flagged (`WARM_INDEX_ENABLED`, default **off**) background
lane that deep-indexes hop-1 wallets which already produced risk signals
during tracing. One item per poll cycle drains through a quota-aware worker
using `transaction.Service.EnsureIndexed` via per-provider single-member
adapter lanes (D1). Interactive indexing is untouched: the API diff is one
import, one `SetEnqueuer` call, and a no-op-by-default `Queue`.

## Files changed

| File | Change |
|---|---|
| `apps/api/migrations/000023_warm_index_queue.up.sql` | **new** — `warm_queue` (status CHECK, address CHECK `^0x[0-9a-f]{40}$`, partial unique index on `(network_id,address)` for pending/claimed, ordering index `priority DESC, id ASC`) + `warm_quota_ledger` PK `(provider_code, period_start)` |
| `apps/api/migrations/000023_warm_index_queue.down.sql` | **new** — drops ledger then queue |
| `apps/api/queries/warm_index.sql` | **new** — `EnqueueWarmItem`, `ResetExpiredWarmClaims`, `ClaimNextWarmItem` (`FOR UPDATE SKIP LOCKED`), `CompleteWarmItem`, `FailWarmItem`, `ReleaseWarmClaim`, `GetWarmQuotaCalls`, `AddWarmQuotaCalls`, `KillWarmItem` |
| `apps/api/internal/db/warm_index.sql.go`, `internal/db/models.go` | sqlc-generated (`make sqlc`) — `WarmQueue`, `WarmQuotaLedger` |
| `apps/api/internal/warmindex/{warmindex,queue,drain}.go` | **new** — `Config`, `ProviderLane`, `Queue.Enqueue`, `Drainer.Run/RunOnce`, `MaskAddr`, constants (`LeaseDuration=15m`, `DrainTimeout=5m`, `MaxAttempts=3`, `PrioritySanctionsMatch=100`, `PriorityRiskyLabel=50`) |
| `apps/api/internal/warmindex/{queue,drain}_test.go` | **new** — 19 integration tests (real DB, `-race` clean) |
| `apps/api/internal/tracing/warm.go` | **new** — `Enqueuer` interface, `SetEnqueuer`, `warmEnqueuePlan` (pure), `warmEnqueue` (fail-open, detached 2s ctx) |
| `apps/api/internal/tracing/service.go` | +`enqueuer`/`log` fields, discard-logger default, 3-line hook inside the hop-1 `rootSet.order` loop after signal filtering, sentinel-gated |
| `apps/api/internal/tracing/warm_test.go` | **new** — pure decision table + 7 Exposures-level hook tests |
| `apps/api/internal/config/config.go` | +6 `WarmIndex*` fields, strict `envBool` (`strconv.ParseBool` vocabulary — garbage → boot error), `splitCSV` |
| `apps/api/internal/config/config_test.go` | +4 tests: defaults, overrides, bool vocabulary, numeric parse errors |
| `apps/api/cmd/worker/main.go` | `-mode ingest\|warmdrain` dispatch (default preserves ingest byte-for-byte), `runWarmDrain` with its own `signal.NotifyContext` — no Redis, downloaders, sanctions/labels services |
| `apps/api/cmd/worker/warmproviders.go` | **new** — `buildWarmLanes`/`buildWarmAdapter`/`warmLaneBudget`: all 13 provider ctors mirrored from `cmd/api`, per-lane boot checks (data_sources row + positive budget), exact `ReorgTail`/BSC-only `WindowCap` mirror, per-lane `SetStatusChecker` |
| `apps/api/cmd/api/main.go` | +`warmindex` import; `tracingSvc.SetEnqueuer(warmindex.NewQueue(queries, cfg.WarmIndexEnabled, log), log)` after `buildTransactions` |
| `apps/api/.env.example` | +6 `WARM_INDEX_*` vars with defaults (commented — feature dark) |
| `plans/20261105-2205-warm-index-queue/phase-0{1..4}-*.md` | frontmatter `status: completed`, success criteria `[x]` |

`apps/api/docs/pipeline-log.md` was a **pre-existing baseline modification**
(Stage-1 planning entry); untouched by implementation.

## Migration status

- Applied to `check_wallet_warmindex_test` on `127.0.0.1:5433`; up → down →
  re-up all clean during Phase 1. No existing tables modified.
- Dev DB `check_wallet` received 000023 via `database.Migrate` during the
  warmdrain smoke boot (expected — migrations run at worker boot).
- **Rollback** (R3): `./cmd/migrate`-compatible down migration drops
  `warm_quota_ledger` then `warm_queue`; the API-side `Queue` is a pure
  no-op when `WARM_INDEX_ENABLED` is unset, so a rolled-back schema leaves
  enqueue as `pg UndefinedTable` errors that are logged fail-open and never
  affect screening results.

## Test coverage vs. contract acceptance tests

| Required test | Where |
|---|---|
| Concurrent drains never claim the same item (SKIP LOCKED) | `TestDrainer_ConcurrentClaimsDisjoint` |
| Duplicate `(net,addr)` enqueue → one row, priority monotone | `TestQueueEnqueue_DedupeMonotone` |
| Ledger at cap → drain skips, zero provider calls | `TestDrainer_LedgerCapSkip` |
| Killswitch-inactive provider → not attempted, stays pending | `TestDrainer_KillswitchSkip` (+`DisabledAdapterSkip`, `LaneOrderAndCapFallthrough`) |
| Crash mid-claim → pending after lease expiry, attempts+1 (D10) | `TestDrainer_LeaseExpiry` |
| EnsureIndexed failure → attempts+1, masked log, ≥3 → dead | `TestDrainer_FetchFailureDeadLetter`, `InfraFailure` |
| Fresh `partial` row → drain makes real upstream calls (Refresh) | `TestDrainer_RefreshDeepensFreshPartial` |
| GAMBLING/EXCHANGE neighbor → no enqueue | `TestExposures_WarmEnqueueGamblingExchange` + plan-table rows |
| Sentinel → no enqueue | `TestExposures_WarmEnqueueSentinelSkipped` |
| Enqueue error → Exposures still succeeds | `TestExposures_WarmEnqueueFailOpen` |
| Sanctions priority 100, risky label 50, `label:<CAT>@<net>` for foreign | `TestWarmEnqueuePlan`, `TestExposures_WarmEnqueue{Sanctions,RiskyNeighbor,ForeignLabel}` |
| Hop-1-only hook | `TestExposures_WarmEnqueueHop2Skipped` |
| Disabled queue/drainer no-op | `TestQueueEnqueue_DisabledNoop`, `TestDrainer_DisabledRunExits` |
| Cooldown-served failed row → no traffic, release pending | `TestDrainer_CooldownFailedNoTraffic` |
| Config defaults/overrides/strict-bool/numeric errors | `TestLoad_WarmIndex*` (config_test.go) |

## Commands + exit codes

| Command | Result |
|---|---|
| `make sqlc` | 0 — generated `warm_index.sql.go` + models |
| migration up/down/re-up on `check_wallet_warmindex_test` | 0 each |
| `go build ./...` | 0 |
| `go vet ./...` | 0 |
| `gofmt -l .` | empty output |
| `go test ./internal/warmindex/` | ok 10.7s |
| `go test ./internal/warmindex/ -race -count=1` | ok 10.3s |
| `go test ./internal/tracing/` | ok 15.5s |
| `go test ./internal/config/` | ok |
| `go test ./...` | **all packages ok** incl. `checkwallet/tests` 67.9s |
| `WARM_INDEX_ENABLED=false worker -mode warmdrain` | logs "disabled…exiting", **exit 0** |
| `WARM_INDEX_ENABLED=1 WARM_INDEX_PROVIDERS=bogus_xyz worker -mode warmdrain` | `worker: warm index: provider "bogus_xyz" has no resolved config`, **exit 1** |
| `worker -mode xyzzy` | `worker: unknown -mode "xyzzy"`, **exit 1** |
| `WARM_INDEX_ENABLED=1 worker -mode warmdrain` | `warm drain loop started lanes=2 daily_cap=200`; SIGTERM → **exit 0** |

## Deviations and accepted limitations

None beyond the plan's declared trade-offs:

- **Undercount window**: a crash between `EnsureIndexed` and the ledger write
  loses those calls — accepted v1, bounded by `WARM_INDEX_ITEM_BUDGET`.
- **Soft quota**: concurrent drains may overshoot `WARM_INDEX_DAILY_CAP`
  slightly — accepted soft-cap.
- **`done` semantics**: "one deeper pass per enqueue," not complete history.
- **Worker-local provider factory** duplicates cmd/api constructor wiring —
  the plan's accepted trade-off vs. refactoring the API path.
- **`source_screening_id`** is stored but always `NULL` in v1 (column is
  future-proofing for screening-join attribution).
- **`blockscout`/`blockscout_pro` defaults** enable ethereum only in this
  env; BSC items correctly stay `pending` until a bsc-enabled provider is
  allowlisted.
- **One shared `StatusCache`** is passed to both `buildWarmLanes` (per-lane
  `SetStatusChecker`) and `NewDrainer` — killswitch parity with the API.
