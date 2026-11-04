# Rebuttal — Round 1 → plan amendments applied

Critic verdict CHALLENGED (3 blockers, 8 majors, 6 minors, 4 nits).
All findings verified accurate; **all 21 accepted** — the provider
premise B1 was factually dead and the plan is re-sourced accordingly.
Amendments below are already written into `plan.md`/phase files.

## B1 — BSC re-sourced; per-chain ENABLED matrix (accepted)

- V1 endpoints removed; adapter targets **`api.etherscan.io/v2/api` with
  `chainid=<net.ChainReference>` + ONE `ETHERSCAN_API_KEY`**.
- `Enabled(net)` is per-chain: `chainid=56` (BSC) is Paid-Tier-Only on
  the Etherscan free tier → chainscan adapter reports `bsc` DISABLED —
  never a fake index.
- **BSC gets its own adapter**: NodeReal MegaNode free tier
  `bsc-mainnet.nodereal.io`, JSON-RPC `nr_getAssetTransfers` (category
  `EXTERNAL`/`ERC20`; spec §4.7 "other providers with verified free
  tiers"; free-tier dev/non-commercial use stated per AGENTS §5 last
  rule). Same `TxProvider` seam; `NODEREAL_API_KEY` empty ⇒ DISABLED.
- If the NodeReal free tier cannot be verified during Cook, the plan
  escalates instead of faking BSC coverage — `provider_status` must
  report the real upstream anyway.
- ToS stated per source: Etherscan free = rate-limited dev tier
  (commercial needs paid plan — MVP dev use only); NodeReal free tier =
  rate-limited dev tier.
- Test added: per-network `Enabled()` matrix asserted.

## B2 — namespaced `transfer_index` vocabulary (accepted)

`{kind}:{ordinal}` deterministic per fetched window:
`n:{i}` NATIVE (i = transfer ordinal within tx, normally 0);
`t:{i}` TOKEN (ordinal within the same-tx transfer group, computed over
the complete fetched window BEFORE upsert — windows are contiguous and
deterministic under the ascending watermark, so ordinals are stable
across refetches); `i:{traceId}` INTERNAL.
Collision rule: byte-identical repeat transfers in one tx get distinct
ordinals — both rows survive (test). Multi-transfer txs and page-edge
txs handled by "compute ordinals over the whole fetched window, then
upsert once" — never per-page.

## B3 — ascending watermark + 10k cap (accepted)

`indexed_until_block` = highest **contiguously** indexed block
(ascending sort, `startblock=watermark+1&endblock=head` windowed paging
inside `max_pages`; the Etherscan-family 10k record cap is stated and
handled — a window that hits it splits by block range, never by guess).
Stale refresh continues `(watermark+1 .. head]`; first index of a busy
wallet can hit `max_pages` before reaching head →
`history_truncated=true` + coverage reports `{rows_indexed,
window_covered, status}` — partial is partial, never complete.

## Majors M4–M11 — all accepted

- **M4**: `/transactions` envelope gains `{index_status,
  provider_status, indexed_until_block, indexed_at, history_truncated,
  next_cursor}`.
- **M5**: `EnsureIndexed` singleflights on `(network_id, wallet_id)`
  (in-process now; cross-process advisory lock deferred + documented).
- **M6**: bound arithmetic published — calls-per-EnsureIndexed =
  endpoints×pages; effective node bound = `⌊max_provider_requests /
  calls⌉`; precedence `deadline > max_provider_requests > max_nodes >
  max_edges > response_cap`; closed truncated-reason enum
  `{max_nodes, max_provider_requests, max_edges, deadline,
  response_cap}`; hop-2 EnsureIndexed runs bounded-concurrent (4);
  hop-2 nodes get `max_pages_hop2` (smaller, default 2).
- **M7**: bulk counterparty resolution — `INSERT … SELECT unnest()` ON
  CONFLICT → address→id map for the batch; provider addresses normalized
  via `wallet.NormalizeEVM` first; counterparty wallet rows documented
  as first-class rows (addressable by other endpoints — acknowledged
  bloat, MVP-acceptable).
- **M8**: FAILED retry allowed under 60 s failure cooldown (enrichment
  negative-TTL convention); coverage reports partial honestly.
- **M9**: screenings stay provider-reported-only in v1 →
  `coverage.tracing` becomes `"provider_reported_only"` (documented
  enum + OpenAPI update) — graph exposures are a separate surface
  (`/exposures`), never silently re-basis'd into scoring.
- **M10**: counterparty sanctions match reuses
  `sanctions.Service.LookupWallet` three-bucket semantics — only the
  same-network direct bucket yields `SANCTIONED`; cross-chain and
  unresolved listings are reported as `cross_chain_listing` category on
  a distinct flag, never direct.
- **M11**: self-edges excluded; path identity = node sequence;
  per-pair edge selection = earliest block_timestamp then smallest
  amount_raw (deterministic); evidence dedup key `(tx_hash,
  transfer_index)`.

## Minors m12–m17 — all accepted

- `temporal_order_valid` = hop edges non-decreasing block_timestamp in
  path order; `path_verified` = constant `false` (documented deliberate —
  MVP cannot prove same-asset continuity), never emitted true.
- `path[]` = ordered edges per path; `transaction_evidence` = flat
  deduped union across the item's paths — contract documented.
- `refresh=1` moved to §6 deviations.
- `confirmation_status` vocabulary defined: `confirmations>0 →
  CONFIRMED`, else `PENDING`; `isError=1` mapped to transfer-level
  `status` note — never conflated with confirmation state; reorg
  posture: head-window revalidation on reindex (re-fetch covers the
  watermark tail; documented residual: deeper reorgs not handled in
  MVP).
- Cursor: opaque base64 keyset `(block_timestamp, id)` + `next_cursor`,
  `INVALID_CURSOR` reuse; response byte cap ~256 KB documented;
  `direction` filter applies to the hop-1 edge direction (in/out vs
  root) — documented.
- E2E injects fixture-backed `TxProvider` through `httpserver.Deps` —
  never live upstream; stated explicitly.

## Nits n18–n21 — all accepted

- `max_pages` defaults stated: hop-0 5, hop-2 2; page_size 10k-legal
  (offset 1000).
- `wallet_index_state` PK `(network_id, wallet_id)` kept (wallet_id is
  already globally unique — stated as harmless symmetry, simplifies
  joins).
- Provenance: `data_sources` registration for the tx source per
  `ingestion_runs.source_id` convention.
- Exposure items = matched counterparties only (labels/sanctions); the
  raw counterparty list is not the response — stated explicitly.
