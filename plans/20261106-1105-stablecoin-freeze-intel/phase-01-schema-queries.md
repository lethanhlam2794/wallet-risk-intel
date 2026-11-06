---
phase: 01
title: Schema & queries — freeze_scan_state, stg_freeze_events, source row
status: pending
priority: high
dependencies: []
---

## Overview

Migration `000024_stablecoin_freeze` adds the incremental-scan schema: a
per-contract cursor table, a run-scoped event staging table, and the
`stablecoin_freeze` data_sources row. sqlc queries for cursor read/advance,
staging insert/count/purge/delete, and the publish-time label upsert +
event-scoped revoke.

## Requirements

- `freeze_scan_state(network_id, contract_address, genesis_block,
  last_scanned_block, last_head_block, updated_at)` — **PK
  (network_id, contract_address)** (EVM deployers collide across chains);
  seeded with USDT+USDC ethereum rows. Deploy blocks verified live via
  `eth_getCode` binary search: **USDT=4634748, USDC=6082465**.
- `stg_freeze_events(run_id, contract_address, event_type, wallet_address,
  tx_hash, log_index, block_number, block_ts)` — PK
  (run_id, contract_address, tx_hash, log_index); event_type ∈
  ('freeze','unfreeze','destroy'). `block_ts` is populated by the
  scanner's `eth_getBlockByNumber` batch pass (phase-02) — `eth_getLogs`
  returns no timestamps.
- `AdvanceFreezeScanState` uses `last_scanned_block =
  GREATEST(last_scanned_block, $to)` — never regresses on degenerate
  windows.
- `data_sources` row: `('stablecoin_freeze', 'Stablecoin Freeze
  (USDT/USDC on-chain)', 'chain_scan', <homepage>)`.
- Down migration drops both tables + deletes the source row. Precedent
  (000005 down): delete `ingestion_runs` + `data_source_snapshots` for the
  source first, then `DELETE FROM wallet_labels WHERE source_id=…` (FK),
  then the `data_sources` row — `wallet_labels` itself is shared and must
  NOT be dropped.
- sqlc queries: `GetFreezeScanStates`, `AdvanceFreezeScanState`,
  `StgInsertFreezeEvent`, `StgFreezeEventCounts`, purge/delete entries wired
  into `stagingPurge`/`stagingDelete` maps in `internal/ingestion`.
- Publish queries: `PublishFreezeUpsertLabels` (INSERT … SELECT from
  staging freeze rows → wallets ensure + wallet_labels upsert by
  fingerprint, DO UPDATE on evidence/observed_at), `PublishFreezeRevoke`
  (set revoked_at + merge unfreeze evidence on matching active labels for
  unfreeze events only — destroy events never revoke).

## Related Code Files

**Create**
- `migrations/000024_stablecoin_freeze.up.sql` / `.down.sql`
- `queries/stablecoin_freeze.sql`

**Modify**
- `internal/ingestion/pipeline.go` (stagingPurge/stagingDelete entries)

## Implementation Steps (TDD)

1. Write migration + sqlc queries; `sqlc generate`.
2. Migration up/down on test DB; assert tables/rows exist.
3. Query-level tests on `_test` DB: cursor advance, staging idempotency.

## Success Criteria

- [ ] `migrate` up+down clean on dedicated `_test` DB
- [ ] sqlc generates; `go build ./...` green
- [ ] Cursor advance + staging insert tests pass
- [ ] `gofmt -l .` empty

## Risk Assessment

- Seed genesis blocks wrong → gaps; verified constants USDT=4634748,
  USDC=6082465 via `eth_getCode` binary search — assert presence diff at
  genesis−1/genesis in a test.
- Down migration FK conflicts → the ordered DELETE chain in Requirements
  (labels → runs → snapshots → tables → source) satisfies FKs; never drop
  shared `wallet_labels`.
