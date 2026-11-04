---
phase: 02
title: Blockscout transaction-history adapter
status: pending
priority: P0
dependencies: [phase-01]
---

# Phase 02: Blockscout adapter

## Overview
Blockscout v2 REST, ETHEREUM ONLY, eth.blockscout.com, keyless. Endpoints: /api/v2/addresses/{addr}/transactions and /token-transfers, cursor pagination. No block-range params — paginate and filter client-side to the window, stop at watermark. Enabled()=true for ethereum without key; gate via enabled-chains list like chainscan, not key presence.

CRITICAL COVERAGE RULE (critic R1+R4): the service derives unfinished-stream coverage as `streamMaxBlock − 1`, assuming ASCENDING contiguous emission from `fetchFrom`. Blockscout returns NEWEST-FIRST with no block-range params, and `TransferLookupOpts` carries no budget visibility — so a "contiguous prefix" does not exist: emitting scanned rows `[stopBlock..fetchTo]` makes the service book `[fetchFrom..fetchTo−1]` and permanently over-claims the unfetched hole `[fetchFrom..stopBlock−1]`. The ONLY honest emissions are (a) ALL buffered rows ascending once the window is fully covered, or (b) ZERO rows with an unfinished stream. Pinned drain/buffer model:

- Each `LookupTransfers` call continues the descending scan from the
  cursor inside `NextPageToken` and drains at most
  `BLOCKSCOUT_MAX_SCAN_PAGES` upstream pages per call (adapter knob,
  default 10) — this bounds per-call spend without needing request-pool
  visibility.
- Buffered scan state is keyed DETERMINISTICALLY by
  `(net, wallet, fetchFrom, kinds)` — NOT by a random token id — because
  the service creates `token := ""` per request (service.go:301); a new
  request must re-derive the same buffer to deliver cross-refresh
  progress. `NextPageToken` carries the scan cursors, not a lookup key.
- Scan state is PER STREAM (`/transactions` and `/token-transfers`
  descend independently): {rows, upstream resume cursor (keyset/seek
  params — Blockscout cursors are block/index-based, `scannedFloor`-seek
  is acceptable), `scannedFloor` (lowest block contiguously scanned
  from the top), `headSeen` (highest fetchTo seen)}. On `fetchTo` drift
  upward: first drain the head segment `(headSeen..fetchTo]` within
  the same per-call page bound, merge+dedupe by upstream transfer
  identity, then continue descending `scannedFloor` toward `fetchFrom`.
- Buffer is process-local in-memory, capped (~20000 rows per scan);
  eviction: per-scan TTL (e.g. 30m idle) + global LRU bound across
  wallets (orphaned partial scans of abandoned wallets must not
  accumulate unboundedly). Overflow → `internal` error + drop buffer
  (a local resource cap is NOT upstream `degraded`; `internal` is a
  never-failover kind so a deterministic cap cannot burn the fallback
  chain — a wallet over the cap loops honestly FAILED, bounded by the
  refresh cooldown, tunable via the cap knob).
- "Fully covered" = `scannedFloor` ≤ `fetchFrom` AND head segment joined
  (or upstream has no earlier page). On coverage: emit ALL buffered
  rows inside `[fetchFrom, fetchTo]` ascending, `Finished` per stream,
  `NextPageToken=""`.
- On drain-bound before coverage: emit zero rows, unfinished stream,
  progress token carrying the cursors.
- Buffer evicted/restart: scan restarts from `fetchTo` — honest
  re-spend, never resume mid-air.
- `UpstreamCalls` = real drained page count per call — internal drain
  must not fake coverage and must not hide spend.

## Requirements
- Implement `provider.TxProvider` for `internal/provider/blockscout/`.
- Verify CURRENT upstream contract against official docs first; docs win
  over assumptions — surface conflicts in the report.
- Enabled() = false without key (keyless providers: gating by
  enabled-chains list instead) — zero upstream traffic when disabled.

## Architecture
Follow internal/provider/nodereal + internal/provider/chainscan:
- adapter.go: constructor `New(baseURL, apiKey, opts...)`, hardcoded
  SSRF `defaultAllowedHosts`, https-only (+ WithInsecureHTTP/
  WithAllowedHosts test opts), resilience.NewClient (bounded rate,
  concurrency 2, MaxRetries 2, jittered backoff, breaker 5/60s,
  MaxBodyBytes 8MiB).
- mapper.go: upstream row -> provider.Transfer; quirks stay here.
- Streams partition by what the upstream API actually paginates
  independently (from|to, kind-split, or unified); ascending block order
  per stream; never fake per-direction on a unified-list upstream;
  opaque NextPageToken; UpstreamCalls = real outbound calls.
- Config: `<NAME>_API_KEY`, `<NAME>_BASE_URL`, enabled-chains;
  `.env.example` commented placeholder.
- Migration: new `data_sources` row (up INSERT, down DELETE).
- cmd/api/main.go: construct + register under `TX_PROVIDER_*` env
  selection (default = current adapter unchanged) + Budget entry.

## Related Code Files
- Create: internal/provider/blockscout/adapter.go, mapper.go, adapter_test.go
- Create: migrations/0000NN_blockscout.up.sql / .down.sql
- Modify: internal/config/config.go, cmd/api/main.go, .env.example

## Implementation Steps (TDD)
1. Write fixture tests where the fake server parses the REAL wire shape
   the upstream transport requires (JSON-RPC: positional params
   array; REST: method+path+query+auth-header) — Red.
2. Implement adapter + mapper — Green.
3. Config + migration + wiring.
4. Failure tests: malformed, 429->rate_limited, timeout, disabled,
   pagination continuation.
5. Gates: gofmt -l empty, go vet clean, go build clean, go test green.
6. Live-verify gate: REQUIRED (keyless — always verify).

## Success Criteria
- [ ] TxProvider implemented; Enabled() gating correct
- [ ] Streams report Finished per real upstream partition; ascending block order
- [ ] UpstreamCalls accurate; budget charged correctly
- [ ] data_sources row via migration (up+down apply)
- [ ] Unit tests green with real-wire-shape mock
- [ ] `Name()` == `data_sources.code` pinned by wiring test or boot check (swallowed `sourceID()` error → reset never fires)
- [ ] `Budget[Name()] > 0` enforced (missing/typo'd `TXINDEX_BUDGET_<NAME>` env → pool 0 = unlimited)
- [ ] if live-verify BLOCKED: adapter absent from ALL default `TX_PROVIDER_*` selectors
- [ ] Gates pass; one conventional commit `feat(provider): add blockscout transaction-history adapter`
- [ ] Live-verify evidence = ALL of: upstream HeadBlock + upstream LookupTransfers on a real address + booted `GET /api/v1/wallets/<net>/<addr>/transactions?refresh=1` showing `provider_status:"ok"` (curl a lone HeadBlock does NOT count) — OR `live-verify: BLOCKED (no key)` reported

## Risk Assessment
- Contract drift: docs-first verification + real-shape mock.
- Native-transfer gap: probe confirms coverage or NOT-VIABLE.
- Rate limits: conservative client rate like nodereal.

## Known limitation (critic R3+R4)

Newest-first upstream + no block-range params + bounded per-call drain ⇒
a FRESH wallet whose history exceeds what repeated drains can cover
emits nothing until coverage completes: watermark stays put, refreshes
advance the scan `BLOCKSCOUT_MAX_SCAN_PAGES` pages at a time via the
progress token, quota burns honestly. If history depth outruns the
practical page budget → permanent honest PARTIAL. Incremental indexing
(watermark near head) works fine. This bound is accepted and stated so
phase-12 failover expectations stay calibrated.
