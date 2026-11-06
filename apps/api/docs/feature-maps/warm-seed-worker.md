# Feature Map — Warm Seed Worker (`-mode warmseed`)

> Pipeline: `codex-vibe --auto` · Plan: `plans/20261106-2345-warm-seed-worker/`
> Shipped: 2026-11-07 · Status: completed

## What It Does

A one-shot, cron-scheduled worker mode that proactively seeds
`warm_queue` from three DB-resident signal sources, so the risk graph
around known-bad actors is warmed **before** any user query. The
pre-existing `warmdrain` consumer then spends leftover provider quota
deep-indexing those wallets. Previously the queue was only fed
reactively by interactive BFS (hop-1 signal counterparties at p100/p50).

## Source Map

| Path | Role |
|------|------|
| `apps/api/queries/warm_seed.sql` | 3 keyset-paginated seed SELECTs |
| `apps/api/internal/db/warm_seed.sql.go` | sqlc v1.30.0 generated (`ListWarmSeed{Sanctions,Label,IndexState}Batch`) |
| `apps/api/internal/warmindex/seed.go` | `Seeder`/`NewSeeder`/`Run`, `scanBatches` loop, `enqueueGated` gate chain, seed priorities |
| `apps/api/internal/warmindex/seed_test.go` | 10 integration tests (`check_wallet_warmindex_test`) |
| `apps/api/internal/config/config.go` | `WarmSeedBatchSize` knob (`WARM_SEED_BATCH_SIZE`, default 500) |
| `apps/api/cmd/worker/main.go` | `case "warmseed"` dispatch + `runWarmSeed()` |

Call graph:
`runWarmSeed` → flag gate (`WARM_INDEX_ENABLED`) → `database.Migrate` →
`Connect` → `NewQueue` → `NewSeeder` → `Run` →
`seed{Sanctions,Labels,IndexState}` → `scanBatches` (keyset, 30 s child
ctx per batch covering SELECT + enqueues) → `enqueueGated` (net →
`IsEVMAddress` → canonical pre-check) → `Queue.Enqueue` →
`EnqueueWarmItem` upsert (the ONLY dedup mechanism).

## Operational Guidebook

### 1. Enable + run

```bash
cd apps/api
DATABASE_URL='postgres://…' WARM_INDEX_ENABLED=1 go run ./cmd/worker -mode warmseed
# optional: WARM_SEED_BATCH_SIZE=500 (default), LOG_LEVEL=info
```

Cron (one-shot by design — no daemon, no loop; total runtime
intentionally unbounded, each batch bounded to 30 s):

```cron
17 * * * * cd /srv/checkwallet/apps/api && WARM_INDEX_ENABLED=1 ./worker -mode warmseed >> /var/log/warmseed.log 2>&1
```

### 2. Expected output (verbatim capture, smoke DB)

```json
{"level":"WARN","msg":"warm seed row rejected (non-canonical address)","source":"ofac_list","addr":"0x0000…0B04"}
{"msg":"warm seed source complete","source":"ofac_list","scanned":5,"enqueued":1,"skipped_non_evm":1,"skipped_inactive_net":2,"skipped_fresh":0,"errors":1}
{"msg":"warm seed source complete","source":"risky_label","scanned":1,"enqueued":1,"skipped_non_evm":0,"skipped_inactive_net":0,"skipped_fresh":0,"errors":0}
{"msg":"warm seed source complete","source":"refresh_stale","scanned":2,"enqueued":1,"skipped_non_evm":0,"skipped_inactive_net":0,"skipped_fresh":1,"errors":0}
{"msg":"warm seed complete","scanned":8,"enqueued":3,"skipped_non_evm":1,"skipped_inactive_net":2,"skipped_fresh":1,"errors":1}
```

A failing source emits `warm seed source aborted` (counters + error)
and the process exits non-zero.

### 3. Verify the queue (executable SQL)

```sql
SELECT wq.priority, wq.reason, wq.status, wq.address, bn.code AS net
FROM warm_queue wq JOIN blockchain_networks bn ON bn.id = wq.network_id
ORDER BY wq.priority DESC;
```
```
 priority |       reason       | status  |                  address                   |   net
----------+--------------------+---------+--------------------------------------------+----------
       20 | seed:ofac_list     | pending | 0x0000000000000000000000000000000000000b01 | ethereum
       10 | seed:risky_label   | pending | 0x0000000000000000000000000000000000000c01 | ethereum
        5 | seed:refresh_stale | pending | 0x0000000000000000000000000000000000000c03 | ethereum
```

```sql
-- Idempotency check (run twice, compare)
SELECT count(*) FROM warm_queue WHERE status IN ('pending','claimed');
```

### 4. Disabled / failure behavior (verified)

| Condition | Behavior |
|-----------|----------|
| `WARM_INDEX_ENABLED=0` | `warm index disabled …; exiting`, exit 0, zero writes |
| `-mode bogus` | `worker: unknown -mode "bogus"`, exit 1 |
| DB failure / ctx death | run aborts, exit 1 |
| Bad row (non-canonical) | warn + `errors++`, scan continues |

## Seed Sources & Priorities

| Source | Eligibility | Priority | Reason |
|--------|-------------|----------|--------|
| `sanctions_wallets` | `revoked_at IS NULL`, active EVM net | **20** | `seed:ofac_list` |
| `wallet_labels` (via `wallets`+`risk_categories`) | `revoked_at IS NULL`, code ∈ SCAM/PHISHING/HACK/RANSOMWARE/MIXER | **10** | `seed:risky_label` |
| `wallet_index_state` (via `wallets`) | `status ∈ {partial,failed}` OR `history_truncated` | **5** | `seed:refresh_stale` |

**Hard invariant**: all seed priorities < `PriorityRiskyLabel` (50) —
interactive investigations always win `priority DESC, id` claim order.
Guarded by `TestSeedPriorities_BelowSignalFloor`.

## Per-Row Gate Chain (`enqueueGated`)

1. **Net gate** — JOIN NULL / `is_active=FALSE` / non-`eip155`
   namespace → `skipped_inactive_net` (NULL `network_id` is an
   *intentional* gap per the anti-cross-listing invariant).
2. **Shape gate** — `!wallet.IsEVMAddress` → `skipped_non_evm`
   (TRON Base58 etc. never reaches `NormalizeEVM`).
3. **Canonical pre-check** — `NormalizeEVM(addr) != addr` → `errors++`,
   continue (the only row-level failure domain).
4. **Enqueue** — post-check failure is infra by construction → abort.
   Coverage gate (source 3 only) runs first: non-stale → `skipped_fresh`.

## Counters Contract

Per-source `warm seed source complete` + totals `warm seed complete`,
always all six keys: `scanned`, `enqueued`, `skipped_non_evm`,
`skipped_inactive_net`, `skipped_fresh`, `errors`.
`enqueued` = accepted upsert *attempts* (deduped re-runs still count;
disabled-queue no-ops still count) — second-run `enqueued=N` with zero
new rows is correct.

## Config Knobs

| Env | Default | Effect |
|-----|---------|--------|
| `WARM_INDEX_ENABLED` | `false` | Master flag — off → log + exit 0 |
| `WARM_SEED_BATCH_SIZE` | `500` | Keyset batch size; `<1` → `Run` error → exit 1 |

## Invariants Preserved (verified)

- Non-EVM input never reaches `NormalizeEVM` (double gate).
- TRON/Solana stay `is_active=FALSE`; no trongrid import; no cross-listing.
- No migrations, no edits to `drain.go`/`queue.go`/`warm_index.sql`/
  `tracing/warm.go`/signal priorities, no endpoints, no deps, no commits.
- Re-runnable: crash mid-scan replays cleanly (upsert dedup only).

## Test Matrix (10 tests, all green)

`TestSeedPriorities_BelowSignalFloor`, `TestSeeder_SeedSanctions`,
`TestSeeder_SeedLabels`, `TestSeeder_SeedIndexState`,
`TestSeeder_IdempotentReplay`, `TestSeeder_CompositeKeysetPagination`,
`TestSeeder_DisabledQueue`, `TestSeeder_BatchSizeValidation`,
`TestSeeder_EnqueueInfraAbort`, `TestSeeder_CtxCancelAborts` —
plus saturation `go test -count=10` (10/10) and `-race` clean.
