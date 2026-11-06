---
phase: 3
title: "Indexer Semantics Doc — transfer_index, cursor→watermark tri-problem, finality, Kinds"
status: completed
priority: P1
dependencies: [2]
---

# Phase 3: TRON Indexer Semantics Document

## Overview

Write `docs/tron-indexer-semantics.md`: the semantic decisions a §15
reviewer needs before any TRON enablement — transfer_index identity, the
**three-problem** cursor→watermark design, finality policy, and Kinds
mapping. Every decision must cite live-probed or code evidence; undecided
items are recorded as enablement blockers, not silently resolved.

> **Critic amendment RA-1:** the TRC-20 block-number gap is not one
> "cursor→watermark" problem — it is three distinct problems:
> (b1) *input side* — mapping `FromBlock`/`ToBlock` to TronGrid's
> ms-timestamp filters; (b2) *stored evidence* — TRC-20 rows carry no
> block number and `txRowJSON.BlockNumber` is non-pointer `int64`
> (`identity.go:32`), so an absent block persists `0` +
> `confirmation_status='pending'` — fabricated evidence in a
> risk-evidence store (while `transactions.block_number` IS nullable,
> `migrations/000008:22`); (b3) *coverage channel* — an unfinished
> stream's coverage derives ONLY from `Transfer.BlockNumber`
> (`service.go:454-456,516-519`), so block-less rows stall the watermark
> forever (`PARTIAL` + `history_truncated` permanently), and
> `StreamStatus` (`provider.go:258-261`) carries only `{Name, Finished}`
> — there is no channel for "stream covered through block B". The doc
> must decide each separately.

## Requirements

- Functional: the doc defines, with evidence, the semantics below.
- Non-functional: document-only; no code.

## Architecture — required decisions

### (a) transfer_index identity

Current scheme (`internal/transaction/identity.go:41-53,197-210`):
`{n|t}:{contentHash16}.{k}` where contentHash covers
`kind|asset|from|to|amount` and `.k` is the max-per-stream occurrence
count under `UNIQUE(network_id, tx_hash, transfer_index)`. The scheme is
content-derived and chain-agnostic. The doc must specify:

- Reuse of the identical scheme for TRON (deterministic, stable across
  refetches, unique under the same constraint) — or an explicit
  deviation with justification.
- TRON field mapping into the hash: `from`/`to` = **base58** addresses
  (see (d) — adapter emits canonical base58), `asset` = TRC-20 contract
  base58 for TOKEN, `""` for NATIVE; `amount` = raw integer decimal
  string (`value` for TRC-20; `parameter.value.amount` sun for TRX) —
  never a float.
- Stream names for per-stream occurrence counting: `trx` and `trc20`
  (the indexer's `Stream` field feeds identity dedup AND per-stream
  coverage — see invariant INV-2 below).
- Same-tx duplicate events (identical content hash, same stream) → `.k`
  occurrence suffix; the suffix is order-insensitive → replay-safe by
  construction — pin with a fixture in Phase 5.

### (b) The three-problem design (was "cursor→watermark")

#### (b1) Window bridge — input side, BOTH streams

`opts.{FromBlock,ToBlock}` → `min_timestamp`/`max_timestamp` via
`walletsolidity/getblockbynum` (verify `block_header.raw_data.timestamp`
presence + `num:0` degenerate boundary). Both bridge calls are real
upstream calls → `TransferPage.UpstreamCalls` + request budget.

- **Resolved window is carried inside the page token** — without it every
  step repays 2 bridge calls (critic RA-4; fingerprint continuation needs
  identical filters anyway).
- Guard `FromBlock ≤ ToBlock` even though the indexer's `head < fetchFrom`
  lag path exists (`service.go:398-408`).
- Boundary semantics: probe `min/max_timestamp` inclusivity (Phase-2 probe
  list). Double-coverage at shared boundaries is dedup-safe (UNIQUE +
  content-hash identity); silent exclusion is not — if exclusivity is
  observed, widen the low edge by a fixed delta (dedup-safe) and document.
- Block-timestamp monotonicity is an **assumption to state** — TRON block
  timestamps are ~3s-slot-quantized and skipped slots exist; document the
  consequence of non-monotonic timestamps (rows outside the claimed
  window silently excluded) and the mitigation (window widening +
  boundary probes).

#### (b2) TRC-20 per-row block-number resolution — stored evidence

Options, decided explicitly with calls-per-ensure arithmetic:

- **Option i — unique-txID fan-out:** `walletsolidity/gettransactioninfobyid`
  per unique `transaction_id` in the trc20 page (dedup within the step;
  bounded by trc20 page size → clamp the trc20 page size per Phase 5 and
  state the bound). Every call → `UpstreamCalls`. txinfo indexing lags
  the event index → bounded retry inside the step → on persistent miss
  `ErrDegraded` (honest) — **never** emit block-less rows (see
  non-options).
- **Option ii — anchor map + fan-out fallback:** native-stream rows carry
  `blockNumber` + `block_timestamp` → same-timestamp anchors resolve some
  trc20 rows for free; misses/ambiguities fall back to (i). Requires
  evidence that native stream surfaces the anchor blocks needed AND that
  timestamps are unique per block — else stay with (i) alone.
- **Option iii — defer trc20:** emit only `trx`; declare `trc20`
  permanently `Finished=false` in `page.Streams` → honest perpetual
  `PARTIAL` + `history_truncated`, zero fan-out cost. The only
  no-resolution option that stays honest.
- **Named non-options (critic RA-1):** emitting block-less trc20 rows
  (fabricates `block_number=0` + `'pending'`; only if a
  `txRowJSON.BlockNumber→*int64` contract change were ever approved is
  honest-NULL possible — record as enablement-decision, not this scope);
  dropping unresolvable rows while advancing the fingerprint (silent
  coverage lie); timestamp-arithmetic block estimation (missed slots);
  block-range scanning (unbounded calls); a `StreamStatus.CoveredThrough`
  contract extension (possible future decision — NOT this scope; note it
  fixes coverage but not stored evidence anyway).

#### (b3) Binding invariants (critic RA-2 — test-asserted in Phase 5)

- **INV-1 cursor-advance:** a fingerprint may only advance past
  fully-resolved rows. Partial resolution inside a step ⇒ typed error,
  cursor held — never drop-and-advance.
- **INV-2 stream-declaration:** every `Transfer.Stream` value emitted
  MUST appear in `page.Streams` every step. Verified failure mode:
  `streamSeen` comes only from `page.Streams` (`service.go:444-449`) — an
  undeclared stream never gates coverage → `fetchTo` watermark over
  incomplete data = **permanent silent data loss**.
- **INV-3 (documented, enablement-side):** `streamMaxBlock` is computed
  on raw transfers BEFORE `buildRows` normalization
  (`service.go:450-457` vs `:479`) — if tron rows ever reach a
  still-EVM `buildRows`, they skip as `InvalidAddress` yet coverage
  advances. Record in the blocker list; the identity.go switch is
  load-bearing, not cosmetic.

### (c) Finality

- Solidified head ONLY: `HeadBlock` = `walletsolidity/getnowblock`
  `block_header.raw_data.number` — never `wallet/getnowblock`
  (unconfirmed). Verified live keyless.
- All upstream reads on the `walletsolidity/` view (consistency with
  `only_confirmed` — critic nit (e)): `getnowblock`, `getblockbynum`,
  `gettransactioninfobyid`.
- Both account streams use `only_confirmed=true`.
- `Confirmations` = `opts.HeadBlock − blockNumber + 1`.
- `ReorgTail` rationale (critic (i)): justify by **upstream index lag**
  (event indexing lags solidification), NOT DPoS reorg — solidified
  blocks do not reorg. Recommend a small tail (~5–20 blocks) and state
  the cost asymmetry: every tail-refetched row repays the (b2) fan-out.

### (d) Address form — DECIDED (critic RA-3)

- `NormalizeTron` accepts base58check only; upstream native-stream hex
  `41…` payloads would error → **100% InvalidAddress skip**.
- **Decision: base58-canonical emission.** The adapter converts native
  hex → base58check before emitting (provider quirks live in the adapter
  — expanding `NormalizeTron` to dual-form would change frozen
  groundwork for one provider's wire format). trc20 `from`/`to`/
  `token_info.address` are already base58 → verbatim. Document the
  deviation-from-verbatim and the `0x41`-version assertion on converted
  payloads.

### (e) Kinds mapping

- `NATIVE` ↔ rows in `/transactions` where **any**
  `raw_data.contract[i].type == "TransferContract"` (iterate the array —
  multi-contract txs exist; `[0]`-only misses them — critic (h)).
  `parameter.value.amount` (sun) → `AmountRaw`; `owner_address`/
  `to_address` (hex → base58 per (d)).
- `TOKEN` ↔ `/transactions/trc20` rows where `type == "Transfer"`
  (`token_info.address` → `Asset`, `value` → `AmountRaw`,
  `transaction_id` → `TxHash`). Name excluded trc20 types (Approve etc.)
  with probe evidence.
- Non-TransferContract native types are excluded — documented scope
  exclusion; INTERNAL/`internal_transactions` TRX moves are excluded
  per chainscan parity (document explicitly).
- Zero-amount and zero-address (mint/burn) rows: pick keep-or-drop
  explicitly — an unstated exclusion is a coverage lie (critic #15).
- `IsError`: `ret[0].contractRet != "SUCCESS"` ⇒ true (native). For
  trc20, gather evidence on whether failed events can appear under
  `only_confirmed`; else state the conservative default.
- Empty `Kinds` = both streams (`chainscan adapter.go:236-247`
  convention; the indexer always sends both at `service.go:432`).

### (f) Enablement-blocker section (mandatory)

Record as out-of-scope-but-required-at-enablement:
- `identity.go` `NormalizeEVM` → `wallet.Normalize(net, …)` switch
  (load-bearing per INV-3).
- `queries/transactions.sql:30` `'evm'` literal → format-aware stamping.
- `internal/transaction/list.go:91` `NormalizeEVM` asset filter
  (critic RA-7).
- `data_sources` row for the adapter `Name()`, providerconfig
  descriptor, selector wiring, `TX_PROVIDER_TRON` env surface.
- Remaining `NormalizeEVM` upstream sites for completeness:
  `walletattribution/mapper.go:30`, `stablefreeze/scanner.go:284`,
  `ingestion/pipeline_freeze.go:176`, `ofac/mapper.go:124`,
  `warmindex/queue.go:46-47`.
- `wallets.address_type` is `VARCHAR(30)` with no CHECK
  (`000001:20`) — `'tron'` is schema-legal today; no DDL needed.

## Related Code Files

- Create: `docs/tron-indexer-semantics.md`
- Read: `internal/transaction/service.go`, `internal/transaction/identity.go`,
  `internal/provider/provider.go`, `internal/provider/chainscan/adapter.go`,
  `docs/non-evm-audit.md`, `docs/tron-provider-decision.md`

## Implementation Steps

1. Re-probe any unverified shapes needed (gettransactioninfobyid
   blockNumber, getblockbynum timestamp + `num:0`, raw_data.contract
   layout, address forms in native rows, timestamp inclusivity, anchor
   viability).
2. Write the doc: (a)–(f) sections; each decision = decision + evidence
   (file:line or live probe) + edge cases + rejected alternatives.
3. Compute the calls-per-ensure worst case under the chosen (b2) option
   and reconcile with Phase-2 quota findings.
4. Cross-check every claim against actual upstream shapes — the doc is a
   §15 artifact; an unverifiable claim fails the phase.

## Success Criteria

- [x] All decisions documented with cited evidence
- [x] The tri-problem (b1/b2/b3) is decided separately and explicitly
- [x] transfer_index scheme provably deterministic + refetch-stable +
      unique under the existing UNIQUE constraint
- [x] INV-1/INV-2 stated as binding contract invariants for Phase 5
- [x] Finality = solidified-only; tail justified by index lag
- [x] base58-canonical decision recorded with justification
- [x] calls-per-ensure arithmetic reconciled with provider decision
- [x] Enablement-blocker list complete (incl. `list.go:91` and the
      `streamMaxBlock`-before-`buildRows` ordering note)

## Risk Assessment

The doc may reveal the free adapter design is not fully sound — that is a
SUCCESS outcome: record the blocker honestly rather than hiding it in
adapter code. The §15 reviewer needs the truth, not a clean-looking doc.
