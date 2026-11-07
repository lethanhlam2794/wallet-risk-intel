---
phase: 3
title: "Indexer semantics doc (a)–(h)"
status: pending
priority: P2
dependencies: [2]
---

# Phase 3: Solana Indexer Semantics

## Overview

Write `docs/solana-indexer-semantics.md` deciding — with evidence — each
open problem (a)–(h). Mirror `docs/tron-indexer-semantics.md` *structure*
(status header, lettered decisions, named non-options, enablement
blockers table, calls-per-ensure reconciliation), NOT its content.

## Required decisions

**(a) slot→`block_number` mapping + intra-slot order.** `block_number`
stores SLOT (documented, no DDL change). Two signatures in one slot need
a deterministic emission order — decide between reversed-desc-position
(= canonical intra-block order, if Phase-2 probes confirm the RPC
ordering guarantee) or a documented deterministic tiebreak
(e.g. base58-decoded signature ordering), with the honesty note if the
guarantee can't be proven.

**(b) signature-desc pagination → ascending `FromBlock`/`ToBlock`
bridge.** BINDING INVARIANT (Critic R1): the emitted set per stream MUST
be a contiguous ascending prefix of the requested window — `service.go`'s
`cov = streamMaxBlock − 1` watermark formula is only sound under
bottom-anchored emission; a top-anchored slice `(frontier, T]` over-claims
`[F, frontier]` forever. Therefore **no row is emitted until desc
collection has passed below `FromBlock` (or the ledger floor)** — the
entire window's signature list is known before emission; emission is then
ascending slices. Decide sig-list persistence across steps:
token-embedded bounded list (~64–90B/sig; window sig-cap),
bounded per-step re-collection (token = emission cursor), or a
documented named non-option. Edge cases to pin: `before`/`until`
semantics (probe); window bounded above when `fetchTo < head`
(`getBlock` signatures `before`-seed or WindowCap restriction);
>1000 sigs in one window and the degenerate >1000-in-one-slot case;
multi-step emission order across steps (global ascending, not just
per-step).

**(c) finality** — `finalized` commitment ONLY for head + tx reads;
`confirmed`/`processed` can fork. `HeadBlock` = `getSlot@finalized`.
Decide `ReorgTail["solana"]` rationale (finalized slots don't reorg —
tail exists for index lag if any; a 0 or small tail must be justified).

**(d) `transfer_index` scheme per stream** — reuse content-derived
`{n|t}:{hash16}.{k}` ONLY IF determinism + refetch-stability +
`UNIQUE(network_id, tx_hash, transfer_index)` hold on base58 input.
Document why; if not, define the alternative. Also pin the SPL
`from`/`to` semantics feeding the hash: token-account addresses (the raw
instruction endpoints) vs owner addresses — token-account emission is
raw-evidence; owner mapping requires `tokenBalances` attribution —
decide and document.

**(e) SPL extraction path** — pick ONE authoritative source:
`innerInstructions` parsed `transfer`/`transferChecked` (token +
token-2022 programs, outer AND inner) vs `pre/postTokenBalances` deltas
vs a provider enhanced-tx API. Failed txs (`meta.err != null`):
rolled-back inner instructions never emit — but `IsError` evidence for
the tx itself must be decided honestly (fee-only txs: a failed tx still
charges fees, and fee is NOT a transfer instruction — include/exclude,
state why). Vote/system-program noise: filtered or counted, honestly.

**(f) archival truncation → `history_truncated`/coverage channel** —
CORRECTED (Critic R1): the signal is the node's retained-ledger floor
(`getFirstAvailableBlock`/`minimumLedgerSlot`, Phase-2 probe) being above
`FromBlock` ⇒ `[F, floor)` unobservable. NOT "oldest observed sig >
FromBlock" — every address whose first activity postdates the window
bottom trips that (perpetual partial for all wallets). Outcome: emit
everything honestly collected, stream `Finished=false`, end the token ⇒
indexer writes `partial` + `history_truncated`; watermark stays the
top-side bound `min(fetchTo, streamMaxBlock−1)` — the only value
`service.go:507-523` produces. Verify this composition against
`service.go:493-559` + `writeTerminal` and document.

**(h) SPL discovery coverage** (added after Critic R1) — decide how the
`spl` stream honestly represents coverage when owner-feed can't see
incoming ATA transfers: per-ATA auxiliary feeds (`getTokenAccountsByOwner`
enumeration, closed-ATA blindness documented), provider-level owner
indexing (keyed path), or the coverage-limited named non-option
(declare `spl` honestly — e.g. perpetual-unfinished ⇒ eternal partial —
or scope SPL coverage to "txs where the owner is a tx account" and state
the blind spot). This decision constrains `spl`'s `Finished` semantics
and the provider requirement-1 verdict.

**(g) Kinds mapping** — `NATIVE`↔lamports SOL (system-program
`transfer`/`transferWithSeed` instructions), `TOKEN`↔SPL (mint = asset
id, `amount_raw` = u64 decimal string; `transferChecked` carries mint —
`transfer` needs mint lookup via `tokenBalances` or `postTokenBalances`
attribution — decide the resolution path).

## Non-decidable items

Use the named-non-option pattern from `docs/tron-indexer-semantics.md` —
deferring with a named non-option is the ONLY honest no-resolution path;
no silent gaps.

## Enablement blockers table (§f-equivalent)

Mirror TRON's table for Solana: `identity.go` `NormalizeEVM`→
`wallet.Normalize(net,…)` switch (load-bearing, INV-3 analogue),
`queries/transactions.sql:30` `'evm'` literal, `list.go:91`,
`data_sources`/providerconfig/selector/env wiring, `solana.csv`,
OpenAPI pattern, OFAC `SOL`→solana mapping, warmindex gates,
`is_active` flip, `address_type='solana'` (schema-legal today).

## Related Code Files

- Create: `docs/solana-indexer-semantics.md`.
- Read: `service.go:415-566`, `identity.go`, `provider.go:242-288`,
  probe transcripts from Phase 2, `docs/tron-indexer-semantics.md`.

## Implementation Steps

1. Draft each lettered decision with probe/code citations.
2. Enumerate named non-options per rejected alternative.
3. Write the binding-invariants list (INV-1/INV-2 analogues for Solana).
4. Reconcile calls-per-ensure against the Phase-2 verified quota.
5. Write the enablement-blockers table.

## Success Criteria

- [ ] All of (a)–(h) resolved or named-non-option deferred.
- [ ] INV-style binding invariants listed for Phase-4 tests.
- [ ] Calls-per-ensure arithmetic reconciled vs decision-doc quota.

## Risk Assessment

The bridge design (b) is the plan's deepest assumption — a wrong
frontier rule silently lies about coverage. Mitigation: every rule is
asserted by Phase-4 boundary tests (slot-straddling window, >1000-sig
window, truncated horizon) before sign-off.
