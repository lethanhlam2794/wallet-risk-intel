# Feature Map — Transaction Tracing (Phases 6–7)

## Endpoints

| Method | Path | Purpose | Handler |
|---|---|---|---|
| GET | `/api/v1/wallets/{network}/{address}/transactions` | Indexed transfers, keyset-paginated, coverage-honest envelope | `httpserver.TxHandler.Transactions` |
| GET | `/api/v1/wallets/{network}/{address}/exposures` | Bounded-BFS risk exposures (1–5 hop) with evidence paths + `exposure_score` | `httpserver.TxHandler.Exposures` |

Query params: `direction=in|out|self`, `asset=native|<contract>`,
`from_timestamp`/`to_timestamp` (RFC3339), `cursor`, `limit` (≤100),
`refresh=1`; exposures adds `max_hops=1..5` (0 or >5 → 400) and
`category`.

## Packages

| Package | Role |
|---|---|
| `internal/transaction` | Lazy indexing (`EnsureIndexed`): singleflight, freshness/cooldown/disabled/stale-claim gates, ascending watermark windows, reorg-tail overlap, per-stream contiguous coverage, content-derived `transfer_index`, `/transactions` read path |
| `internal/tracing` | Bounded BFS: hop-1 edge scan → per-depth ordered-position frontier expansion 2..max_hops (shared concurrency + provider pool, CAS edge budget) → contiguous-prefix assembly → matched signals + exposure-v1 scoring → precedence-pinned truncation |
| `internal/provider/chainscan` | Etherscan V2 (`txlist`/`tokentx` page+offset, per-stream cursors, 10k-record cap detection via `Capped` token set) |
| `internal/provider/nodereal` | BSC `nr_getAssetTransfers` (opaque pageKey, from/to streams, 100k window cap) |

## Index lifecycle (`wallet_index_state`)

`not_indexed → indexing → fresh | partial | failed | disabled`

- `fresh` = every declared stream completed through `fetch_to` — never
  earned by a cursor-dropped unfinished stream or a zero-page stop.
- `partial` = bounded stop (max_pages/budget/window cap/record cap) —
  persisted with `history_truncated=true`.
- Watermark = min over per-stream coverage: finished → `fetch_to`;
  unfinished → **its own** max block − 1; untouched → `fetch_from − 1`.
- Reorg tail: `fetch_from = max(0, watermark − tail + 1)`; mutable-set
  upsert revalidates `{confirmation_status, block_number,
  block_timestamp, is_error}`.
- Stale claim: live `indexing` within `IndexingStaleAfter` is served;
  past the horizon it is retried.
- `provider_status` on the wire: persisted ErrKind, `"ok"` only after a
  succeeded attempt, `null` otherwise — never a fake verdict (incl.
  local budget exhaustion).

## `transfer_index` identity

`{kind-letter}:{sha256-16hex-of content}.{k}` — content = transfer
type + asset + normalized from/to + canonical amount. `.{k}` count =
max across per-stream occurrences (a W→W sighting on both streams
counts once). Byte-identical duplicates stay distinguishable.

## `/exposures` bounds & truncation

`deadline > max_provider_requests > max_nodes > max_edges >
response_cap > limit` — every bound names its reason; edge-budget
clips are CAS-reserved and reported (`edgesClipped`), never silent.
Hop-d paths are per **node sequence** (`root→via₂→…→cp`), forward-DFS
enumerated over emitted-prefix parents/children, evidence edges
picked by ts → amount → (tx_hash, transfer_index); `path_verified`
is always false — a graph path never proves ownership.

Per-depth coverage: `frontier_by_hop` / `nodes_examined_by_hop` /
`frontier_emitted_by_hop` / `nodes_remaining_by_hop` keyed by
materialized frontier index {2..max_hops}; `positions_emitted` = Σ
contiguous resolved prefixes; `max_depth_reached` = deepest frontier
with ≥1 attempt; `examined + remaining = total` holds globally.

`exposure_score` (envelope `exposure_policy_version="exposure-v1"`):
per-item `min(100, Σ base×decay×vf×cf)` with per-term integer half-up
rounding (denom 1600); bases — sanctions_direct 100, label `SANCTIONED`
85, MALICIOUS_ACTIVITY label 70, other tiers 0, sanctions listing 20 —
decay 1/2/4/8/16-sixteenths for hops 1–5. Null when all signals are
zero-weight. Σ is literal over evidence rows (declared Σ-additive
deviation from Phase-5 max-block). Never feeds `risk_score`.

Cross-chain correlation is observation-only: foreign-network label
rows emit `kind:"label"` signals carrying `network` + `listed_address`
(original representation), score at full label weight, never win local
category precedence (solely-foreign → `cross_chain_listing`), and never
trigger expansion — BFS stays inside the request network.

## Invariants (enforced + regression-tested)

- unknown ≠ safe; provider failure never clean (disabled/failed
  persisted + surfaced; zero upstream calls under disabled)
- 1-hop ≠ ownership; `path_verified=false`; no ownership vocabulary
- direct attribution ≠ direct exposure ≠ indirect exposure
- sentinel addresses listed, never expanded at any depth; self-edge
  once as `direction="self"`, excluded from graph expansion
- `is_error` rows visible on `/transactions`, excluded from graph edges
- cross-chain listings → `cross_chain_listing`, never `SANCTIONED`;
  foreign `kind:"label"` signals carry `network`+`listed_address`,
  never win local category
- BFS never crosses networks — cross-chain is label evidence only
- `exposure_score` never feeds `risk_score`; `POST /screenings`
  `options.max_hops` stays 0–2
- category precedence: SANCTIONED > LEGAL > MALICIOUS > SERVICE > ENTITY
- reproducible coverage: per-node `indexed_until`, snapshot versions,
  request-global provider budget, `tracing_method=bounded_bfs`

## Raw captures

`docs/feature-maps/assets/live-transactions-disabled.json`,
`live-exposures-disabled.json` (real API, disabled-provider lifecycle),
`live-exposures-3hop.json` (seeded 3-hop + cross-chain label,
`exposure_score=29`), `live-exposures-advanced-disabled.json`
(unknown wallet at max_hops=3).
