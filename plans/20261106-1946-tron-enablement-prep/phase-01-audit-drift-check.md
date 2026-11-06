---
phase: 1
title: "Audit Drift Check — verify non-evm-audit.md claims against current code"
status: completed
priority: P2
dependencies: []
---

# Phase 1: Audit Drift Check

## Overview

Trace `EnsureIndexed → TxProvider → watermark` end-to-end and verify every
file:line claim in `docs/non-evm-audit.md` against current code. This is a
read-only verification phase — its output is a drift report, not code.

## Requirements

- Functional: every claim in the audit's "Per-module blockers" section is
  re-verified at current HEAD; any drift is reported with the corrected
  file:line and the correct current shape.
- Non-functional: zero code changes in this phase.

## Architecture

Verification checklist (orchestrator pre-verified ✓ = confirmed, ⚠ = drift
suspected or partial):

| Audit claim | Location | Orchestrator check |
|---|---|---|
| TxProvider contract, block-window shaped + ascending order | `internal/provider/provider.go:242-288` | ✓ verified (`:279-288`, order comment `:269-271`) |
| `GetOrCreate` stamps `"evm"` via `$4` | `internal/wallet/service.go:25` | ✓ verified |
| 'evm' literals in queries | `queries/transactions.sql:30`, `ofac_publish.sql:57`, `labels_publish.sql:12`, `stablecoin_freeze.sql:61` (post-comment line numbers; originally :27/:54/:9/:58) | ✓ verified all 4 |
| `wallets.sql` param-driven (not a literal site) | `queries/wallets.sql` | ✓ verified (`$4` param) |
| buildRows NormalizeEVM (EVM-only upstream) | `internal/transaction/identity.go:88,93,104` | ✓ verified |
| watermark = min per-stream contiguous coverage | `internal/transaction/service.go` | ✓ verified `:490-524` |
| warmindex stored-address EVM check | `internal/warmindex/queue.go:46-47` | ⚠ Cook must verify (Critic pre-verified — re-confirm at HEAD) |
| wattrFiles code+".csv" maps | `cmd/api/main.go:651-658`, `cmd/worker/main.go:285-292` | ✓ verified both |
| OFAC TRX listing UNRESOLVED | `internal/provider/ofac/mapper.go:32-44` | ⚠ Cook must verify (Critic pre-verified) |
| Remaining `NormalizeEVM` sites (audit :108-118) | `internal/transaction/list.go:91`, `walletattribution/mapper.go:30`, `stablefreeze/scanner.go:284`, `ingestion/pipeline_freeze.go:176`, `ofac/mapper.go:124` | ⚠ verify each for the Phase-3 blocker list (critic amendment — `list.go:91` was missing from the blocker list) |
| `Normalize`/`ResolveAndNormalize` dispatch | `internal/wallet/address.go:51-81` | ✓ verified |
| migration `000008` columns + UNIQUE | `migrations/000008_transactions.up.sql` | ✓ verified `:12-44` |
| `000019` source_key rekey | `migrations/000019_*` | ⚠ Cook must verify shape |
| `000026` inactive rows | `migrations/000026_*` | ✓ verified |
| **MISSED in audit**: 4th GetOrCreate caller | `internal/httpserver/server.go:270` | ⚠ new finding — must be added to audit doc in phase 6 |
| `wallet_index_state.indexed_until_block` semantics | `migrations/000008:42-77` | ✓ verified |

## Related Code Files

- Read: every file listed above.
- Create: `plans/20261106-1946-tron-enablement-prep/reports/audit-drift-check.md`
- Modify/Delete: none.

## Implementation Steps

1. Read `docs/non-evm-audit.md` in full.
2. For each row above (and any additional claims in the audit not listed),
   open the cited file and confirm or correct the claim.
3. Enumerate ALL `GetOrCreate` call sites (`grep -rn "GetOrCreate(ctx"` —
   known: `screening/service.go:146`, `enrichment/service.go:117`,
   `transaction/service.go:182`, `httpserver/server.go:270`, plus test
   callers in `failover_test.go`, `service_test.go`, `killswitch_test.go`,
   `screening/service_test.go`) and the `WalletStore` interface
   (`screening/service.go:54-57`). Record the complete list for Phase 4.
4. Trace `EnsureIndexed → ensure → attemptFetch → LookupTransfers →
   buildRows → watermark` and note every contract assumption the TRON
   adapter must satisfy (ascending order, Stream.Name/Finished,
   UpstreamCalls accounting, HeadBlock semantics, Transfer fields).
5. Write `reports/audit-drift-check.md` — each claim VERIFIED (with
   file:line evidence) or DRIFTED (with corrected evidence), plus the full
   call-site inventory.

## Success Criteria

- [x] Every audit claim classified VERIFIED or DRIFTED with file:line evidence
- [x] Complete `GetOrCreate` call-site inventory (prod + test + interface)
- [x] Contract-assumption list for the adapter is complete and cited
- [x] No source file modified in this phase

## Risk Assessment

Drift is unlikely (audit written same-day) but must be proven, not assumed.
A missed call site in Phase 4 is the main downstream risk — the inventory
produced here is the Phase 4 contract.
