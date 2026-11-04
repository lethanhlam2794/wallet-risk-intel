# Plan — Phase 6: Transaction Tracing

status: planning
spec: `docs/tailieu.md` §4.7, §7.13, §9.9–9.10, §11, §19 (phase 6)
predecessor: `plans/20261103-2318-risk-scoring/` (APPROVED, complete)
critic: 9 rounds → ~125 findings applied → **PLAN_CONSENSUS_REACHED** (reports/)

## Goal

Transaction-based wallet exposure analysis: index wallet transfers
(native + ERC-20/BEP-20), serve indexed transactions, and run bounded
BFS exposure analysis (1-hop/2-hop only) returning transaction evidence
and honest coverage metadata — keeping direct attribution, direct
exposure, and indirect exposure strictly separated; a graph path never
proves ownership or identical fund flow.

## §1 Scope

In scope:
- `TxProvider` seam (`internal/provider/provider.go`) + 2 adapters:
  `chainscan` (Etherscan V2, ethereum) and `nodereal`
  (`nr_getAssetTransfers`, bsc).
- On-demand lazy indexing: `/transactions` and `/exposures` fetch
  upstream per wallet (ascending watermark), upsert into
  `transactions`, serve from DB.
- `transactions` table per spec §7.13 (+ declared `is_error` column,
  §6) + `wallet_index_state` for coverage honesty.
- Bounded BFS: hop 1 and hop 2 only (`max_hops≤2`), max_nodes,
  max_edges, max_provider_requests, query timeout, response cap (§11.4).
- `GET /wallets/{network}/{address}/transactions` (§9.9 filters).
- `GET /wallets/{network}/{address}/exposures` (§9.10 response fields).
- §11.5 evidence fields per edge; coverage per request.
- Metrics: `tracing_nodes_examined_total` + reuse existing
  `provider_requests_total`/`provider_errors_total` families +
  new `provider_request_duration_seconds` (no duration family exists
  today — added, not "reused").

Out of scope (Phase 7, separately approved): 3+ hop, cross-chain
tracing. Declared-out at any phase: INTERNAL transfers (see §6),
NFT categories (721/1155), automatic enforcement decisions.

## §2 Invariants

- 1-hop ≠ ownership; a graph path never proves shared ownership or
  identical fund flow (§11.1). `relationship` vocabulary:
  `direct_counterparty` (hops=1) vs `indirect_hop` (hops=2).
- Provider failure → typed status + coverage honesty, never a
  clean-looking empty index.
- Index completeness is never claimed without evidence:
  `{index_status, provider_status, indexed_until_block,
  history_truncated}` on the wire; partial is partial.
- `amount_raw` NUMERIC integer — never float.
- TOKEN rows must carry `asset_identifier` (contract); rows missing it
  are skipped + counted in coverage (never silently dropped).
- Reorg: `confirmation_status` is a mutable column (revalidated upsert);
  deeper-reorg residual is documented, not hidden.
- Upsert identity `(network_id, tx_hash, transfer_index)` with the
  content-derived vocabulary §3.4 — `ON CONFLICT` never silently drops
  a real transfer, and a byte-identical repeat transfer inside one tx
  is a distinct row by `.{k}` suffix; a wallet-scoped fetch can never
  see another wallet's transfers in the same tx, so identity must not
  depend on fetch context.
- Provider-reported exposure (Phase 5, `basis=provider_reported`) stays
  separate from graph-derived exposure (`/exposures`,
  `tracing_method=bounded_bfs`) — never folded into scoring.
- Only label/sanctions-matched counterparties become exposure items;
  sanctions matching reuses the three-bucket semantics — same-network
  direct bucket only for `SANCTIONED`.

## §3 Design decisions

### 3.1 Providers — per-chain adapters behind `TxProvider`

```
LookupTransfers(ctx, net, normalized, opts{
  FromBlock, ToBlock int64; Kinds []string; PageToken string;
  PageSize int}) (*TransferPage, error)
```

`PageToken` is opaque; empty = first page. `TransferPage` carries
`NextPageToken` (empty = done).

| adapter | upstream | key | chains |
|---|---|---|---|
| `chainscan` | `api.etherscan.io/v2/api?chainid=<net.ChainReference>` (V2 unified; V1 + bscscan.com dead) — endpoints `txlist` + `tokentx` (INTERNAL out of scope, §6) + `eth_blockNumber` | `ETHERSCAN_API_KEY` | enabled per `config.enabled_chains` — default `[ethereum]`; `bsc` DISABLED by default (chainid 56 is paid-tier-only) — config-overridable for paid keys |
| `nodereal` | `bsc-mainnet.nodereal.io/v1/{key}` JSON-RPC `nr_getAssetTransfers` (categories `"external"`, `"20"`; `excludeZeroValue=true`) + `eth_blockNumber` | `NODEREAL_API_KEY` | `bsc` default; empty key ⇒ DISABLED |

Both adapters wrap `internal/provider/resilience` `NewClient` (timeout,
per-adapter limiter — chainscan ~3/s, nodereal conservative 0.6/s —
semaphore, breaker, bounded retry). `PageToken` encodes both direction
cursors `{fromKey,toKey}` (chainscan: int page per stream); a finished
stream's side drops out; empty = first page; `NextPageToken` empty =
done. `max_pages` counts **combined stream-pair steps** (one step = the
paired from/to calls needed to advance a window); the head-fetch counts
toward `max_provider_requests` like every call.

Facts verified (NodeReal docs, 2026-11-04): `nr_getAssetTransfers` costs
**250 CU/call**; free tier 150–300 CUPS (docs disagree — take
conservative 150 ⇒ **~0.6 calls/s sustained**, 1.2 optimistic);
`toBlock − fromBlock ≤ 100,000` blocks hard cap; `pageKey` UUID cursor +
`maxCount ≤ 0x3E8` (1000); separate `fromAddress`/`toAddress` direction
streams (2 calls per window); response has no `traceId`/`logIndex`/
confirmations (`receiptsStatus` = exec flag; confirmations computed
`head − blockNum + 1`); tolerant parse of `blockTimeStamp`/
`blockTimestamp`. ToS: both free tiers are rate-limited dev/
non-commercial tiers — AGENTS §5 gate satisfied; no paid provider added
(§15 untouched). If NodeReal's free tier proves unverifiable during
Cook, the plan **escalates** instead of faking BSC coverage.

Per-adapter `calls_per_index` (published):
chainscan `2 endpoints × pages + 1 head`; nodereal `2 direction streams
× pages + 1 head`. Sustained rates: chainscan ~3 req/s; nodereal
~0.6–1.2 calls/s ⇒ **BSC hop-2 is deadline-bound by design** — the
plan accepts `truncated=deadline` as the normal BSC outcome at free
tier and declares it; per-adapter deadline/budget overrides are config
knobs (`bsc: max_provider_requests=8` default). Monthly quota note:
nodereal 10M CU ÷ 250 ≈ 40k calls/month — hop-2 indexing burns it
measurably.

### 3.2 Lazy indexing — ascending watermark + singleflight

`EnsureIndexed(ctx, net, normalized, force)` singleflights on
`(network_id, wallet_id)` (`singleflight.Group`, enrichment convention;
cross-process advisory lock deferred + documented):
- Fresh index-state — `status ∈ {fresh, partial}` ∧ `indexed_at` ≤
  `IndexingFreshTTL` (default 10 m — named config key; not
  `EnrichmentCacheTTL`; FAILED ⇒ 60 s cooldown per
  `EnrichmentNegativeTTL`; `refresh` bypasses freshness **only** — the
  cooldown still holds so refresh loops cannot hammer a dead provider)
  and no `refresh` → serve DB only. `indexing` rows never satisfy the
  gate: within `IndexingStaleAfter` an in-process caller joins via
  singleflight (same `(network_id,wallet_id)` key); past it the row is
  retryable (sized vs bounded EnsureIndexed duration — **not**
  `EnrichmentDeadline`, a BSC index legitimately outlives 25 s).
  `not_indexed` always takes the fetch path. `indexed_at` = the last
  EnsureIndexed **terminal** write (`fresh|partial|failed|disabled`),
  written with `status`/`error` in one upsert — it is simultaneously
  the FAILED-cooldown clock and the `stale` clock; NULL ⇒ `stale=true`.
  `provider_status` therefore only ever appears on rows with
  `status ∈ {fresh,partial,failed,disabled}` — the rule is total.
  `status=disabled` re-evaluates `Enabled()` per request — enabled now
  ⇒ normal indexing path (keys are env-static; zero calls, zero cost).
- Else fetch inclusive `[fetch_from .. fetch_to]` where
  `fetch_from = max(0, watermark − reorg_tail + 1)` and
  `fetch_to = min(head, fetch_from + window_cap)` — `window_cap`
  bounds the **request span** `fetch_to − fetch_from` (nodereal hard
  cap 100k; chainscan ∞ ⇒ `fetch_to = head`), so the tail consumes
  forward reach (BSC effective advance ≈ 99,901 blocks/window; the
  ~1300-call convergence arithmetic stands at this precision). The
  `reorg_tail` overlap (per-chain config: ETH 64 / BSC 100 blocks) is
  what revalidates the mutable columns via DO UPDATE; rows in the
  re-covered tail absent from the refetch are **left as-is** —
  deep-reorg residual declared in §6. `head < fetch_from` (RPC
  node-lag or >tail reorg) ⇒ **no upstream call** — serve persisted
  facts; `head_block` on the wire makes the condition self-describing
  (`head_block < indexed_until_block`; no status transition). Bounded by `max_pages` (hop-0 5, hop-2 2,
  counted in stream-pair steps). Watermark advance = **min over all
  streams' contiguous coverage** (a page boundary mid-block ⇒
  watermark = boundary_block − 1; an empty/finished stream is
  complete-**to `fetch_to`**, not coverage 0), applied only after
  the batch commits — crash-safe ordering.
- Provider DISABLED ⇒ `status=disabled`, `error='disabled'` (ErrKind
  exists), zero upstream calls — never a FAILED cooldown it did not
  earn. Typed failure ⇒ `status=failed` + ErrKind. `error` column is
  cleared-on-success. Previously-indexed rows always still served.
- Cold-start convergence arithmetic: a never-indexed BSC wallet needs
  ~`head/100k × 2 directions` ≈ **~1300 calls** just to converge windows
  (~20–40 min of limiter wait) — the posture is **lazy incremental
  convergence**: each request advances windows until `max_pages`/
  deadline, `history_truncated=true` says the truth, later requests
  continue. `history_truncated` = "fetch stopped on a bound", distinct
  from "indexed_until < advancing head". Single-block-overflow edge: a
  block exceeding the record budget cannot split below block
  granularity — declared residual.
- Provider switch: `index_state.source_id != current` ⇒ reset watermark
  (re-index lazily) — never silently trust another adapter's watermark.
- `data_sources`: one row per adapter (per-adapter provenance);
  `wallet_index_state.source_id` is the FK (`source_type='api'` —
  the established convention for on-demand providers, 000006).

### 3.3 Bounded BFS (`internal/tracing`)

1. Hop-0 `EnsureIndexed`. `max_provider_requests` is a
   **request-global pool** shared across hop-0 + every hop-2 index.
2. Hop-1 counterparties from DB — edge queries filter `is_error =
   false` (a reverted tx moved nothing: no counterparty, no exposure
   item; the row still lists on /transactions with the flag —
   forensic value). Self-edges excluded. Deterministic expansion
   order `(earliest edge block_timestamp, tx_hash,
   normalized_address)`. **Bounds govern hop-2 expansion positions
   only** — hop-1 items are unaffected by max_nodes/budget (their
   evidence is local to the root index, zero provider calls). Every
   hop-1 counterparty is an expansion position regardless of match
   status (clean intermediaries are exactly what hop-2 exists to
   reach through) — except sentinel addresses `0x0`/`0x…dead` (burn/
   mint), which are excluded from expansion but still list on
   /transactions — **sentinels DO emit items when matched** (they are
   counterparties, just not expansion positions; `counterparties_total`
   excludes them regardless). *Resolved* = index attempt + edge scan + match
   complete; a FAILED/DISABLED index resolves with empty contribution
   + `nodes_failed++`.
3. Hop-2 `EnsureIndexed` per node (bounded concurrency 4). Workers
   pull positions **strictly in the declared order**; the assembler
   emits the **maximal contiguous prefix of resolved positions** — a
   FAILED/DISABLED node counts as resolved (lands in `nodes_failed`,
   does not break contiguity); results resolved beyond the first
   unresolved position are computed but excluded from items — never
   silently dropped. Edges terminating at root or self-looping at any
   hop are excluded. `max_edges` is a request-global running count
   across all edge scans, truncated mid-scan in declared edge order
   (per-node scan `(block_timestamp, tx_hash, transfer_index)`, node
   order = expansion order).
   Ascending-index bias declared: a hop-2 node's discovered edges are
   its *earliest* history when convergence is incomplete —
   `node_indexed_until` carries the honesty.
4. Bound precedence `deadline > max_provider_requests > max_nodes >
   max_edges > response_cap > limit` (limit is a shaping cap — hard
   bounds win the reported reason); closed truncated-reason enum
   `{deadline, max_provider_requests, max_nodes, max_edges,
   response_cap, limit}`. Pipeline order pinned: assemble →
   `category` filter (over all `matched_signals`) → deterministic
   order (hops asc, then expansion order) → `limit`/`response_cap`
   cut at item boundary.
5. Item contract (matched counterparties only): `counterparty,
   category, matched_signals[], relationship, hops, path[],
   transaction_evidence[]`. `category` = highest-precedence match:
   `SANCTIONED` (same-network direct bucket only) > label categories
   (intra-tier: `category_type` order MALICIOUS_ACTIVITY >
   SERVICE_TYPE > ENTITY_TYPE > OTHER, tiebreak category code asc) >
   `cross_chain_listing` (cross-chain/unresolved-network listing —
   informational, emits an item even when it is the only match;
   classification-only matches like MIXER/GAMBLING also emit items —
   informational, consistent with the cross-chain rule).
   `matched_signals[]` entries = `{kind: sanctions_direct|label|
   listing, category, label?, entity?, programs[]?, source,
   verification_status?, network?, listed_address?}` — per-kind
   applicability: `sanctions_direct` → entity + programs[] + source
   (OFAC); `label` → label + category + entity? + source +
   verification_status; `listing` → network + listed_address +
   source (the listing's actual provenance). Field shapes: `entity`
   = `{id, name}`; `programs[]` elements = `{code, name}`; `source`
   = `data_sources.code` string. `category` filtering
   matches **all signals**, not only the emitted category. A
   `category=SANCTIONED` item is distinguished by `kind`
   (`sanctions_direct` vs label-kind sanctions claims — same
   provenance convention as `scoring.Kind`); `LEGAL` ranks first in
   the label intra-tier order (LEGAL > MALICIOUS_ACTIVITY >
   SERVICE_TYPE > ENTITY_TYPE > OTHER). Label-matched items
   propagate `verification_status` (reported stays reported).
6. `path[]` per path entry: `hop_count`, `path_verified` (constant
   `false`), `temporal_order_valid`, `edges[]` — each edge §11.5:
   `{from_address, to_address, network, tx_hash, transfer_index,
   transfer_type, asset (`"native"` for NATIVE rows — aligns with the
   filter vocabulary), amount_raw, block_number, block_timestamp,
   confirmation_status}` — `transfer_index` on the wire makes
   identical-content copies distinguishable (declared §6); path
   identity = node sequence over normalized address strings; edge
   pick = one picked edge per hop position along the sequence
   (earliest block_timestamp, then smallest amount_raw, final tiebreak
   `(tx_hash, transfer_index)`); `max_paths_per_pair=3` keeps the 3
   lexicographically-first node sequences. `transaction_evidence` =
   flat deduped union across the item's paths, dedup key
   `(tx_hash, transfer_index)`; `tx_hash` stored lowercase (UNIQUE key
   is case-sensitive).
7. `temporal_order_valid` = hop edges non-decreasing block_timestamp
   along path order.
8. Envelope `coverage` = `{counterparties_total (expansion positions
   after sentinel exclusion; `max_hops=1` ⇒ `nodes_examined`=
   `positions_emitted`=`nodes_failed`=0, `nodes_remaining`=
   `counterparties_total`),
   nodes_examined (all attempted incl. discarded beyond-prefix),
   positions_emitted (contiguous resolved-prefix length),
   nodes_remaining (never attempted; examined+remaining =
   counterparties_total), nodes_failed, edges_examined,
   provider_calls, provider_status (root EnsureIndexed outcome —
   ErrKind or `"ok"`; node failures live in nodes_failed),
   sanctions_snapshot_version + labels_snapshot_version (each null
   when none published; a mid-request publish yields bounded drift
   vs per-node matches — declared), node_indexed_until{
   normalized_address→block|null} **for attempted positions only**
   (never-attempted positions are nodes_remaining, not map
   entries), `head_block` (**the root wallet's observed head this
   request** — null when the root made no head call; per-node heads
   remain internal, `node_indexed_until` carries per-node honesty),
   root{index_status, stale, provider_status, indexed_until_block,
   indexed_at, history_truncated, index_scope, rows_indexed,
   rows_skipped{missing_contract, invalid_address}}}` +
   `truncated` (reason enum) + `tracing_method="bounded_bfs"`
   (envelope level — §11.5 path-level `data_coverage`/`truncated`
   collapse into the envelope; declared deviation). `direction`
   values `in|out` (default both) = hop-1 edge direction;
   `max_hops` default 2, `=1` ⇒ hop-1 items only (no expansion:
   `nodes_examined`=`positions_emitted`=`nodes_failed`=0,
   `nodes_remaining`=`counterparties_total` — positions exist but
   expansion is skipped), `=0` or `>2` → 400; `direction` filters the
   hop-1 edge set **before** position derivation (positions derive
   from filtered edges); `category` filter post-assembly (matches all
   `matched_signals`); `refresh=1` applies to **every** EnsureIndexed
   performed this request (consistent semantics; FAILED cooldown
   still holds; quota consequence on BSC declared); items emitted in
   deterministic order (hops asc, then **min contributing position
   index** — a sentinel item, which never becomes a position, uses
   its own rank in the hop-1 expansion ordering as its index —
   then `counterparty` normalized_address asc — a total
   key; hop-2 items sharing a position must still order
   deterministically);
   `limit` ≤100 (default 50 on both endpoints) caps emitted items
   with `truncated=limit`; `truncated` is `null` when untruncated
   (closed enum stays closed); deadline truncation emits valid JSON
   cut at item boundary.
9. Emitted items **dedupe by counterparty** (normalized address)
   across hop depths: `hops` = the minimum depth at which the
   counterparty is evidenced, `relationship` follows `hops`, and
   `path[]`/`transaction_evidence` are restricted to min-hop
   sequences. Hop-2 paths terminating at an already-emitted
   counterparty contribute nothing (its edges may still appear
   inside other items' paths). Dedup precedes category selection —
   `matched_signals` are depth-independent.

### 3.4 Schema (migration 000008)

- `transactions` per spec §7.13 **plus `is_error BOOLEAN NOT NULL
  DEFAULT false`** (declared deviation — `receiptsStatus`/`isError`
  is execution status, never conflated with `confirmation_status`;
  `block_timestamp` is indexed rows only — both adapters return mined
  transactions, so NULLs never reach ordering/pagination logic);
  `confirmation_status` vocabulary `pending|confirmed` lowercase;
  mapping `confirmations>0 → confirmed, else pending`; nodereal
  computes confirmations `head − blockNum + 1`).
- `transfer_index` is **content-derived and context-free**:
  `{kind}:{h}` where `h` = 16 hex chars of SHA-256 over the immutable
  stored content `(transfer_type, asset_identifier‖∅,
  from_normalized, to_normalized, amount_raw)` — never over mutable
  columns (hash inputs = stored canonical values: normalized
  addresses, NUMERIC-canonical amount string, lowercase contract,
  fixed field order — protects identity across provider-switch
  reindex). Byte-identical duplicate transfers within one tx get `.{k}`
  occurrence suffixes (`t:{h}.1`, `t:{h}.2` — identical rows are
  indistinguishable so suffix order is unobservable and consistent
  across fetch contexts). **Counting domain pinned**: occurrences per
  `(tx_hash, h)` = `max` over **per-stream** occurrence counts within
  the assembled scan — the correct cross-adapter premise is *no
  stream lists a real copy more than once, and ≥1 stream covered the
  block*: on nodereal a W→W self-transfer counts `max(1,1)=1`; on
  chainscan each `(tx_hash,h)` is confined to exactly one kind-stream
  so `max` degenerates to the single-stream count — and
  counting runs over the assembled batch, never per page (a page
  boundary inside an identical-copy group is reconciled by the tail
  refetch — whole block re-scanned, self-healing). Residual declared:
  both direction streams truncated inside the same identical-copy
  group is undetectable and bounded. This gives every real transfer
  one identity regardless of which wallet's stream fetched it
  (cross-wallet dedup), makes "immutable fields never change"
  literally true, and dissolves the partial-window-union problem.
  Kind prefixes `n:`/`t:`; INTERNAL excluded in MVP (§6) — future
  `i:{h}`. Declared residuals: cryptographic collision (negligible);
  a pagination bug double-returning a row inflates `.{k}` by ≤1
  (bounded, honest).
- `wallet_index_state`: `(network_id, wallet_id)` PK; `source_id
  BIGINT REFERENCES data_sources(id)`; `indexed_until_block`,
  `indexed_at`, `status` enum `{not_indexed, indexing, fresh,
  partial, failed, disabled}` lowercase; `error` (cleared-on-
  success); `rows_indexed` + `rows_skipped_missing_contract` +
  `rows_skipped_invalid_address` = **cumulative** counters;
  `history_truncated BOOL`. `status=partial` ⟺ `history_truncated`
  (one upsert writes `status='partial'` ∧ `history_truncated=true`
  together; the flag is authoritative on read; a successful
  unbounded fetch clears both to `fresh`/`false`). On the wire
  `stale` is a **derived boolean** beside
  `index_status` — `indexed_at` vs `IndexingFreshTTL` — composable
  over any status, never a stored enum value.
  `provider_status` on every response = persisted `error` when
  non-empty (its ErrKind, `disabled` included); `"ok"` only after a
  succeeded attempt (`status ∈ {fresh, partial}`, empty error) —
  lowercase vocabulary, deliberately distinct from enrichment's
  uppercase status fold.
- Bulk counterparty resolve: `INSERT … SELECT unnest() ON CONFLICT
  (network_id, normalized_address) DO UPDATE SET updated_at =
  wallets.updated_at` → full `RETURNING` address→id map (existing
  rows are not omitted — wallets.sql convention); addresses pass
  `wallet.NormalizeEVM`; invalid from/to rows skipped + counted in
  `rows_skipped.invalid_address`.
- Bulk label match for counterparties: `WHERE w.network_id = $1 AND
  w.normalized_address = ANY($2) AND wl.revoked_at IS NULL` (the only
  existing reads are per-address or `LIMIT 5` summaries — a batch
  query is required); sanctions matching stays per-node
  `LookupWallet` (cache-backed, node-bounded);
  `sanctions_snapshot_version` in coverage comes from
  `LatestSnapshotPublished` fetched once per request.
- Upsert rule: `ON CONFLICT (network_id, tx_hash, transfer_index)
  DO UPDATE` on the mutable set `{confirmation_status, block_number,
  block_timestamp, is_error}` — this is what makes tail-window
  revalidation actually revalidate (immutable fields never change:
  from/to/asset/amount).

### 3.5 API responses (OpenAPI additions)

`/transactions` envelope: `items[]` (tx_hash, direction, transfer_type,
asset_identifier|null, amount_raw string, counterparty, block_number,
block_timestamp, confirmation_status, is_error) + `{index_status,
stale, provider_status, indexed_until_block, indexed_at, head_block
(null on no-fetch serves), history_truncated, index_scope,
rows_indexed, rows_skipped{missing_contract,
invalid_address}, next_cursor}` — keyset cursor `(block_timestamp, id)`
base64, `INVALID_CURSOR` reuse; items sort `block_timestamp DESC`
(keyset handles both directions via cursor); `direction` values
`in|out` relative to the queried wallet; `asset` filter:
`asset=native` selects NATIVE rows, `asset=<contract-address>` selects
a TOKEN asset; `from_timestamp`/`to_timestamp` bound
`block_timestamp` inclusively; `limit` ≤100 (cursor-bounded — the
response cap for /transactions is the page itself, declared).
Unfiltered listing = UNION of the from-index and to-index scans with
keyset over the union — a W→W self-row is emitted **once** and its
`direction` value is always `"self"` (third wire value, declared §6 —
the row's fact, not a filter echo). **Per-endpoint filter enums
pinned**: `/transactions` `direction` accepts `{in,out,self}` —
`self` ⇒ self-rows only; `in`/`out` both match self-rows;
`/exposures` `direction` accepts `{in,out}` (self-edges are excluded
by construction there).
`/exposures`: items §9.10 + envelope `coverage`/`truncated` per §3.3.8;
`limit≤100`; ~256 KB cap.
Screening vocabulary: `exposures.coverage.tracing` changes
`"NOT_IMPLEMENTED"` → `"provider_reported_only"` (enum stays
closed-ended with documented values
`{provider_reported_only, bounded_bfs}` to avoid recreating this
drift); consumers enumerated: `internal/screening/{service.go,
mapper.go}` emission sites, `model.go` comment, `service_test.go`
assertion, `api/openapi.yaml` enum, and 5
`docs/feature-maps/assets/*.json` captures (regenerated).

## §4 Phases

| File | Scope |
|---|---|
| `phase-01-provider-and-schema.md` | migration 000008, sqlc, `TxProvider` seam, both adapters + config |
| `phase-02-indexing-service.md` | `internal/transaction`: EnsureIndexed singleflight, watermark windows, upsert, coverage |
| `phase-03-tracing-bfs.md` | bounded BFS, deterministic expansion, evidence, coverage |
| `phase-04-api-and-wiring.md` | handlers, routes, OpenAPI, metrics, E2E |

## §5 Required test list (all must exist)

1. txlist/tokentx + nr_getAssetTransfers fixtures → normalized rows;
   token carries contract; amount_raw integer string.
2. Upsert idempotency + mutable-column revalidation (refetch → 0 dupes,
   confirmation_status updates).
3. Provider DISABLED → honest coverage + no fake empty (both adapters).
4. 1-hop exposure on labeled counterparty → `direct_counterparty` +
   evidence edges.
5. 2-hop → `indirect_hop`, path len 2, `temporal_order_valid`.
6. Bound truncations → `coverage.truncated` + named reason each.
7. Freshness: stale refetches, fresh does not; FAILED cooldown.
8. Provider failure → `coverage.provider_status` typed.
9. `confirmation_status` mapping + tail-window revalidation updates
   mutable columns.
10. Sanctioned counterparty flagged only via same-network direct
    bucket (published snapshot).
11. Cursor pagination honest; `limit` bound; `INVALID_CURSOR`.
12. Response keeps `relationship` vocabulary — no ownership language
    (grep-checked).
13. Multi-transfer tx → ≥2 distinct rows; identical-content duplicates
    both survive via `.{k}`; refetch → identical `transfer_index`
    values; same tx ingested via two different wallets' streams → one
    row per real transfer, zero loss, zero collisions; W→W
    self-transfer via nodereal ⇒ exactly one row (max-count); two
    identical W→W ⇒ two rows.
14. K concurrent `EnsureIndexed` cold wallet → exactly 1 fetch sequence.
15. Provider budget before `max_nodes` → `truncated=max_provider_requests`.
16. Per-network `Enabled()`: ETH key ⇒ ethereum on, bsc DISABLED +
    reason in coverage; config override enables bsc on chainscan.
17. FAILED state: partial rows + partial coverage; cooldown prevents
    hammering.
18. `history_truncated` asserted when `max_pages` bites — and still
    reported on a subsequent fresh-serve/cooldown response (persisted
    column).
    18b. Provider-switch reset: rows written under adapter A, `source_id`
    mismatch ⇒ watermark reset, re-index proceeds.
    18c. `refresh=1` bypasses freshness while the FAILED cooldown still
    holds; stale `indexing` row (> deadline) recovers to retryable.
19. Cross-chain-only counterparty → item `category=cross_chain_listing`,
    never `SANCTIONED`; multi-match precedence `SANCTIONED` > label >
    cross_chain; `matched_signals[]` carries all.
20. Self-edge + root-returning path excluded; `path_verified` always
    false.
21. `is_error=true` tx → no edge, no counterparty, no item; row still
    listed on /transactions with `is_error` flag.
    21b. Unfiltered listing emits a W→W self-row once with
    `direction="self"`; `direction=in`/`out` filters both match;
    sentinel counterparties excluded from expansion but still listed;
    `max_hops=1` → hop-1 items only (nodes_*=0), `=0`/`=3` → 400;
    `direction`/`category` filters on /exposures behave per §3.3.8.
22. Prefix property: emitted items are always a contiguous prefix of
    the declared order and identical given identical resolution sets —
    exercised over timing-free bounds (max_provider_requests/max_nodes
    truncation, pre-expired ctx, `resilience.WithClock`); wall-clock
    deadline asserted only as `truncated=deadline` firing.

## §6 Deviations declared

- `is_error` column added to `transactions` (spec §7.13 lacks it;
  receiptsStatus/isError needs a home — declared, never conflated with
  confirmation_status).
- INTERNAL transfers excluded in MVP: chainscan uses txlist+tokentx;
  nodereal has no trace identifiers — index scope is
  `native + fungible token`, surfaced in coverage (`index_scope`).
- `refresh=1` on both endpoints (ops forcing; same bounds; OpenAPI).
- `exposures.coverage.tracing` on POST /screenings →
  `"provider_reported_only"` (contract update + fixture regeneration).
- `path_verified` constant `false` (MVP cannot prove same-asset
  continuity — deliberate).
- Zero-value `external` rows excluded (`excludeZeroValue` on nodereal;
  `value>0` filter on chainscan txlist) — contract calls are not
  transfers; declared scope.
- Deep-reorg revalidation: tail-window only (documented residual;
  tail rows absent from a refetch are left as-is, not marked).
- BSC hop-2 is deadline-bound by design at free-tier CUPS (§3.1).
- `cross_chain_listing` also covers unresolved-network listings (the
  LookupWallet third bucket — not demonstrated cross-chain; name kept
  for wire brevity, documented here).
- §11.5 path-level `data_coverage`/`truncated` collapse into the
  exposures envelope (one decision point, not per-path constants).
- `direction="self"` third wire value on /transactions items; edge
  schema carries `transfer_index` (beyond §11.5's listed fields —
  identical-content copies are otherwise indistinguishable).
