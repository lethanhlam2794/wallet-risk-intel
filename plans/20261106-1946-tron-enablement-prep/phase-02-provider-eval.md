---
phase: 2
title: "Provider Eval — verify TRON candidates against the 6 contract requirements"
status: completed
priority: P1
dependencies: [1]
---

# Phase 2: Provider Eval

## Overview

Verify each TRON candidate from `docs/non-evm-provider-discovery.md`
against the 6 `TxProvider` contract requirements using **current** vendor
docs (cite URL + retrieval date) and live probes where no key is needed.
Produce `docs/tron-provider-decision.md` and flip every TRON row in the
discovery table from UNCONFIRMED to VERIFIED or REJECTED.

## Requirements

- Functional: a decision document naming the chosen provider, the verified
  free-tier quota/limit evidence, and per-candidate rejection reasons.
- Non-functional: live probes must stay within free no-key/no-card
  surfaces; never pay for or embed an API key (stop condition).
- **Quota modeling (RA-5):** raw free-tier numbers are not enough — the
  decision doc MUST include worst-case **calls-per-ensure** arithmetic
  under the Phase-3 design (head call + per-step pages + block-bridge
  calls + TRC-20 fan-out bound by page size, under
  `MaxPagesHop0`/`MaxPagesHopN`/`MaxPagesHop3` tiers from
  `transaction/service.go:49-56`), and judge the free tier against that
  number.

## Architecture

### The 6 contract requirements (discovery doc §Contract)

1. Per-address transfer history covering **native + token** (TRX + TRC-20).
2. Deterministic ordering usable for coverage watermarks (ascending within
   a stream; comparable cursor for `indexed_until_block`).
3. Typed head endpoint ≈ `HeadBlock(ctx, net)` — **finalized/solidified**.
4. Pagination compatible with `TransferLookupOpts.PageToken` (bounded page
   sizes).
5. Free tier without a credit card; limits coexist with our
   timeout/concurrency/retry contract **and the calls-per-ensure model**.
6. Terms permitting our use case (commercial/derivative clauses checked).

### Candidates (TRON scope only — Solana rows out of scope)

| Candidate | Expected finding (to be re-verified) |
|---|---|
| **TronGrid** (api.trongrid.io) | Front-runner. Live-verified 2026-11-06 keyless: `walletsolidity/getnowblock` (solidified head), `/v1/accounts/{a}/transactions` (native, has `blockNumber`), `/v1/accounts/{a}/transactions/trc20` (events, **no block number** — `block_timestamp` only), `meta.fingerprint` cursor, `only_confirmed`, `order_by=block_timestamp,asc`, `min_timestamp`/`max_timestamp`. Must verify: free-key quota vs keyless shared limit, whether a free key needs a card, ToS commercial-use clause. |
| **Tatum** | Verify TRON account-history endpoint exists on free tier; credit-model quota math; TRC-20 coverage path. |
| **Ankr** | Free public RPC; account history enumeration likely needs paid Advanced API — verify. |
| **Covalent** | TRON support historically absent; verify current chain list. |
| **Out-of-table** (Tronscan, etc.) | Name explicitly why not evaluated (e.g. ToS prohibits API reuse, no free tier, scraping-only) — don't leave the gap silent (RA-10-adjacent). |

### Verified evidence already collected (carry into the decision doc)

- `GET https://api.trongrid.io/walletsolidity/getnowblock` → HTTP 200,
  `block_header.raw_data.number` (solidified head) — keyless, 2026-11-06.
- `GET /v1/accounts/{addr}/transactions?only_confirmed=true&limit=2
  &order_by=block_timestamp,asc` → HTTP 200; rows have `txID`,
  `blockNumber`, `block_timestamp`, `raw_data.contract[]`, `ret[]`;
  `meta.fingerprint` + `meta.links.next`. Keyless.
- `GET /v1/accounts/{addr}/transactions/trc20?only_confirmed=true` →
  HTTP 200; rows have `transaction_id`, `from`, `to`, `token_info`,
  `value`, `block_timestamp`, `type` — **no block number field**.
- TronGrid validates address checksum server-side (HTTP 400 on bad
  address) — adapters may forward malformed input errors verbatim.

### Mandatory probe list (RA-4/RA-5 — cheap, load-bearing)

Probe and record in the decision doc (keyless where possible):
- `order_by=block_timestamp,asc` honored on **both** account endpoints.
- Maximum `limit` value per endpoint (TronGrid advertises ~200 — verify).
- `min_timestamp`/`max_timestamp` **inclusivity** — probe a real
  `block_timestamp` as both bounds and observe boundary inclusion
  (determines double-count vs silent-exclusion at window edges).
- `meta.fingerprint`/`meta.links.next` behavior on an empty result page.
- `walletsolidity/gettransactioninfobyid` — `blockNumber` field present;
  note any observable indexing lag vs the trc20 event index.
- `walletsolidity/getblockbynum` — response shape +
  `block_header.raw_data.timestamp` presence (incl. `num:0` degenerate
  boundary behavior).
- Whether the native `/transactions` stream also surfaces pure TRC-20
  recipient txs (determines anchor-map viability).
- ToS commercial/derivative-use clause, with URL + retrieval date.

## Related Code Files

- Create: `docs/tron-provider-decision.md`
- Modify: `docs/non-evm-provider-discovery.md` (TRON rows → VERIFIED /
  REJECTED with evidence)
- Read: `docs/non-evm-provider-discovery.md`, `internal/provider/provider.go`,
  `internal/transaction/service.go` (budget/page tiers)

## Implementation Steps

1. Use a subagent (or focused web fetches) for vendor-doc verification so
   quota research stays out of main context — per source prompt Session
   Strategy.
2. For each candidate: check current docs for (a) endpoint existence,
   (b) free-tier quota and card requirement, (c) rate limits,
   (d) pagination/ordering params, (e) ToS commercial clause. Cite
   URL + retrieval date for every claim.
3. Run the mandatory probe list above; record outputs verbatim.
4. Compute the calls-per-ensure worst case and evaluate quota fitness
   under it — successful probes do NOT imply production fitness;
   keyless shared quota flakiness must be stated honestly (RA-5).
5. Apply the 6-requirement rubric per candidate; record VERIFIED or
   REJECTED (with the failing requirement number) in both the decision doc
   and the discovery table.
6. Write `docs/tron-provider-decision.md`: chosen provider, verified
   quota evidence + calls-per-ensure math, rejected alternatives,
   out-of-table exclusions, and the `Enabled()` gating recommendation —
   **expected: key-required**, matching chainscan
   (`adapter.go:150-152`) and nodereal precedent; blockscout's keyless
   path exists but TronGrid keyless shared quota is likely unfit for
   sustained indexing and key-required is defense-in-depth for a dormant
   adapter (RA-5).
7. **STOP CONDITION**: if no candidate meets all 6 on a free no-card
   tier, halt with `Status: BLOCKED` and report — do not pick a paid
   fallback unilaterally.

## Success Criteria

- [x] `docs/tron-provider-decision.md` exists with URL+date citations
- [x] Every TRON candidate row is VERIFIED or REJECTED — zero UNCONFIRMED
- [x] Chosen provider meets all 6 requirements on a free no-card tier
      **under the calls-per-ensure model**
- [x] Mandatory probe list results recorded verbatim
- [x] `Enabled()` gating recommendation recorded for Phase 5
- [x] Out-of-table candidates named with exclusion reasons
- [x] No key embedded anywhere; no paid tier used

## Risk Assessment

- Vendor docs may lag the live API — prefer live-probe evidence; cite both
  when they disagree.
- Keyless TronGrid rate limits are shared and may be flaky — the decision
  doc must state whether free-tier keyless is production-viable or a dev
  fallback only.
- Fan-out-heavy designs can flip the quota verdict — that's why the
  calls-per-ensure model is mandatory, not decorative.
