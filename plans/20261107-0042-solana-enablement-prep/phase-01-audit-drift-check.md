---
phase: 1
title: "Scout / drift check"
status: completed
priority: P2
dependencies: []
---

# Phase 1: Scout / Drift Check

## Overview

Verify every Solana claim in `docs/non-evm-audit.md` and
`docs/non-evm-provider-discovery.md` against the code **post-TRON-prep**,
adjudicating each VERIFIED or DRIFTED with file:line evidence. Produces
`reports/audit-drift-check.md`.

## Requirements

- Functional: per-claim verdict with citation; every claim checked against
  CURRENT code, not the doc's memory of it.
- Non-functional: read-only — no production code touched.

## Claims to adjudicate (from prompt + docs)

1. `GetOrCreate` signature stamps `net.AddressFormat()` — expect
   `internal/wallet/service.go:29-35`, `ErrUnknownAddressFormat` fail-closed.
2. OFAC `SOL` path maps `CHAIN_SPECIFIC` with unmapped network —
   `internal/provider/ofac/mapper.go:32-44`, `mapper_test.go:29`.
3. `identity.go` chain-agnosticism — `contentHash` (identity.go:41-45) is
   content-derived, but `buildRows` calls `NormalizeEVM` at
   `identity.go:88,93,104` — the enablement-blocker surface (INV-3
   analogue) must be re-confirmed and its exact call sites re-cited.
4. warmindex EVM boundary — `warmindex/queue.go:47` (`NormalizeEVM`
   canonical gate), `warmindex/seed.go:195-203`
   (`AddressFormatForNamespace != EVM` + `IsEVMAddress` gates).
5. `wallet.Normalize` dispatch — `address.go:54-65` solana branch.
6. migration `000026` `is_active=FALSE` state — and the DOWN migration's
   FK safety (labels publish path D4b predicates still intact:
   `queries/labels_staging.sql`, `labels_publish.sql` `AND is_active`).
7. TxProvider contract shape — `provider.go:242-288` (ascending-block
   ordering requirement at :269-271, `UpstreamCalls` accounting).
8. Indexer mechanics the adapter must satisfy — `service.go`
   `streamSeen`/`streamMaxBlock`/`finishedStreams`/`history_truncated`
   write path (~:415-566), `MaxPagesHop0=5`, Budget `settle` post-hoc
   (~:339-344), `head < fetchFrom` lag path (~:398-408).
9. Discovery-doc Solana rows still UNCONFIRMED; `wattrFiles` solana.csv
   never requested (resolver = active-only).
10. `x/time/rate` + `resilience` toolkit availability for the adapter
    (used by trongrid: `internal/provider/resilience`).
11. **`identity.go:116` `txHash := strings.ToLower(tr.TxHash)`** — the
    lowercasing corrupts case-sensitive base58 signatures (decodes to
    different bytes → stored tx_hash unresolvable upstream). Verify the
    line, confirm it runs for every provider row, and record it as a
    blocker-table landmine (worse than a skip — silent corruption).
12. **NULL-`block_timestamp` read-path policy** — `list.go` +
    `queries/transactions.sql:64-67` order `block_timestamp DESC, id DESC`
    and keyset on `(block_timestamp, id) < (cursor_ts, cursor_id)`. NULL-ts
    rows sort first under DESC but can never satisfy a non-NULL cursor →
    unreachable past page 1. Adjudicate the read-path consequence of the
    no-fabricated-`blockTime` rule.
13. **`Transfer.Confirmations` slot semantics** — `head − block + 1`
    drives `confirmation_status` (`identity.go:188-191`); slot distance
    ≠ confirmation depth (skipped slots inflate it). Decide what the
    adapter may honestly emit.

## Related Code Files

- Read: everything cited above; `internal/provider/trongrid/` (mirror).
- Create: `plans/20261107-0042-solana-enablement-prep/reports/audit-drift-check.md`.

## Implementation Steps

1. Re-read the two audit/discovery docs; enumerate every checkable claim.
2. Verify each against file:line in current code (incl. test files).
3. Write `audit-drift-check.md` with a VERIFIED/DRIFTED table; every
   DRIFTED item gets the drift description + plan impact.

## Success Criteria

- [x] Every claim has an explicit verdict + citation — no "assumed".
- [x] Any drift found is reflected in Phase 2-4 inputs before proceeding.

## Risk Assessment

Low — read-only. Main risk is shallow verification; mitigate by requiring
file:line citations for every verdict.
