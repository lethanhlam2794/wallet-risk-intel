# Guidebook — Transaction Tracing (Phases 6–7)

Phase 6 adds lazy transaction indexing and bounded transaction-exposure
tracing for Ethereum and BSC; Phase 7 extends the bounded traversal to
1–5 hops with per-depth frontiers, observation-only cross-chain label
correlation, and a separately versioned `exposure_score`. It answers,
with evidence: *which wallets did this wallet transact with, which of
them carry risk signals, and which transaction hashes prove the path* —
while keeping the index's coverage claims strictly honest.

## 1. Architecture in one paragraph

`GET /transactions` lazily brings the wallet's index forward inside
PostgreSQL (`EnsureIndexed`): ascending-block windows anchored at a
persisted watermark, with reorg-tail overlap, per-stream coverage
math, singleflight per (network, wallet), and a request-global provider
budget. `GET /exposures` runs bounded BFS over the indexed rows:
hop-1 edge scan → per-depth ordered-position frontier expansion (one
frontier per depth, 2..max_hops) → contiguous-prefix assembly →
matched-signal emission → precedence-pinned truncation.
Providers are adapters behind the `TxProvider` seam: `chainscan`
(Etherscan V2, Ethereum + configured chains) and `nodereal`
(BSC `nr_getAssetTransfers`).

## 2. Endpoints

```
GET /api/v1/wallets/{network}/{address}/transactions
GET /api/v1/wallets/{network}/{address}/exposures
```

Filters: `direction=in|out|self`, `asset=native|<contract>`,
`from_timestamp`, `to_timestamp`, `cursor`, `limit` (default 50, max
100), `refresh=1`. Exposures adds `max_hops=1..5` (0 or >5 → 400) and
`category`.

## 3. The index honesty contract

Every response carries `index_status`, `stale`, `provider_status`,
`indexed_until_block`, `indexed_at`, `history_truncated`,
`index_scope`, `rows_indexed`, `rows_skipped`, `head_block`:

- `fresh` — every declared stream completed through `fetch_to`.
- `partial` — stopped on a bound (`history_truncated=true`).
- `failed` — typed provider failure, ErrKind in `provider_status`.
- `disabled` — no configured provider for the chain; zero upstream
  calls, never a clean-looking empty index.
- `indexing` — a live claim inside the stale horizon.
- `not_indexed` — never attempted.

The watermark is the **min over per-stream contiguous coverage**: a
finished stream proves `fetch_to`; an unfinished stream proves only
its own max block − 1 — a fast `txlist` can never lend coverage to a
lagging `tokentx` (review blocker B1). A capped stream (Etherscan 10k
record bound) drops its cursor yet stays unfinished, so coverage stops
at its boundary instead of fabricating completion (M5).

`provider_status` is a persisted ErrKind, `"ok"` only after a real
upstream success, `null` otherwise — local budget exhaustion is not a
provider verdict.

## 4. Transfer identity

`transfer_index = {n|t}:{sha256-16 of content}.{k}` over
`type + asset + from + to + canonical amount` — context-free, so the
same transfer indexed from either wallet agrees on identity; `.{k}` is
the max per-stream occurrence count, so a W→W sighting on both streams
yields one identity set while byte-identical duplicates remain
distinguishable rows.

## 5. Exposure semantics

- `relationship`: `direct_counterparty` (1 hop) or `indirect_hop`
  (≥2 hops) — never ownership.
- `paths[]`: hop-1 items carry one evidence edge; hop-d items carry up
  to 3 **node sequences** (`root→via→…→cp`), each with picked edges
  (earliest ts → smallest amount → tx hash/index) and
  `path_verified=false` + `temporal_order_valid` (every adjacent edge
  pair nondecreasing in timestamp). Deeper paths are enumerated by
  forward DFS over emitted-prefix parents/children — never the full
  product.
- Frontier expansion is per-depth (`frontier_2..5`): each scheduled
  frontier is built from **all scanned edges of the previous depth's
  emitted-prefix nodes** (clean intermediaries seed deeper frontiers).
  Every depth keeps the two-book split — `cpAgg` holds item/path
  candidates, the frontier holds the expansion subset after sentinel
  and dedup exclusion. Dedup is snapshotted per level: root ∪ frontier
  positions queued so far ∪ counterparty sets of emitted-prefix scans —
  a node emits at its minimum reachable depth only.
- `matched_signals[]`: sanctions direct (`SANCTIONED`), labels with
  `verification_status` verbatim (`reported` stays reported),
  `cross_chain_listing` for cross-network observations — never
  `SANCTIONED` across chains.
- **Cross-chain labels** (phase 7, observation-only): a label row for
  the same normalized address on a *different* network emits a
  `kind:"label"` signal carrying `network` + `listed_address` (the
  foreign row's original representation). It contributes to
  `exposure_score` at full label weight but never wins local category
  precedence; an item whose only signals are foreign labels gets
  `category="cross_chain_listing"`. BFS itself never crosses networks —
  no provider call is ever made for a foreign network.
- `coverage`: counterparties_total, nodes examined/emitted/remaining/
  failed, edges_examined, provider_calls/status, per-node
  `indexed_until`, snapshot versions, full root coverage, per-hop maps
  `frontier_by_hop` / `nodes_examined_by_hop` /
  `frontier_emitted_by_hop` / `nodes_remaining_by_hop` (keyed by
  materialized frontier index {2..max_hops}), `positions_emitted`,
  `max_depth_reached` (deepest frontier with ≥1 attempt), and
  `truncated` with a named reason
  (`deadline > max_provider_requests > max_nodes > max_edges >
  response_cap > limit`).

### 5.1 `exposure_score` (exposure-v1)

Every item carries `exposure_score` — a separately versioned
(`exposure_policy_version: "exposure-v1"` on the envelope, always)
evidence weight in 0–100, or `null` when every signal is
informational-only (never `0` on the wire). Per signal row:

`score = min(100, Σ base × decay(hops) × vf × cf)` — each term
integer half-up rounded under denominator 1600.

| Term | Value |
|---|---|
| base | `sanctions_direct` 100; label code `SANCTIONED` 85; `MALICIOUS_ACTIVITY` label 70; other label tiers 0; sanctions `listing` 20 |
| decay | hop 1–5 → 1, ½, ¼, ⅛, 1/16 |
| vf | verified 1.0 · reported 0.7 · unverified 0.5 · disputed 0 |
| cf | high 1.0 · medium 0.9 · low 0.8 · unknown 0.7 |

Deviation (declared): the Σ is literal over evidence rows — no
per-kind dedup or mirror-source collapsing — unlike Phase-5's
max-block scoring. `exposure_score` never feeds
`screening_results.risk_score`; `POST /screenings` `options.max_hops`
stays 0–2.

Sentinel addresses (zero/burn) appear as counterparties but are never
expanded at any depth (they may still emit items). `is_error` rows
appear on `/transactions` but never form graph edges. Self-transfers
emit once as `direction="self"`, match `in`/`out` filters, and never
expand. `direction=` filters the hop-1 edge set only; deeper scans are
bidirectional facts.

## 6. Configuration surface

`ETHERSCAN_API_KEY`, `CHAINSCAN_BASE_URL`,
`CHAINSCAN_ENABLED_CHAINS` (bsc off by default — paid tier),
`NODEREAL_API_KEY`, `NODEREAL_BASE_URL`, `TXINDEX_PAGE_SIZE`,
`TXINDEX_MAX_PAGES_HOP0/HOPN`, `TXINDEX_REORG_TAIL_{ETH,BSC}`,
`TXINDEX_WINDOW_CAP_BSC` (100k upstream cap), `TXINDEX_FRESH_TTL`,
`TXINDEX_STALE_AFTER`, `TXINDEX_BUDGET_{ETH,BSC}`
(request-global provider pools), `TXINDEX_MAX_PAGES_HOP3` (shared
frontier-3+ page tier, default 1), `TRACING_MAX_NODES_{2,3,4,5}`
(per-depth position caps; legacy `TRACING_MAX_NODES` is the depth-2
fallback), `TRACING_MAX_EDGES`, `TRACING_DEADLINE`,
`TRACING_RESPONSE_CAP`, `TRACING_CONCURRENCY`.

## 7. Failure playbook

| Condition | Wire truth | Retry posture |
|---|---|---|
| provider timeout/429/degraded/malformed | `failed` + ErrKind | 60 s cooldown |
| no provider configured | `disabled` + `error='disabled'` | per-request re-eval |
| max_pages / window cap / record cap | `partial` + truncation | next window continues |
| stale `indexing` claim | served within horizon | retried past horizon |
| BFS bound hit | `truncated` reason | smaller filters |
| infra error inside hop-2 | HTTP 500 | never faked as deadline |

## 8. Evidence

Live captures (real API :8091, SQL-seeded 3-hop graph root→A→B→C,
local SCAM on C + foreign BSC HACK label):
`docs/feature-maps/assets/live-exposures-3hop.json` — 3-edge path,
`hops=3`, `category=SCAM` (local wins over foreign),
`exposure_score=29` (18 SCAM + 11 foreign HACK),
`exposure_policy_version="exposure-v1"`, per-hop coverage maps —
and `live-exposures-advanced-disabled.json` (unknown wallet,
disabled lifecycle). Disabled-lifecycle captures:
`docs/feature-maps/assets/live-transactions-disabled.json`,
`live-exposures-disabled.json`. Real-index E2E:
`tests/transactions_api_test.go` (real engine + real DB + stubbed
upstream). Feature map: `docs/feature-maps/transaction-tracing.md`.
