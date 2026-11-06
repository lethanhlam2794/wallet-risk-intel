# Cook Report — stablecoin_freeze (all 4 phases)

**Date**: 2026-11-06 · **Plan**: `plans/20261106-1105-stablecoin-freeze-intel/`
**Scope**: Ethereum-only USDT/USDC issuer blacklist event scanning →
`wallet_labels` FROZEN labels with delta semantics (absence ≠ revoke,
destroy ≠ revoke). Delivered end-to-end: schema → chain scanner → delta
ingestion pipeline → worker/API wiring → live Ankr-backed evidence run.

---

## Phase 1 — schema & queries

**Files**
- `migrations/000024_stablecoin_freeze.up.sql` / `.down.sql` —
  `freeze_scan_state` PK `(network_id, contract_address)` seeded
  USDT=4634748 / USDC=6082465 on ethereum; `stg_freeze_events` with
  `(run_id, contract_address, tx_hash, log_index)` PK; `stablecoin_freeze`
  row in `data_sources`; `cursor_value` column on `ingestion_runs`.
- `queries/stablecoin_freeze.sql` + generated `internal/db/stablecoin_freeze.sql.go` —
  cursor read/`GREATEST` update, staging insert/counts/max-block gate/
  cleanup, delta publish set (ensure wallets → freeze|destroy upsert →
  destroy append → chronological revoke → orphan unfreeze → cursor →
  snapshot), `GetWalletLabelByFingerprint`, `UpdateRunCursorValue`.
- `internal/ingestion/pipeline.go` — `freeze_events` in `stagingPurge` and
  `stagingDelete`.
- `internal/testutil/testdb.go` — `freeze_scan_state` added to
  `staticTables` (migration-seeded; `TruncateAll` must not wipe cursors).
- `tests/freeze_scan_state_test.go` — schema presence, seed rows, cursor
  monotonicity, staging idempotency.

## Phase 2 — `internal/provider/stablefreeze`

`contracts.go` (registry + keccak topic0s), `rpc.go` (JSON-RPC client:
`eth_blockNumber`, `eth_getLogs` **positional-array params**, batched
`eth_getBlockByNumber`, resilience client w/ SSRF allowlist, rate limit,
bounded retry, `FinalURL` left empty per execute() contract),
`fingerprint.go` (`stablecoin_freeze|network|address|category|label`
namespace twin — never reuses walletattribution's), `scanner.go`
(`provider.SnapshotSource`: fail-closed cursors, `head−confirmations`
floor, `BlockRange` paging, `MaxBlocksPerRun` cap, both address
extractions, block-ts resolution, degenerate-window header-only artifact,
checksummed JSONL + `provider.CombinedChecksum`), `build.go`
(providerconfig → scanner; Ankr endpoint derivation
`TrimSuffix(base,"/multichain")+"/eth/"+key`; explicit `rpc.ankr.com`
allowlist — no init-registry dependency in worker binaries).

Tests: `scanner_test.go` (pinned topics, SSRF, missing cursors, head/RPC
failure, happy path paging+ts, **two-word destroy data regression**,
window capping, degenerate window, malformed logs, ctx cancel),
`build_test.go` (endpoint derivation, fail-closed matrix).

## Phase 3 — delta pipeline

`internal/ingestion/pipeline_freeze.go` — Import/Validate/Publish through
the existing `ingestion.Pipeline` contract; registered in
`service.go` (`NewService`) alongside a boot assertion that every
pipeline `StagingNames()` resolves in both housekeeping maps.
`queries/labels_read.sql` — `LatestLabelSnapshotPublished` broadened to
all label-producing sources (`wallet_attribution`, `stablecoin_freeze`)
so freeze publishes bump `/wallets/labels` + `/screenings` cache
versions.

Tests: `tests/freeze_pipeline_test.go` — full `RunSync` file-mode runs:
freeze labels, unfreeze revocation + evidence merge, destroy retention +
keyed `destroy_events` append, destroy-only upsert, orphan unfreeze,
refreeze revival, cursor advancement, idempotent replay, malformed
staging rejection, snapshot version format, `cursor_value` summary,
admin-trigger acceptance. `internal/testutil/testdb_integration_test.go`
— seeded-source list updated to include `stablecoin_freeze`.

## Phase 4 — wiring & config

`config.go` + `.env.example` — `FREEZE_SCAN_BLOCK_RANGE` (50000),
`FREEZE_SCAN_MAX_BLOCKS_PER_RUN` (2000000), `FREEZE_SCAN_CONFIRMATIONS`
(12), `FREEZE_SCAN_RPC_PROVIDER` (ankr). `cmd/worker/main.go` —
`-until-caught-up` flag loops `RunSync` until all cursors reach
`head−conf`. `cmd/api/main.go` — scanner registered in the fetcher map
(admin trigger = actual runnability). Missing/inactive/disabled provider
config fails closed (fetcher omitted → triggered runs fail, never a
fabricated empty success). No cron scheduler, per scope.

## Live evidence run (dev DB, real Ankr RPC)

`worker -mode ingest -source stablecoin_freeze -until-caught-up`
completed across 22 COMPLETED runs; final state:

| contract | last_scanned | head | head−conf |
|---|---|---|---|
| USDT 0xdac1…ec7 | 26353538 | 26353550 | ✓ |
| USDC 0xa0b8…6eb48 | 26353538 | 26353550 | ✓ |

| label | total | active | revoked | with destroy_events |
|---|---|---|---|---|
| usdt_blacklist | 3198 | 2855 | 343 | 1208 |
| usdc_blacklist | 886 | 689 | 197 | 0 |

Evidence verified against real chain data (e.g. tx `0x348a04…0631`
block 5758962 ts 1528547502; destroy marker tx `0x383adc…eb7f` block
12588398). A wallet blacklisted at block 26353511 between the two
backfill invocations was picked up correctly by the resuming delta run.

## Bugs found by live verification & fixed

1. **USDC contract address typo** (`…eb0c1e3606eb48`, 41 chars) in
   `contracts.go`, migration seed, tests → corrected to
   `…eb0ce3606eb48` everywhere incl. already-migrated DBs.
2. **`eth_getLogs` params shape** — upstream requires positional array
   `[{filter}]`; bare object rejected `-32602 missing field 'filter'`.
3. **`DestroyedBlackFunds` signature** — real event is
   `(address,uint256)` → topic `0x61e6e6…98c6`; my 1-arg topic matched
   zero of the 1305 on-chain events. Fixed + pinned.
4. **Two-word data extraction** — non-indexed extraction read the blob
   tail; for `(address,uint256)` the tail is the *amount*. Now reads the
   low 20 bytes of ABI word 0. Regression test added.

3 FAILED run rows remain in `ingestion_runs` — two pre-fix attempts and
one mid-backfill upstream 429; failure visibility preserved, cursors
never advanced past a failed fetch (atomic publish confirmed live).

## Final gates

```
gofmt -l .   → (empty)
go build ./… → OK
go vet ./…   → OK
go test -count=1 ./… → ALL PASS (incl. stablefreeze, tests/, tracing)
```

## Deviations / notes

- `internal/tracing` tests are flaky under parallel load (deadlock +
  timing assertions; different failure sets per run, all pass in
  isolation, zero overlap with this diff). Final suite run fully green.
- Redis (6380) had to be started for the suite — environmental.
- Migration 000024 was edited after first apply (address fix); the two
  already-migrated local DBs were corrected by hand. No release impact.
- `docs/pipeline-log.md` contains the orchestrator's Stage-1 entry
  (pre-existing convention, left as-is).
- Per spec: no BSC/Tron, no on-demand `isBlackListed` lookup, no
  scheduler — future work if wanted.
