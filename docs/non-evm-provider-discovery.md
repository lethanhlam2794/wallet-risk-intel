# Non-EVM TxProvider Discovery Brief (Solana + TRON)

Status: **discovery only — no adapter code exists and none is authorized.**
Every candidate below is **UNCONFIRMED**: quotas, limits, and endpoint
shapes are transcribed from each vendor's public marketing/docs at a high
level and MUST be re-verified against current terms before any §15 review.
Free-tier commercial-use terms and attribution requirements are
specifically unverified.

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

## Candidate table — ALL UNCONFIRMED

| Candidate | Chain | Free quota (unverified) | Endpoint shape | Native coverage | Token coverage | Pagination semantics | Block/slot-range limits | Known quirks |
|---|---|---|---|---|---|---|---|---|
| Helius Enhanced Transactions | Solana | Free tier advertised (rate-limited; exact RPS/monthly cap UNCONFIRMED) | REST `GET /v0/addresses/{addr}/transactions` (parsed tx API) | SOL transfers + full tx set | SPL incl. parsed tokenTransfers | `before=<signature>` cursor | Not block-range; signature-ordered history — watermark mapping needs slot lookup per tx | Parsed (decoded) shape ≠ raw; signature ordering not slot-contiguous; needs slot→watermark bridge |
| TronGrid | TRON | Free API key tier advertised (quota UNCONFIRMED) | REST `GET /v1/accounts/{addr}/transactions[/trc20]` | TRX transactions | Separate `/transactions/trc20` stream | `fingerprint` cursor + `limit` | `min_timestamp`/`max_timestamp` (ms) not block range | Native + TRC-20 are *separate* streams — must merge like our existing multi-stream model; TRC-20 stream lacks TRX |
| Tatum | multi-chain | Free tier advertised (request-rate + monthly credit model, UNCONFIRMED) | REST/JSON-RPC gateway | TRON supported; Solana address-history endpoint support UNCONFIRMED | TRC-20/SPL via indexed endpoints (UNCONFIRMED) | Varies per endpoint | UNCONFIRMED | Credit-based quota math per call type; data-plane vs gateway split needs scoping |
| Ankr | multi-chain | Free public RPC endpoints advertised (rate limits UNCONFIRMED); Advanced APIs paid | JSON-RPC + Advanced API | Raw `getSignaturesForAddress` (Solana) / Trongrid-style TRON | SPL via parsed tx or getTokenAccounts; TRC-20 via contract logs | Signature cursor (Solana RPC) | `until`/`before` signature cursors, not slot ranges | Public RPC cannot enumerate token transfers per address without per-tx parse fan-out — likely needs paid Advanced tier |
| Covalent | multi-chain | Free tier advertised (UNCONFIRMED — tier terms changed historically) | REST `GET /v1/{chain}/address/{addr}/transactions_v3/` | Native tx list | Token transfers via events subset | `page` number + `page-size` | Recent-history oriented; deep-history window UNCONFIRMED per chain | Solana support level UNCONFIRMED (EVM-first vendor); TRON unsupported historically |

## Reading the table

- **Native + token in one stream**: Helius is the only candidate whose
  single response merges both for Solana; TronGrid needs stream merging
  (our `TransferPage.Streams` model already supports this pattern).
- **Watermark fit**: none of the candidates paginate by block/slot range
  natively — every integration must map (signature|fingerprint|page)
  cursors to our contiguous-coverage watermark; that mapping is the core
  adapter design problem and is deliberately undecided.
- **Cheapest integration path (preliminary, unverified)**: TronGrid for
  TRON (purpose-built account APIs) and Helius for Solana (single merged
  feed) — pending quota verification.

## Next steps (all blocked on §15 review + verification)

1. Verify each candidate's current free-tier terms, quota, and
   commercial-use clause directly with the vendor docs/ToS.
2. Prove token-coverage semantics per candidate (SPL inner instructions;
   TRC-20 log decoding) on a live free-tier key.
3. Design the cursor→watermark mapping per chain (slot vs block;
   commitment level on Solana).
4. Only then draft an adapter behind `TxProvider` — no code before this.
