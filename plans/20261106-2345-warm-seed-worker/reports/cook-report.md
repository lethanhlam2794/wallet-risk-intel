# Cook Report — Warm Seed Worker (`-mode warmseed`)

**Plan**: `plans/20261106-2345-warm-seed-worker/plan.md` (3 phases, all `completed`)
**Binding prompt**: `docs/prompts/20261106-2322-claude-code-proactive-warm-seed.md`
**Risk classification**: R2 — normal business logic (worker batch), flag-gated, upsert-only writes.
**Executed**: 2026-11-06/07, TDD order — Phase 1 artifacts → Red (seed_test.go fails: `undefined: Seeder`) → Green → Refactor → Phase 3 wiring → gates + real CLI smoke.

## Files changed

| File | Change |
|---|---|
| `apps/api/queries/warm_seed.sql` | NEW — 3 keyset-paginated SELECTs (`ListWarmSeedSanctionsBatch`, `ListWarmSeedLabelBatch`, `ListWarmSeedIndexStateBatch`), `sqlc.narg`/`sqlc.arg` convention, LEFT JOIN keeps NULL/inactive-net rows visible |
| `apps/api/internal/db/warm_seed.sql.go` | NEW (generated, sqlc v1.30.0) — regeneration churn in 4 unrelated `.sql.go` files (comment-only) reverted so `internal/db` diff is scoped to this file only |
| `apps/api/internal/config/config.go` | EDIT — `WarmSeedBatchSize` field + `envInt("WARM_SEED_BATCH_SIZE", 500)` beside `WarmIndex*` block (malformed → boot error; `<1` rejected by `Seeder.Run`) |
| `apps/api/internal/warmindex/seed.go` | NEW — `Seeder`, `NewSeeder`, `Run`, shared `scanBatches` keyset loop, `enqueueGated` gate chain, `seedStats` six-counter struct |
| `apps/api/internal/warmindex/seed_test.go` | NEW — 9 tests; reuses `testDSN`, `netByCode`, `warmRow`, `warmRowCount`, `addrW*` from `queue_test.go`/`drain_test.go` (no redeclarations) |
| `apps/api/cmd/worker/main.go` | EDIT — `case "warmseed"` + `runWarmSeed()` (mirrors `runWarmDrain` minus Redis/lanes/resolver), `-mode` help text |
| `plans/20261106-2345-warm-seed-worker/phase-0{1,2,3}-*.md` | frontmatter → `status: completed`, success criteria checked |

Untouched per binding prompt: `drain.go`, `queries/warm_index.sql`, `tracing/warm.go`, 50/100 priorities, migrations, `api/openapi.yaml`, `.env*`, `go.mod`/`go.sum`, no `internal/provider/trongrid` import.

## Counter names (per-source log line, all six keys always emitted)

`source` ∈ `ofac_list` | `risky_label` | `refresh_stale`; keys: `scanned`, `enqueued`, `skipped_non_evm`, `skipped_inactive_net`, `skipped_fresh`, `errors`. Totals line `warm seed complete` emits the same six. `enqueued` = accepted upsert **attempts** (dedupe/disabled no-ops count honestly).

## Enqueue-path call graph

```
cmd/worker main.go: -mode warmseed → runWarmSeed()
  config.Load() → WARM_INDEX_ENABLED=0 → log + exit 0 (no DB touched)
  database.Migrate → database.Connect
  warmindex.NewQueue(queries, cfg.WarmIndexEnabled, log)
  warmindex.NewSeeder(queries, queue, cfg.WarmSeedBatchSize, log)
  Seeder.Run(ctx)
    ├─ seedSanctions   → scanBatches(fetch=Queries.ListWarmSeedSanctionsBatch   keyset on sw.id)
    ├─ seedLabels      → scanBatches(fetch=Queries.ListWarmSeedLabelBatch      keyset on wl.id)
    └─ seedIndexState  → scanBatches(fetch=Queries.ListWarmSeedIndexStateBatch keyset on (net,wallet,source) PK)
        each batch: SELECT + enqueue iteration under one 30s child ctx
        visit → enqueueGated:
          net gate (NULL/inactive/non-eip155 → skipped_inactive_net)
          → IsEVMAddress (→ skipped_non_evm)   [S3: coverage gate first → skipped_fresh]
          → canonical pre-check NormalizeEVM(addr)==addr (→ errors++, masked warn log)
          → Queue.Enqueue(bctx, netID, addr, priority, reason, nil)
              → Queries.EnqueueWarmItem — ON CONFLICT upsert, ACTIVE-row dedup (ONLY dedup)
          post-check Enqueue failure / ctx|bctx death → abort run (non-zero exit)
```

## Test counts & verification (real runner output)

- `go test ./internal/warmindex/ -count=1` → **ok 10.9s** — 9 new seed tests + all pre-existing queue/drain tests, zero drift.
- `go test ./... -count=1` → **all packages ok**, incl. `tests` 60.1s integration.
- `go build ./...`, `go vet ./...` → exit 0; `gofmt -l .` → empty.
- `git diff --check` → clean; `internal/db` diff scoped to `warm_seed.sql.go` only.
- Config: `WARM_SEED_BATCH_SIZE` malformed → `config.Load()` error (envInt); `<1` → `Seeder.Run` error (tested: 0, −5).

New seed tests (all against `check_wallet_warmindex_test` via `testutil.RequireTestPoolDSN`):
`TestSeedPriorities_BelowSignalFloor` (20/10/5 < 50 invariant), `TestSeeder_SeedSanctions` (p20 + revoked excluded + NULL/tron-net → skipped_inactive_net + non-EVM → skipped_non_evm + uppercase → errors), `TestSeeder_SeedLabels` (p10 + EXCHANGE/GAMBLING/revoked SQL-excluded), `TestSeeder_SeedIndexState` (8-status coverage matrix + literal OR truncation), `TestSeeder_IdempotentReplay`, `TestSeeder_CompositeKeysetPagination` (batchSize=1, multi-source shared (net,wallet)), `TestSeeder_DisabledQueue`, `TestSeeder_BatchSizeValidation`, `TestSeeder_CtxCancelAborts`.

## CLI smoke (real binary vs `check_wallet_warmindex_test`, explicit env exports)

Fixtures: 1 eligible OFAC (eth), 1 revoked OFAC, 1 tron-net OFAC (Base58), 1 SCAM label (eth), 1 EXCHANGE label (bsc), 1 `partial` index_state (bsc).

- Run 1: `WARM_INDEX_ENABLED=1` → exit 0; counters `ofac_list scanned=2 enqueued=1 skipped_inactive_net=1`, `risky_label scanned=1 enqueued=1`, `refresh_stale scanned=1 enqueued=1`; `warm_queue` = 3 rows at p20/p10/p5 with `seed:*` reasons.
- Run 2 (idempotency): exit 0; `enqueued=3` attempts; still **3** active rows (`status IN ('pending','claimed')`).
- `WARM_INDEX_ENABLED=0` → `"warm index disabled (WARM_INDEX_ENABLED); exiting"`, exit 0, zero queue writes.
- `-mode bogus` → `worker: unknown -mode "bogus"`, exit 1 (no dispatch regression).

## Known limitations

- Per-row `Enqueue` is one round-trip each — accepted for one-shot MVP scale (plan risk note).
- `enqueued` counts attempts, not new rows — documented in-code and tested (`TestSeeder_IdempotentReplay`).
- Total run time intentionally unbounded (per-batch 30s bound only); ops wraps cron/systemd timeouts.

Status: DONE
