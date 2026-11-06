---
phase: 04
title: Worker wiring, config, live evidence
status: completed
priority: medium
dependencies: [02, 03]
---

## Overview

Wire `stablecoin_freeze` into `cmd/worker -mode ingest`: resolve the RPC
endpoint via `providerconfig.ResolveAll` (same as warm lanes — `ankr`
ethereum, killswitch-aware), register the fetcher, document env knobs, and
produce live evidence: real run → labels in `wallet_labels`.

## Requirements

- `cmd/worker/main.go`: build `stablefreeze.Scanner` when `file == ""`
  using resolved provider config for `FREEZE_SCAN_RPC_PROVIDER`
  (default `ankr`, ethereum lane); register in `fetchers` map +
  `svc.pipelines["stablecoin_freeze"]` registered in `NewService`.
- **`cmd/api/main.go` MUST also wire the scanner into its fetchers map** —
  `HasPipeline` is shared so the admin `POST /admin/ingestion-runs`
  accepts the source; an unwired API fetcher produces 202 → FAILED run
  (`pcRes` is already resolved at cmd/api/main.go:117+). Add a handler
  test asserting the source is triggerable end-to-end or explicitly
  rejected.
- `-until-caught-up` flag on `worker -mode ingest`: loops `RunSync`
  until every contract cursor reaches `head−conf` (backfill convergence
  without ~11 manual invocations; each pass is an honest run row).
- Config additions in `internal/config`: `FREEZE_SCAN_BLOCK_RANGE`,
  `FREEZE_SCAN_MAX_BLOCKS_PER_RUN`, `FREEZE_SCAN_CONFIRMATIONS`,
  `FREEZE_SCAN_RPC_PROVIDER`; `.env.example` documented.
- RPC provider inactive/missing key → run FAILS with explicit error
  (fail-closed, never silently produce an empty file).
- `NewService` boot assertion: every pipeline `StagingNames()` entry
  resolves in `stagingPurge` AND `stagingDelete` — turns the nil-func
  panic into a boot-fatal config error.
- Live evidence: `worker -mode ingest -source stablecoin_freeze
  -until-caught-up` against dev DB → `wallet_labels` rows with category
  FROZEN, source stablecoin_freeze, real tx-hash evidence (e.g. the
  exploiter wallet — verifiable independently on etherscan).
- Admin ingestion trigger (`HasPipeline`) must accept
  `stablecoin_freeze` — verify in handler test or live call AND the run
  must reach COMPLETED (acceptance ≠ completion).

## Related Code Files

**Modify**
- `cmd/worker/main.go` (fetcher wiring + `-until-caught-up` flag)
- `cmd/api/main.go` (scanner into admin-trigger fetchers map)
- `internal/config/config.go` (+ defaults) & tests
- `internal/ingestion/service.go` (NewService staging-map boot assertion)
- `.env.example`

## Success Criteria

- [ ] `worker -mode ingest -source stablecoin_freeze` → run COMPLETED
      with real upstream calls against dev DB
- [ ] `wallet_labels` shows FROZEN rows incl. `0x5227f603…` (Binance
      exploiter, USDT+USDC evidence) with `verified` status
- [ ] Disabled-provider posture: ankr inactive → FAILED run, no labels
- [ ] `gofmt`, build, vet, full test suite green

## Risk Assessment

- Ankr free-tier range limits → confirmed via real run; adapt
  `FREEZE_SCAN_BLOCK_RANGE` default if needed.
- Backfill runtime: first run caps at `MAX_BLOCKS_PER_RUN`; document
  expected ≈11-run convergence in guidebook.
