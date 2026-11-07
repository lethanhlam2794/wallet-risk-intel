---
phase: 2
title: "Provider evaluation + live probes"
status: completed
priority: P2
dependencies: [1]
---

# Phase 2: Provider Evaluation

## Overview

Verify each Solana TxProvider candidate in the discovery table against
the 6 contract requirements using CURRENT vendor docs (cite URL + date)
plus live keyless probes of the public mainnet-beta JSON-RPC. Write
`docs/solana-provider-decision.md` and flip every UNCONFIRMED row in
`docs/non-evm-provider-discovery.md` to VERIFIED/REJECTED.

## Requirements

- Functional: decision doc naming the chosen provider, verified
  quota/limit evidence, why others rejected; calls-per-ensure worst-case
  model reconciled against verified quota.
- Non-functional: probes only hit free endpoints needing no key and no
  account; no key is embedded or paid for.

## Candidates (initial table — expand if a free candidate surfaces)

- **Public Solana JSON-RPC** (`api.mainnet-beta.solana.com`) — keyless
  baseline. MUST probe: `getSignaturesForAddress` page shape (field set,
  ≤1000 cap), `before`/`until` cursor semantics, ordering guarantee
  (desc; intra-slot ordering — is same-slot order canonical block order
  reversed?), `commitment` param effect, `getSlot` finalized head,
  `getTransaction` `maxSupportedTransactionVersion` requirement +
  `innerInstructions`/`prePostTokenBalances` availability under
  `jsonParsed` encoding, `blockTime` nullability on finalized entries
  (BOTH `getSignaturesForAddress` items AND `getTransaction`),
  JSON-RPC batch request support + whether batched calls count
  individually toward rate limits, documented/measured rate limits.
- **Ledger-floor probes (Critic R1)**: `getFirstAvailableBlock` and/or
  `minimumLedgerSlot` on the public node — the only honest truncation
  signal (distinguishes node ledger purge from a genuinely new address;
  "oldest sig > FromBlock" alone is NOT a truncation signal).
- **SPL discovery gap probe (Critic R1 — decision-determining)**:
  empirically verify that an incoming SPL transfer to a wallet's ATA is
  absent from `getSignaturesForAddress(owner)` while present on the
  ATA's feed; measure `getTokenAccountsByOwner` enumeration cost and
  confirm closed ATAs are unenumerable. This quantifies whether the
  keyless baseline can satisfy requirement 1 for SPL-incoming.
- **`getBlock` signatures-detail probe (Critic R1)**: `getBlock` with
  `transactionDetails:"signatures"` (or equivalent) as a slot-anchored
  `before`-seed — needed to bound desc collection when `fetchTo < head`
  (WindowCap or lagged head).
- **Helius Enhanced Transactions** — keyed free tier (reportable
  candidate): verify current free quota/RPS, signup card requirement,
  parsed-tx field fidelity vs raw RPC (`tokenTransfers`,
  `nativeTransfers`, `fee`, `transactionError`, `slot`, `timestamp`),
  `before` cursor semantics, ToS commercial-use clause. Chosen only with
  user approval (stop condition).
- **Others in discovery table** (Ankr/Covalent/Tatum/Moralis/QuickNode/
  Solscan or any newly surfaced free candidate) — verify or reject with
  evidence, especially whether their free Solana history endpoints exist
  at all and whether signup needs a card.

## The 6 contract requirements (from discovery doc)

1. Per-address native+token transfer history (SOL + SPL incl. inner
   instructions).
2. Deterministic ordering usable for watermarks.
3. Typed finalized head/slot endpoint.
4. `PageToken`-compatible pagination with bounded page sizes.
5. Free tier without a credit card.
6. Terms permitting our use case.

## STOP conditions (task-level)

- If the only viable candidates require a credit card → STOP, report.
- If the only viable path is a keyed free tier → decision doc reports it
  as the reportable option; choosing it needs user approval before
  Phase 4. A viable keyless path removes this gate.

## Related Code Files

- Create: `docs/solana-provider-decision.md`;
  `plans/.../reports/probe-*.md` (transcribed probe outputs).
- Modify: `docs/non-evm-provider-discovery.md` (row verdicts).

## Implementation Steps

1. Probe public RPC endpoints with `curl`/script; transcribe verbatim
   outputs into `reports/probe-solana-rpc.md` (shape evidence the
   semantics doc cites).
2. Fetch current vendor docs/ToS for each candidate; record URL + date.
3. Score each candidate against reqs 1-6; build the calls-per-ensure
   worst-case model under `MaxPagesHop0=5` and the chosen page/fan-out
   bounds.
4. Write the decision doc (mirror `docs/tron-provider-decision.md`
   structure: decision table, sources w/ dates, calls-per-ensure,
   rejected candidates, out-of-table exclusions, `Enabled()`
   recommendation).
5. Flip discovery-table Solana rows to VERIFIED/REJECTED.

## Success Criteria

- [x] Zero UNCONFIRMED Solana rows left; every verdict cites evidence.
- [x] Probe transcripts persisted in `reports/` (shape claims traceable).
- [x] `Enabled()` recommendation stated for Phase 4.
- [x] Stop conditions honored — no card-required or unapproved keyed pick.

## Risk Assessment

Vendor docs may lag reality → every quota claim gets a live probe or an
explicit "claimed not verified" note. Public RPC rate limits may make the
keyless path infeasible → then the honest outcome is the keyed reportable
option + user approval, not a quiet downgrade.
