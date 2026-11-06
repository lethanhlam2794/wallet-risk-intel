---
phase: 02
title: internal/provider/stablefreeze — RPC client + scan→file SnapshotSource
status: pending
priority: high
dependencies: [01]
---

## Overview

New provider package implementing `provider.SnapshotSource`:
`Download(ctx)` reads the per-contract cursor, scans `eth_getLogs` for the
freeze/unfreeze/destroy topics over `(cursor, min(head−conf, cursor+window)]`,
and materializes observed events plus per-contract window metadata into a
temp JSONL file — the "snapshot artifact" the ingestion service already
knows how to checksum, stage, and publish.

## Requirements

- Contract registry (constants): USDT/ETH
  `0xdAC17F958D2ee523a2206206994597C13D831ec7` → topics
  `AddedBlackList`/`RemovedBlackList`/`DestroyedBlackFunds`; USDC/ETH
  `0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48` → topics
  `Blacklisted`/`UnBlacklisted`. (BSC pegs expose none — documented
  out-of-scope.)
- Thin JSON-RPC client reusing `provider/resilience` client conventions
  (timeout, bounded retry, breaker) + SSRF host allowlist; compatible with
  ankr node-API base `rpc.ankr.com/eth` (and any same-shape endpoint —
  nodereal BSC for future expansion).
- Topic0 keccak constants computed/pinned per event signature; address
  extracted from `topics[1]` when indexed else `data` tail (USDT events
  verified non-indexed during research — handle BOTH).
- Output file: first line = JSON header `{version, contracts:[{address,
  fromBlock,toBlock,head,empty}]}`, then one JSON event per line. Header
  makes every run's checksum unique → publish always advances cursor (no
  checksum no-op trap). **Degenerate window** (`head−conf ≤ cursor`):
  emit header-only file with `empty:true` + `toBlock = fromBlock−1` —
  Import/Validate accept it and Publish advances nothing; run COMPLETEDs
  with 0 events instead of failing forever.
- `block_ts` is NOT in `eth_getLogs` output — after scanning, batch
  `eth_getBlockByNumber` over the distinct `block_number` set (sparse:
  tens of blocks per window, bounded by staged-event count; distinct-block
  cache so each block is fetched once).
- `CursorStore` interface (`GetFreezeScanStates`) implemented over
  `db.Queries`; scanner fails closed when cursor rows are missing.
- Fetcher owns checksums: populate `File.Checksum` (sha256 of bytes) +
  `provider.CombinedChecksum(files)`; leave `FinalURL` empty — `execute()`
  logs `f.Checksum[:12]` whenever `FinalURL != ""`, so an empty-checksum+
  FinalURL artifact panics there.
- Ankr base derivation: resolved `BaseURL` is the multichain Advanced-API
  base (`rpc.ankr.com/multichain`); node API = `strings.TrimSuffix(base,
  "/multichain") + "/" + <chain>` — the exact derivation ankr's
  `HeadBlock` already performs (`adapter.go:216`); extract or mirror it.
- Config knobs (env): `FREEZE_SCAN_BLOCK_RANGE` (default 50000),
  `FREEZE_SCAN_MAX_BLOCKS_PER_RUN` (default 2000000),
  `FREEZE_SCAN_CONFIRMATIONS` (default 12), `FREEZE_SCAN_RPC_PROVIDER`
  (default `ankr` — resolved via providerconfig like warm lanes).
- Malformed log entries → skip + count `rejected`; RPC error mid-scan →
  fail the Download (run FAILED, cursor untouched) — never a partial file.
- No secrets/full-address logging beyond existing masking conventions.

## Related Code Files

**Create**
- `internal/provider/stablefreeze/contracts.go` — registry + topics
- `internal/provider/stablefreeze/rpc.go` — JSON-RPC client
- `internal/provider/stablefreeze/scanner.go` — cursor→window→file
- `internal/provider/stablefreeze/scanner_test.go`, `rpc_test.go`

**Modify**
- none (new package; wired in phase 04)

## Implementation Steps (TDD)

1. `rpc_test.go` — httptest fake JSON-RPC: blockNumber, getLogs paging,
   error mapping to `provider.ErrKind`.
2. `scanner_test.go` — fake RPC feeding crafted logs; assert file header
   + event lines, both topic-indexed and data-carried addresses, window
   capping, empty window (header-only file still produced).
3. Edge tests: missing cursor → error; confirmations floor; malformed log
   skipped+counted; ctx cancel mid-scan.

## Success Criteria

- [ ] `go test ./internal/provider/stablefreeze/` green
- [ ] SSRF allowlist unit-tested (foreign host rejected)
- [ ] File format versioned + documented in header

## Risk Assessment

- Upstream caps getLogs range → adaptive range shrink (halve on
  range-error, floor at 1k) or fixed config; decide by testing against
  real ankr early.
- Deploy-block genesis off by a few → harmless overlap (idempotent).
