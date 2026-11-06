# Non-EVM TxProvider Discovery Brief (Solana + TRON)

Status: **TRON rows verified 2026-11-06 — decision:
`docs/tron-provider-decision.md` (TronGrid chosen; dormant adapter exists
under `internal/provider/trongrid/`, unwired per the §15 gate).** Solana
rows remain UNCONFIRMED — no adapter code exists for Solana and none is
authorized. Free-tier commercial-use terms and attribution requirements
for Solana candidates are still unverified.

## Contract requirements a provider MUST meet

To serve as a `TxProvider` (`internal/provider/provider.go:279-288`) for
solana or tron, an upstream must offer:

1. **Per-address transfer history covering native + token** — SOL plus SPL
   transfers (incl. inner instructions for token movement) for Solana; TRX
   plus TRC-20 transfers for TRON.
2. **Deterministic ordering usable for coverage watermarks** — our indexer
   stores contiguous coverage as a monotonic watermark
   (`wallet_index_state.indexed_until_block`); upstream must expose a
   comparable cursor (block height or slot) and emit transfers in
   ascending order within a stream.
3. **A typed head/slot endpoint** — equivalent of `HeadBlock(ctx, net)`:
   latest finalized slot / block for confirmation semantics.
4. **Pagination compatible with `TransferLookupOpts.PageToken`** — opaque
   continuation or offset cursor; bounded page sizes.
5. **Free tier without a credit card** — per AGENTS.md §7 no paid sources
   without approval; rate limits must coexist with our
   timeout/concurrency/retry contract.
6. **Terms permitting our use case** — free-tier commercial/derivative-use
   clauses verified per source before integration.

## Candidate table — TRON rows resolved 2026-11-06

| Candidate | Chain | Free quota (unverified) | Endpoint shape | Native coverage | Token coverage | Pagination semantics | Block/slot-range limits | Known quirks |
|---|---|---|---|---|---|---|---|---|
| Helius Enhanced Transactions | Solana | Free tier advertised (rate-limited; exact RPS/monthly cap UNCONFIRMED) | REST `GET /v0/addresses/{addr}/transactions` (parsed tx API) | SOL transfers + full tx set | SPL incl. parsed tokenTransfers | `before=<signature>` cursor | Not block-range; signature-ordered history — watermark mapping needs slot lookup per tx | Parsed (decoded) shape ≠ raw; signature ordering not slot-contiguous; needs slot→watermark bridge |
| **TronGrid — VERIFIED (chosen)** | TRON | VERIFIED: free key = 100K req/day + 3 keys + 15 RPS/key, email signup no card; keyless 3 RPS shared-IP dev-only (trongrid.me/price, BoltRPC 2026-10-24) | REST `GET /v1/accounts/{addr}/transactions[/trc20]` — live-probed keyless 200, 2026-11-06 | TRX txs w/ `blockNumber`+`block_timestamp` | `/transactions/trc20` — `transaction_id`/`from`/`to`/`token_info`/`value`/`type` (Transfer+Approval observed), **no block number** | `meta.fingerprint`+`meta.links.next`, `limit≤200` (201→400), absent on last page | `min/max_timestamp` ms, **inclusive both ends** (probed) | TRC-20 rows lack block numbers → per-row `gettransactioninfobyid` bridge (semantics doc); `getblockbynum(0)` elides number/timestamp |
| **Tatum — REJECTED (req 5)** | multi-chain | REJECTED: free plan is 100K **lifetime** credits at 5 credits/call ≈ 76 worst-case ensures total (docs.tatum.io plans-limits, 2026-11-06) | `GET /v3/tron/transaction/account/{address}` — TronGrid-shaped mirror, same block-number gap | TRON supported | TRC-20 via account tx endpoint (same shape) | `next` fingerprint param, ≤200 rows | `min/maxTimestamp`, asc/desc order | No added value over TronGrid; lifetime credit cap fails the calls-per-ensure model |
| **Ankr — REJECTED (req 1,5)** | multi-chain | REJECTED: Advanced API chain list has 16 EVM chains + Stellar — **no TRON** (ankr.com/docs/advanced-api/overview, 2026-11-06) | Free TRON = standard JSON-RPC only; no indexed account-history endpoint | TRON RPC node only | Cannot enumerate per-address TRC-20 | n/a | n/a | Even paid AAPI lacks TRON — not a tier problem |
| **Covalent / GoldRush — REJECTED (req 1)** | multi-chain | REJECTED: 47-chain list contains zero TRON (goldrush.dev/chains, 2026-11-06) | REST `GET /v1/{chain}/address/{addr}/transactions_v3/` (EVM chains only) | No TRON support | No TRON support | n/a | n/a | Chain-support failure precedes quota evaluation |

## Reading the table

- **Native + token in one stream**: Helius is the only candidate whose
  single response merges both for Solana; TronGrid needs stream merging
  (our `TransferPage.Streams` model already supports this pattern).
- **Watermark fit**: none of the candidates paginate by block/slot range
  natively — every integration must map (signature|fingerprint|page)
  cursors to our contiguous-coverage watermark; that mapping is the core
  adapter design problem and is deliberately undecided.
- **Integration path**: TronGrid verified + chosen for TRON
  (`docs/tron-provider-decision.md`, 2026-11-06). Helius remains the
  preliminary, unverified candidate for Solana (single merged feed).

## Next steps (all blocked on §15 review + verification)

1. TRON — done 2026-11-06: decision doc + semantics doc written, dormant
   adapter implemented (unwired). Remaining TRON enablement items are the
   §15 checklist in `docs/tron-indexer-semantics.md` §(f).
2. Solana — verify each candidate's current free-tier terms, quota, and
   commercial-use clause directly with the vendor docs/ToS.
3. Prove token-coverage semantics per candidate (SPL inner instructions).
4. Design the cursor→watermark mapping for Solana (slot vs block;
   commitment level).
