---
plan: stablecoin-freeze-intel
title: Stablecoin freeze intelligence — on-chain issuer blacklist events → FROZEN labels
status: completed
created: 2026-11-06
---

# Stablecoin Freeze Intelligence

## Overview

Tether (USDT) and Circle (USDC) freeze addresses **on-chain** — the token
contracts emit blacklist events and expose a public `isBlackListed` /
`isBlacklisted` view. Verified live during research:

- USDT (ETH `0xdAC17F958D2ee523a2206206994597C13D831ec7`): `AddedBlackList`,
  `RemovedBlackList`, `DestroyedBlackFunds` — 28 freeze events / ~11 days
- USDC (ETH `0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48`): `Blacklisted`,
  `UnBlacklisted` — 33 freeze events / ~11 days
- The $2B "Binance Hot Wallet Exploiter 2" wallet was frozen by **both**
  issuers 3 minutes apart, ~41 min after the drain began — faster than any
  structured feed we ingest (OFAC: weeks; prettydeath: days+)

This plan adds a `stablecoin_freeze` **chain-scan source** that watches those
events and publishes `FROZEN` labels into `wallet_labels` with full
provenance. It reuses the existing ingestion machinery (run rows, single-
flight locks, staging, atomic publish tx, admin trigger) instead of
introducing a parallel path.

### Core design decision — incremental, not snapshot

The labels pipeline (`pipeline_labels.go`) is **full-replace**: publish
revokes every live label absent from the staged snapshot
(`PublishRevokeAbsentWalletLabels`). A freeze feed cannot use that model —
an address absent from this run's scan window is *not* unfrozen; unfreeze
only comes from explicit `RemovedBlackList` / `UnBlacklisted` events
(`DestroyedBlackFunds` burns the balance but leaves the address
blacklisted — it must never revoke). Therefore:

- Scan state lives in a new `freeze_scan_state` cursor table
  (per-contract `last_scanned_block`), advanced inside the publish tx.
- Publish is **delta**: upsert FROZEN labels for freeze events; revoke only
  on explicit unfreeze events. No absence-based revocation. Ever.
- The fetcher *is* a `provider.SnapshotSource`: `Download()` scans
  `eth_getLogs` and materializes the observed events into a temp JSONL
  file — so `Service.execute()` needs **zero changes**.

### Scope

| In scope | Out of scope |
|---|---|
| USDT + USDC on **ethereum** | BSC/Tron (BSC pegs expose no blacklist functions — verified live) |
| Freeze + unfreeze + destroy events | `isBlackListed` on-demand screening lookup (follow-up) |
| Backfill from contract deploy block | Reason attribution (events carry none — `reason` stays NOT_AVAILABLE) |
| `worker -mode ingest -source stablecoin_freeze` | Cron scheduler (existing trigger path reused) |

## Phase Roadmap

| # | Phase | Status | Dependencies |
|---|---|---|---|
| 01 | Schema & queries (`freeze_scan_state`, `stg_freeze_events`, source row) | pending | — |
| 02 | `internal/provider/stablefreeze` — RPC client + scan→file fetcher | pending | 01 |
| 03 | `internal/ingestion/pipeline_freeze.go` — stage/validate/delta-publish | pending | 01, 02 |
| 04 | Worker wiring + config + live evidence | pending | 02, 03 |

## Risk Matrix

| Risk | Mitigation |
|---|---|
| Absence-as-revoke regression (unfreezing by omission) | Delta publish only; no `PublishRevokeAbsent*`; explicit test |
| Reorg rewriting history | Scan up to `head − FREEZE_SCAN_CONFIRMATIONS` (12) |
| getLogs range limits upstream | Bounded window per call (50k) + per-run block cap (2M) |
| Re-scan overlap duplicates | Idempotent fingerprint + staging PK (run, contract, tx, log_index) |
| Stuck cursor on crash | Cursor only advances inside committed publish tx |
| Wrong-chain label | `network_id` pinned to ethereum; contract address checked per event; PK `(network_id, contract_address)` |
| RPC failure mid-scan | Bounded retries via resilience client; run marked FAILED, cursor untouched |
| Invented reasons | `reason_summary`/entity stay null; evidence carries only contract+tx+block |
| Backfill cost | ~21M blocks ÷ 2M/run ≈ 11 runs, resumable; `-until-caught-up` worker flag; steady state = 1–2 calls |
| Stale cache on publish | Cache-version query spans all label-producing sources |
| Degenerate window | Header `empty:true` marker; cursor never regresses (`GREATEST`) |
| Destroy-as-revoke | `destroy` never revokes — append-only evidence |

## Resolved after Critic round 1

1. `verification_status='verified'` — endorsed: the issuer's own contract
   event is primary evidence; `reason` stays `NOT_AVAILABLE`.
2. `DestroyedBlackFunds` does **NOT** revoke — `destroyBlackFunds` keeps
   `isBlackListed` set; label stays active + evidence append (terminal
   marker). Only `RemovedBlackList`/`UnBlacklisted` revoke.
3. Fingerprint: source-namespaced twin `stablefreeze.Fingerprint` pinning
   `"stablecoin_freeze|"` — `walletattribution.Fingerprint` hardcodes its
   own prefix; reusing it would collide namespaces and re-open
   absence-as-revoke via the wattr publish path.
4. `source_type='chain_scan'` — endorsed (honest delta signal).
5. Read-path invalidation (BLOCKER): label cache keys embed a version
   currently hard-scoped to `wallet_attribution`. Fix: the cache-version
   query returns the latest publish across **all** label-producing
   sources, so every freeze publish bumps cached reads on
   `/wallets/labels` and `/screenings`. `lastSynced` per-source semantics
   documented separately in phase-03.
6. Consequence noted: FROZEN already carries scoring weight 60 — freeze
   labels immediately affect `risk_score`. Deliberate: a wallet frozen by
   Tether/Circle SHOULD score; documented, not accidental.
