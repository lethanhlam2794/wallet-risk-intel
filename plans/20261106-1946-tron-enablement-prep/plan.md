---
title: "TRON Enablement Prep — provider eval, indexer semantics, address_type generalization, dormant adapter"
status: completed
priority: P1
mode: codex-vibe --auto
created: 2026-11-06
risk_class: R3 (external provider integration + schema-adjacent semantics)
source_prompt: docs/prompts/20261106-1740-codex-tron-enablement-prep.md
---

# TRON Enablement Prep

## Overview

Make TRON **code-complete but still INACTIVE**: verify and pick a free
TxProvider candidate, implement a `provider.TxProvider` adapter that can
never emit traffic until configured AND the network is active, generalize
the hardcoded `'evm'` `address_type` on the `Service.GetOrCreate` wallet
path, and produce the two semantics/decision documents a §15 reviewer
needs. `blockchain_networks` rows stay `is_active=FALSE`; nothing is wired
into any provider selector.

## Context (verified by orchestrator scouting, 2026-11-06)

- Groundwork shipped: `AddressFormat` (`internal/network/address_format.go`),
  `wallet.Normalize` dispatch (`internal/wallet/address.go:51-81`),
  `NormalizeTron` base58check (`internal/wallet/address_tron.go`),
  solana/tron `is_active=FALSE` rows (migration `000026`), D4b `is_active`
  predicates on code-keyed joins.
- `provider.TxProvider` contract verified at
  `internal/provider/provider.go:279-288`; per-stream ascending-block order
  required by the watermark (`TransferPage` comment :269-271).
- Indexer flow verified: `EnsureIndexed → ensure → attemptFetch`
  (`internal/transaction/service.go:179-566`) — HeadBlock → window
  `[watermark−tail+1 … head]` → stream-pair `LookupTransfers` pages →
  `buildRows` → `UpsertTransactions` → per-stream min-coverage watermark.
- transfer_index identity is content-derived
  (`internal/transaction/identity.go:41-53,197-210`): `{n|t}:{hash16}.{k}`
  — chain-agnostic, reusable verbatim for TRON.
- **New finding**: `internal/httpserver/server.go:270` is a 4th production
  `GetOrCreate` caller the audit's per-module list did not cite.
- **Live probe evidence (no key, 2026-11-06)**: TronGrid
  `walletsolidity/getnowblock` returns solidified head HTTP 200;
  `/v1/accounts/{a}/transactions?only_confirmed=true` rows carry
  `txID`/`blockNumber`/`block_timestamp` + `meta.fingerprint`;
  `/transactions/trc20` rows carry `transaction_id`/`from`/`to`/
  `token_info`/`value`/`block_timestamp`/`type` but **no block number** —
  the cursor→watermark bridge is the central adapter design problem.

## Phase Roadmap

| # | Phase | File | Deps | Deliverable |
|---|-------|------|------|-------------|
| 1 | Audit Drift Check | `phase-01-audit-drift-check.md` | — | `reports/audit-drift-check.md` (VERIFIED/DRIFTED per claim) |
| 2 | Provider Eval | `phase-02-provider-eval.md` | 1 | `docs/tron-provider-decision.md` + discovery table VERIFIED/REJECTED |
| 3 | Indexer Semantics Doc | `phase-03-indexer-semantics.md` | 2 | `docs/tron-indexer-semantics.md` |
| 4 | address_type Generalization | `phase-04-address-type-generalization.md` | 1 | `GetOrCreate` takes `network.Network`; all callers updated; tests |
| 5 | Dormant Adapter | `phase-05-tron-adapter.md` | 2,3 | `internal/provider/<chosen>/` + tests, zero-traffic gate |
| 6 | Gates & Docs | `phase-06-gates-docs.md` | 2,3,4,5 | gofmt/vet/build/test clean; audit doc updated; final report |

## Non-Goals / Locked Surfaces (stop conditions)

- No `blockchain_networks`/`is_active` changes; no transactions or
  `wallet_index_state` DDL; no adapter wiring into `cmd/api`/`cmd/worker`
  provider selectors, `providerconfig` descriptors, or `data_sources` seed.
- No `tron.csv`/`wattrFiles` changes; no OpenAPI `Address` pattern change;
  OFAC mapper TRX path stays UNRESOLVED (spec §5.6); `warmindex/queue.go`
  untouched; `identity.go` `NormalizeEVM` calls untouched (EVM-only
  upstreams today — recorded in audit as enablement work).
- No paid dependency, no embedded keys, no new external Go dependency
  without asking.

## Risk Matrix

| Risk | Mitigation |
|------|-----------|
| Provider eval picks vendor on stale quota claims | Phase 2 re-verifies against current vendor docs + live probes **under a calls-per-ensure worst-case model**; STOP if no free-no-card candidate qualifies |
| TRC-20 tri-problem: ms-window bridge input, missing per-row block numbers (stored evidence — `txRowJSON.BlockNumber` non-pointer ⇒ `0`+`'pending'` fabrication), and no per-stream coverage channel in `StreamStatus` | Phase 3 decides each separately (b1/b2/b3): token-carried resolved window; explicit per-row resolution options with named non-options; INV-1/INV-2 binding invariants test-asserted in Phase 5. Defer-unfinished is the only no-resolution honest path |
| Silent coverage lies (cursor advance past unresolved rows; undeclared streams; block-less rows) | INV-1/INV-2 binding invariants + test fixtures; block-less emission is a named non-option |
| `Enabled(net)` semantics drift (keyless vs keyed) + layering conflation | Key-required recommendation w/ precedent in decision doc; adapter gates config only — resolver gates activeness (`Network` has no `IsActive`); zero upstream calls asserted via httptest counter |
| `GetOrCreate` signature change breaks unseen callers | Phase 4 enumerates every call site incl. tests; `GetOrCreateWallet` direct callers stay param-driven |
| Adapter "done but dead code" criticism | Acceptable by design: package is compile-checked + unit-tested; the §15 gate intentionally keeps it unwired (documented in plan + audit) |

## Stage 1 Debate Summary

- **Devin Critic session**: `swanky-icon` (round 1 `auto` mode — write tool
  rejected in non-interactive mode, critique delivered inline; round 2
  resumed same session for amendment verification).
- **Round 1 verdict**: CHANGES_REQUIRED — all ~35 plan citations verified
  accurate at HEAD, but 10 amendments (RA-1..RA-10) required.
- **Key challenges raised**: (1) TRC-20 missing block number is a
  tri-problem — input windowing, stored evidence (`txRowJSON.BlockNumber`
  non-pointer vs nullable `transactions.block_number` ⇒ block-0 +
  'pending' fabrication), and a nonexistent per-stream coverage channel
  in `StreamStatus`; (2) two binding invariants — cursor never advances
  past unresolved rows, every emitted stream must be declared in
  `page.Streams` — whose violation is silent data loss; (3) verbatim-hex
  native addresses would 100% fail `NormalizeTron` (base58check-only) ⇒
  base58-canonical emission decided; (4) quota must be judged under
  calls-per-ensure worst case (fan-out), not raw free-tier numbers;
  (5) Enabled() layering — resolver gates activeness, adapter gates
  config only.
- **Concessions applied**: all 10 amendments written into phase-01..06;
  Phase 3 restructured around (b1) token-carried window bridge, (b2)
  explicit per-row block-resolution options with named non-options,
  (b3) binding invariants; Phase 2 gained the mandatory probe list +
  quota model; Phase 4 gained `ErrUnknownAddressFormat` + test
  conventions; Phase 5 gained interface assertion, invariants-as-tests,
  fan-out bound, dormant package doc; Phase 6 gained doc-consistency
  items + honest deps.
- **Round 2 verdict**: PLAN_CONSENSUS_REACHED — all amendments verified
  as genuinely applied; residual items were cosmetic plan.md staleness
  (now fixed).

## Success Criteria

- [ ] tron rows still `is_active=FALSE`; `/networks` returns bsc+eth only
- [ ] Adapter emits zero upstream calls when `Enabled()=false` (test)
- [ ] Same block window refetch → identical transfer sets (replay-safe test)
- [ ] Ascending-order + per-stream `Finished` contracts hold in tests
- [ ] `GetOrCreate` stamps `"tron"` for tron-format networks, `"evm"` for
      EVM — both tested
- [ ] Discovery table: every TRON row VERIFIED-with-evidence or
      REJECTED-with-reason; zero UNCONFIRMED
- [ ] `gofmt -l .` empty; `go vet ./...`, `go build ./...`, `go test ./...`
      clean
