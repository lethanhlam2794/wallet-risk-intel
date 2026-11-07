# Non-EVM TxProvider Discovery Brief (Solana + TRON)

Status: **TRON rows verified 2026-11-06 — decision:
`docs/tron-provider-decision.md` (TronGrid chosen; dormant adapter exists
under `internal/provider/trongrid/`, unwired per the §15 gate). Solana
rows verified 2026-11-07 — decision: `docs/solana-provider-decision.md`
(keyless public Solana JSON-RPC chosen; dormant adapter pending under
`internal/provider/solanarpc/`, unwired per the §15 gate).**

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
| Helius Enhanced Transactions | Solana | VERIFIED (keyed-free, reportable — NOT chosen): 1M credits/mo, 10 RPS RPC, 2 RPS Enhanced APIs, no card (helius.dev/docs/billing/*, 2026-11-07). Enhanced Tx = 100 credits/req, in maintenance mode | REST `GET /v0/addresses/{addr}/transactions` (parsed tx API) | SOL transfers + full tx set | SPL incl. parsed tokenTransfers — solves owner-feed SPL-discovery gap | `before=<signature>` cursor | Not block-range; signature-ordered history — watermark mapping needs slot lookup per tx | Parsed (decoded) shape ≠ raw; signature ordering not slot-contiguous; needs slot→watermark bridge; keyed pick needs user approval |
| **Public Solana JSON-RPC — VERIFIED (chosen)** | Solana | VERIFIED: keyless, no card; 100 req/10s/IP total, 40/10s per method, 40 conn, 100MB/30s; 429 JSON-RPC error on exceed (solana.com/docs/references/clusters + live probe, 2026-11-07) | JSON-RPC `getSignaturesForAddress` + `getTransaction`/`getBlock`/`getSlot` — live-probed 2026-11-07 | SOL system transfers via parsed system ixs | SPL via parsed `transfer`/`transferChecked` + tokenBalances — **proven owner-feed blind spot for incoming ATA transfers** (probe §7) | `before`/`until` sig cursors, both exclusive, `before` accepts foreign-sig positional seed | desc by (slot,transactionIndex) — probed; ≤1000/page, >1000 → -32602 | `transactionIndex` field present; v0 txs need `maxSupportedTransactionVersion`; `processed` rejected -32602; `getBlockSignatures` -32601 on public endpoint; genesis-era `blockTime:null` |
| PublicNode Solana | Solana | VERIFIED keyless live 2026-11-07 — same JSON-RPC shapes incl. `transactionIndex` | JSON-RPC identical surface | same as public RPC | same owner-feed gap | same cursors | `getFirstAvailableBlock`≈465.6M ≈ ~3-day retention — **truncated** | Mirror/failover only inside its retained window |
| Ankr | multi-chain | REJECTED (req 1): Advanced API = 16 EVM chains + Stellar — no Solana index; Solana standard RPC only, ~16h retention, keyed (ankr.com docs, 2026-11-07) | keyed JSON-RPC | n/a | n/a | n/a | n/a | Same no-history failure as its TRON verdict |
| Covalent / GoldRush | multi-chain | REJECTED (req 1): Solana Foundational REST serves NO transactions or SPL transfer history — warehouse-only (goldrush.dev solana SKILL doc, 2026-11-07) | REST | none | none | n/a | n/a | Explicit doc disclaimer |
| Tatum | multi-chain | REJECTED (req 5): 100K lifetime credits (TRON eval 2026-11-06) — quota model fails identically | REST | — | — | — | — | Lifetime cap |
| QuickNode | multi-chain | REJECTED (req 5): free = 1-month TRIAL (10M credits, 15 RPS, no card) then deactivated (quicknode.com/pricing, 2026-11-07) | keyed JSON-RPC | RPC only | RPC only | n/a | n/a | Trial ≠ durable free tier |
| Solscan Pro API | Solana | REJECTED (req 1,5): `account/transactions` + `account/transfer` are PRO endpoints — paid from $49/mo; free key = core endpoints only (pro-api.solscan.io docs, 2026-11-07) | REST keyed | partial | paid only | `before` sig cursor, limit≤40 | n/a | History gated behind paywall |
| **TronGrid — VERIFIED (chosen)** | TRON | VERIFIED: free key = 100K req/day + 3 keys + 15 RPS/key, email signup no card; keyless 3 RPS shared-IP dev-only (trongrid.me/price, BoltRPC 2026-10-24) | REST `GET /v1/accounts/{addr}/transactions[/trc20]` — live-probed keyless 200, 2026-11-06 | TRX txs w/ `blockNumber`+`block_timestamp` | `/transactions/trc20` — `transaction_id`/`from`/`to`/`token_info`/`value`/`type` (Transfer+Approval observed), **no block number** | `meta.fingerprint`+`meta.links.next`, `limit≤200` (201→400), absent on last page | `min/max_timestamp` ms, **inclusive both ends** (probed) | TRC-20 rows lack block numbers → per-row `gettransactioninfobyid` bridge (semantics doc); `getblockbynum(0)` elides number/timestamp |
| **Tatum — REJECTED (req 5)** | multi-chain | REJECTED: free plan is 100K **lifetime** credits at 5 credits/call ≈ 76 worst-case ensures total (docs.tatum.io plans-limits, 2026-11-06) | `GET /v3/tron/transaction/account/{address}` — TronGrid-shaped mirror, same block-number gap | TRON supported | TRC-20 via account tx endpoint (same shape) | `next` fingerprint param, ≤200 rows | `min/maxTimestamp`, asc/desc order | No added value over TronGrid; lifetime credit cap fails the calls-per-ensure model |
| **Ankr — REJECTED (req 1,5)** | multi-chain | REJECTED: Advanced API chain list has 16 EVM chains + Stellar — **no TRON** (ankr.com/docs/advanced-api/overview, 2026-11-06) | Free TRON = standard JSON-RPC only; no indexed account-history endpoint | TRON RPC node only | Cannot enumerate per-address TRC-20 | n/a | n/a | Even paid AAPI lacks TRON — not a tier problem |
| **Covalent / GoldRush — REJECTED (req 1)** | multi-chain | REJECTED: 47-chain list contains zero TRON (goldrush.dev/chains, 2026-11-06) | REST `GET /v1/{chain}/address/{addr}/transactions_v3/` (EVM chains only) | No TRON support | No TRON support | n/a | n/a | Chain-support failure precedes quota evaluation |

## Reading the table

- **Native + token in one stream**: Helius is the only candidate whose
  single response merges both for Solana — and the only one that closes
  the owner-feed SPL-discovery gap via provider-side owner indexing.
  The chosen keyless RPC carries that gap as a declared coverage limit
  (`docs/solana-indexer-semantics.md` §h). TronGrid needs stream merging
  (our `TransferPage.Streams` model already supports this pattern).
- **Watermark fit**: none of the candidates paginate by block/slot range
  natively — every integration must map (signature|fingerprint|page)
  cursors to our contiguous-coverage watermark; that mapping is the core
  adapter design problem. Solana's desc-signature→ascending-window bridge
  is decided in `docs/solana-indexer-semantics.md` §b.
- **Integration path**: TronGrid verified + chosen for TRON
  (`docs/tron-provider-decision.md`, 2026-11-06). Public Solana JSON-RPC
  verified + chosen for Solana (`docs/solana-provider-decision.md`,
  2026-11-07); Helius keyed-free is the documented upgrade path if
  SPL-incoming coverage becomes required (user-approval gate).

## Next steps (all blocked on §15 review + verification)

1. TRON — done 2026-11-06: decision doc + semantics doc written, dormant
   adapter implemented (unwired). Remaining TRON enablement items are the
   §15 checklist in `docs/tron-indexer-semantics.md` §(f).
2. Solana — done 2026-11-07: provider decision + probe evidence written;
   semantics doc + dormant adapter in progress (same plan). Remaining
   Solana enablement items are the §15 checklist in
   `docs/solana-indexer-semantics.md` §(f)-equivalent.
