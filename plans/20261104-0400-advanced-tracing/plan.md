# Phase 7 — Advanced Tracing (plan v3 — self-contained)

Scope (user-approved, spec §19 Phase 7): 3–5 hop tracing, cross-chain
correlation (observation-only), advanced exposure scoring (new
separately-versioned field; `risk_score`/`scoring_policy_version`
untouched), large-graph optimizations. Invariants carried from
Phases 1–6: unknown ≠ safe; graph path ≠ ownership/identical fund
flow; reported ≠ verified; cross-chain observation ≠ direct listing;
provider failure never clean; coverage always honest.

## 1. Scope decisions (pinned)

1. `max_hops` on `/exposures`: 1–5 (0 or >5 → 400). Default stays 2.
   `POST /screenings options.max_hops` stays 0–2 — **unchanged**, do
   not harmonize upward.
2. Cross-chain = foreign-network **label** observation only
   (sanctions cross-chain observations already exist via
   `CrossChainObservations`/`UnresolvedNetworkListings` → `kind:
   listing` signals — unchanged). The BFS graph NEVER crosses
   networks.
3. `exposure_score` is a NEW per-item field under
   `exposure_policy_version = "exposure-v1"` on the envelope — never
   feeds `screening_results.risk_score` (policy change out of scope).
4. Frontier generalization uses Reading A below; all Phase-6 wire
   fields keep meaning at max_hops ≤2 (bit-identical behavior).

## 2. Frontier generalization (B1)

### 2.1 Frontier construction — Reading A pinned

Frontier_d (d ≥ 3) is built from **all scanned edges** (`r.edges`) of
the depth-(d−1) emitted-prefix nodes — NOT only `pickEdge`-emitted
path edges. Clean (unmatched) intermediaries seed deeper frontiers —
that is what deeper hops exist to reach through. Sentinel addrs are
excluded at EVERY frontier derivation (they may still emit items if
they carry signals, but never consume a position or provider call).

### 2.2 Position structure

```go
type position struct {
    addr    string
    parents map[string][]db.ListWalletEdgesRow // parentAddr → incoming edges
}
// frontier_d: addr → *position; order via sort key §2.3
```

Each depth keeps the Phase-6 two-book split:
- `cpAgg_d`: addr → (parent, edge) contributions over ALL scanned
  counterparties (self-edges excluded) — the item + path-evidence
  store. Sentinel far endpoints remain item/path candidates (never
  positions); dedup-set far endpoints are excluded from candidacy
  entirely — their edges are never re-emitted at a deeper level.
- `frontier_d`: the expansion subset of cpAgg_d — a candidate is
  excluded from positions when it is a **sentinel** or already in the
  **dedup set** (§2.4); it stays in cpAgg_d for items.
A candidate's (parent, edge) contributions are recorded once; every
path edge carried as evidence.

### 2.3 Ordering key — multi-parent pinned

Per candidate: collect ALL incoming edges across all emitted-prefix
parents; take `firstEdge` (earliest block_timestamp, then tx_hash,
then transfer_index) over that union; order candidates by
(firstEdge.ts, firstEdge.tx_hash, candidate normalized_address).
Deterministic under every schedule.

### 2.4 Dedup set (min-depth + cycle exclusion, unified)

`dedup` is snapshotted per level build. Before building the level-d
candidate set (d ≥ 3, terminal included):
`dedup` = root ∪ every addr queued as a position in frontiers ≤ d−1
∪ every addr in a **post-filter counterparty set** of an emitted-prefix
node's scanned edges at scan levels ≤ d−2.
- At level 1 the counterparty set is the POST-`direction`-filter set
  (identical to Phase-6's `cps`; the filter applies BEFORE `cpAgg_2`
  construction, exactly as Phase-6 filters before `cps`) — a direction-filtered root cp (never
  in `cps`) can still emit at a deeper level, preserving bit-identical
  behavior at max_hops≤2 under `direction`.
- At levels ≥2 the counterparty set is unfiltered (deep scans are
  bidirectional facts).
The producing scan for the level-d set is level d−1 — strictly outside
the clause, so candidates are not self-excluded. A candidate already
in `dedup` emits no item and no position at the deeper level — min-
depth dedup, uniform at EVERY level including the terminal one (the
terminal candidate set is dedup'd identically; it is not a scheduled
frontier — excluded from `counterparties_total`, no MaxNodes cap,
items bounded by response_cap/limit). Ancestor cycles are impossible
because every path's depth strictly increases. Residual declared: a
node glimpsed ONLY via a beyond-prefix (never emitted) node's scan is
invisible to deeper levels — consistent with the emitted-prefix domain
and recorded via `positions_emitted`.

`nodeUntil` (per-node `indexed_until`) is a request-global
`addr → *int64` map populated for every attempted position at every
depth — uniqueness holds via dedup.

### 2.5 minPos / item ordering

`Item.minPos` keeps TWO pinned domains (domains never mix — the sort
compares minPos only within equal `hops`):
- hop-1 items: rank in `cpOrder` over ALL hop-1 counterparties
  including sentinels — unchanged from Phase 6.
- hop-d items (d ≥ 2): min over contributing sequences of the FIRST
  via's position index in frontier_2 (the hop-1 position list). For
  d=2 this is exactly today's `viaPos`; generalizes the "which
  first-hop intermediary" ordering.
Item total order unchanged: hops asc → minPos → counterparty asc.
Determinism test asserts order AND paths.

### 2.6 Per-depth caps

- `TRACING_MAX_NODES_{2,3,4,5}` defaults 50/25/12/6; legacy
  `TRACING_MAX_NODES` remains the depth-2 fallback when
  `TRACING_MAX_NODES_2` is unset. `tracing.Config` gains
  `MaxNodesPerDepth [4]int` (index d−2); `defaults()` fills it from
  the envs with the legacy fallback; `Concurrency` is shared across
  all depth loops.
- Index page tiers: existing `TXINDEX_MAX_PAGES_HOPN` keeps its name
  and covers depth 2 (frontier_2 attempts); NEW
  `TXINDEX_MAX_PAGES_HOP3` (default 1) covers frontier_3..5 attempts
  (one shared deeper tier). `transaction.Service.MaxPagesFor(hopN
  bool)` becomes `MaxPagesFor(frontierIdx int)`: 0/root → existing
  `MaxPages≤0` fallback; 2 → HopN tier; ≥3 → Hop3 tier.

### 2.7 Coverage vocabulary at depth ≥3 (M1 — pinned)

- `counterparties_total` = Σ |frontier_d| over all MATERIALIZED
  frontier position-books d ∈ {2..max(max_hops,2)} — frontier_2 is
  materialized unconditionally (Phase-6 derives `positions` for
  ordering/dedup/coverage regardless of expansion), so
  total = |frontier_2| at max_hops=1, exactly Phase-6's
  unconditional `len(positions)`. "Scheduled" gates only attempts and
  `max_nodes`. The terminal candidate set (level max_hops+1 cpAgg) is
  NOT counted. At max_hops≤2 all values bit-identical — unchanged.
- `nodes_examined` = positions attempted across all depths;
  `nodes_remaining = counterparties_total − nodes_examined`
  (floored ≥0) — invariant `examined + remaining = total` preserved.
- `positions_emitted` = Σ contiguous resolved prefixes across depths.
- NEW fields keyed by MATERIALIZED frontier index (every built
  frontier_d appears — {2..max_hops}; at max_hops=1 the map shows
  `{2:n}` with examined 0; the terminal candidate set is not an
  expansion frontier and appears in none):
  `frontier_by_hop`, `nodes_examined_by_hop`,
  `frontier_emitted_by_hop`, `nodes_remaining_by_hop`;
  `max_depth_reached` = deepest frontier index whose positions were
  ATTEMPTED (0 = none; scheduled-but-zero-attempts — e.g. budget
  already exhausted — does NOT count; attempted-then-clipped DOES
  count; ceiling = max_hops).
- `node_indexed_until` spans attempted positions at EVERY depth.
- Truncation (precedence unchanged, evaluated post-assembly):
  `deadline > max_provider_requests > max_nodes > max_edges >
  response_cap > limit`. `max_nodes` fires when a frontier SCHEDULED
  for expansion (d ≤ max_hops) exceeds MaxNodes_d — frontier_2 built
  but not scheduled (max_hops=1) never fires it, preserving the
  Phase-6 `maxHops==2` guard (which depth hit is not represented —
  declared). `max_provider_requests` fires on pool exhaustion with
  nodes_remaining > 0. `max_edges` fires on `edgesClipped` or
  edgesLeft ≤0 with unexamined positions at any depth.

### 2.8 direction scope (M2 — pinned, status quo)

`direction` filters the **hop-1 edge set only**, before position
derivation — identical to Phase 6. Deeper-depth edges are unfiltered
bidirectional: a `direction=out` response may carry inbound deep edges
as path evidence. Declared residual — evidence edges describe the
path, not the filter direction.

### 2.9 Path evidence at depth ≥3 (M5 — pinned)

- `hop2`-style map generalizes: `paths_d[cp][seq]` where `seq` = the
  address tuple (root, via₂, …, via_d) — one entry per node sequence.
  Sequence ordering key: lexicographic over the address tuple
  (via₂, …, via_d), root excluded.
- `temporal_order_valid` = every ADJACENT edge pair non-decreasing
  (`e₁.ts ≤ e₂.ts ≤ … ≤ e_d.ts`) — transitive.
- `MaxPathsPair` keeps the first 3 sequences in tuple order over the
  FULL sequence set — no parent-map pre-truncation (it can drop the
  true first tuple at d ≥ 4).
- Enumeration mechanism: tuple order is via₂-MOST-significant, so
  enumeration runs FORWARD. During frontier build, each position
  records a sorted `children` list (next-level addrs it reaches);
  sequences emit via lexicographic DFS from frontier_2 positions in
  ascending ADDRESS order restricted to the reachability mask —
  matching via₂-most-significant tuple order (frontier order is the
  scheduling key, NOT the tuple key) — following sorted children
  lists, pruned by the per-item reverse-reachability mask (positions that can reach cp —
  one reverse BFS over the parents DAG). Cut after `MaxPathsPair`
  emitted — never materializes the full product. Honest bound:
  O(mask_build + MaxPathsPair × d × P) per item where mask_build ≤
  Σ|parents| edges and P = max children per position; emitted cps
  bounded by edges_examined (≤ 5000).
- Evidence pick per edge position: `pickEdge` (earliest ts → smallest
  amount → (tx_hash, transfer_index)) over that step's candidate
  edges — same rule as hop-2.
- `Path{HopCount:d, PathVerified:false, TemporalOrderValid,
  Edges[len=d]}` — HopCount equals `len(Edges)`.

## 3. Cross-chain correlation (M4 — pinned)

- **What is new**: foreign-network `wallet_labels` matches for the
  normalized address — nothing else (sanctions observations already
  flow through the existing `kind:"listing"` path — untouched,
  no duplication).
- Query: new sqlc `BulkMatchLabelsCrossNet` — same shape as
  `BulkMatchLabels` plus `n.code AS network_code`, `wl.confidence`,
  filtered `w.network_id = ANY($2::bigint[])`; params pinned
  `$1`=addrs, `$2`=resolver active-network IDs **∖ {request net ID}**
  (deactivated/absent networks never match; empty set → zero rows).
  One union call per depth for all counterparties.
- Signal shape: `MatchedSignal{kind:"label", category:<foreign
  category code>, label, entity, source, verification_status,
  network:<foreign net code>, listed_address:<foreign
  `wallets.address` original representation>}` — `kind` stays inside
  the closed enum (`label`).
- Precedence: foreign-label signals NEVER participate in `consider()`
  — `signals()` distinguishes foreign rows (network ≠ request net) to
  skip them; item.category derives only from local signals; an item
  whose only signals are foreign gets `category="cross_chain_listing"`.
- Scoring parity declared: foreign labels score at full `base()`
  weight (provenance is carried on the signal via `network`;
  down-weighting foreign labels is a policy choice for a later
  exposure policy version — declared, not silent).
- Wiring: `tracing.Service` gains `resolver *network.Resolver` —
  `tracing.New(q, tx, sanctions, resolver, cfg)`; `cmd/api/main.go`
  passes the existing resolver; phase-02 owns the sqlc query +
  regeneration.

## 4. Exposure scoring `exposure-v1` (B2/B3 — pinned)

Per item, computed at item-build time (before the category filter)
over the same evidence rows that produce `matched_signals` (label
rows carry `category_type` + `confidence`; `MatchedSignal` stays
unchanged on the wire):

```
score = min(100, Σ_rows  base(kind, code) × decay(hops) × vf × cf)
```

- `base(kind, code)` — **code exception first, then tier**:
  - `sanctions_direct` → 100 (OFAC listing; vf=cf=1.0)
  - `label` with code `SANCTIONED` → 85 (community claim ≠ OFAC —
    distinct from `sanctions_direct` by `kind`, mirroring Phase-5
    `baseWeightsV1`; code beats its seeded LEGAL tier — explicit
    precedence)
  - `label` otherwise by `category_type` tier: MALICIOUS_ACTIVITY →
    70; SERVICE_TYPE / ENTITY_TYPE / LEGAL / OTHER → 0
    (classification only — informational; MIXER/GAMBLING/EXCHANGE = 0)
  - `listing` (cross-chain) → 20
- `decay(hops)`: hop1 1.0, hop2 0.5, hop3 0.25, hop4 0.125, hop5
  0.0625 — monotone decreasing (spec §11 reduced weight).
- `vf` (verification): verified 1.0, reported 0.7, unverified 0.5,
  disputed **0.0** (Phase-5 semantics); non-label kinds → 1.0.
- `cf` (confidence): Phase-5 factors reused — high 1.0, medium 0.9,
  low 0.8, unknown 0.7; requires adding `wl.confidence` to
  `BulkMatchLabels` + `BulkMatchLabelsCrossNet` projections (phase-02
  lists it); non-label kinds → 1.0.
- Rounding: per-term INTEGER half-up — decay expressed as
  sixteenths (16,8,4,2,1)/16; term =
  `(base×vf_num×cf_num×decay_num + half)/denom` with integer
  arithmetic, mirroring `engine.go` `weighted()` per-finding integer
  rounding; then integer Σ, then min(100).
- Σ is literal over evidence rows (no per-kind dedup inside the sum);
  `min(100,·)` is the only bound — declared deviation from Phase-5's
  max-not-sum direct block (exposure is additive evidence, not a
  verdict; documented in guidebook).
- **`exposure_score` null-vs-0**: null when the item's signals are ALL
  zero-weight (informational-only) — `0` never appears on the wire.
  No-signal items can't exist (items emit only when sigs > 0). JSON:
  `exposure_score *int` tagged `json:"exposure_score"` WITHOUT
  `omitempty` — key always present, `null` for informational.
- `exposure_policy_version` emitted on the envelope ALWAYS (even when
  `items` is empty).

## 5. Cost bound (worst case)

Per depth d: ≤ MaxNodes_d attempts × MaxPages_d provider steps, all
under the request-global provider budget, edge CAS budget, deadline.
Path assembly per item O(Σ|parents| + MaxPathsPair × d × P) — §2.9.
Post-bound
sanctions/foreign-label lookups per counterparty (≤ edges_examined
5000) run under the caller ctx (implementation choice — strictly
better than the stale deadline→500 residual: caller ctx outlives the
BFS deadline, so these lookups survive a BFS-deadline truncation).
Every bound exits via honest truncation, never unbounded work.

## 6. Tests (pinned — plan v3)

1. 3-hop: `hops=3`, `indirect_hop`, 3-edge path, adjacent-pair
   temporal flag incl. non-monotone middle edge → false.
2. max_hops=5 ok; 0/6 → 400; `POST /screenings options.max_hops`
   still rejects >2.
3. Frontier caps: depth-3 exceeds MaxNodes_3 → `truncated=max_nodes`;
   per-hop coverage fields present and consistent.
4. Min-depth dedup: node reachable at hop-2 AND hop-3 emits at 2 only.
5. Ancestor cycle: A→B→C→A path never materializes; edge to dedup-set member
   excluded — asserted via `frontier_by_hop`/`nodes_examined_by_hop`
   values, provider-call counts, and item absence.
6. Clean intermediary seeds frontier: unmatched depth-2 node's
   counterparty IS a frontier_3 position (B1 Reading-A guard).
7. Sentinel at depth ≥3: `0xdead` cp of a depth-2 node never consumes
   a frontier_3 position or provider call; still emits if signaled.
8. direction=out at max_hops=3: hop-1 filtered, deep edges unfiltered
   (assert an inbound e2 edge is present as evidence).
9. Coverage vocabulary: `counterparties_total` = Σ frontiers;
   `examined+remaining=total`; `positions_emitted` = Σ prefixes;
   `max_depth_reached` semantics (attempted, not emitted);
   `node_indexed_until` spans depths.
10. Cross-chain label: addr labeled on `bsc` while tracing `ethereum`
    → `kind:"label"`, `network:"bsc"`, `listed_address`=the foreign
    `wallets.address` ORIGINAL representation (fixture seeds a
    checksum-case `address` so original ≠ normalized — discriminating),
    `verification_status` verbatim; category never wins;
    solely-foreign item → `category="cross_chain_listing"`; no
    duplicate signals vs `kind:"listing"` sanctions observations.
11. Cross-chain never expands: bsc-only neighbor never a position.
12. Score arithmetic: sanctioned 2-hop vs label 1-hop vs
    cross_chain listing — exact expected ints incl. rounding.
13. Label-kind `SANCTIONED` (85×conf) vs `sanctions_direct` (100) —
    distinct by kind; `disputed` label contributes 0.
14. All-zero-weight signals → `exposure_score: null` (never 0).
15. Score never touches `risk_score`/`scoring_policy_version`.
16. Per-depth index tier: hop-3 node uses MaxPagesHop3 (provider call
    counts asserted); deeper nodes' `indexed_until`/`nodes_failed`
    surfaced.
17. Determinism: identical inputs → identical items AND paths.
18. Mid-sequence unattempted node at depth 3 → honest prefix
    truncation for that depth.
19. Deadline mid-depth-3 → `truncated=deadline`, prefix intact;
    edge/provider budget shared across depths — `max_edges`,
    `max_provider_requests` each reported once.
20. API E2E: seeded 3-hop graph, `max_hops=3`, OpenAPI-valid
    response, `items:[]` never null, `exposure_policy_version`
    always present.
21. Phase-6 suite updated for the new max_hops domain (the two tests
    asserting 3→400 get edited to the new boundary) — all else green;
    max_hops≤2 behavior bit-identical.
22. `max_hops=1` with |frontier_2| > MaxNodes_2 emits NO
    `truncated=max_nodes` (scheduled-frontier guard).
23. ≥4-sequence fixture with ≥2 via₂ roots chosen so frontier order
    ≠ address order: emitted `paths[]` equal the true top-3 by
    ADDRESS-tuple order (discriminates M1‴).
24. Hop-1 ordering with an interleaved sentinel (cpOrder
    [dead, A, B]): item order dead, A, B — minPos domain pinned.
25. Foreign `kind:"label"` contributes to `exposure_score` at full
    weight (e.g. foreign SCAM verified → 70×decay), local category
    untouched.
26. `max_depth_reached`: scheduled-but-zero-attempts (budget gone
    before first attempt) → not counted; attempted-then-clipped
    (deadline mid-expansion, emitted prefix 0) → counted.
27. Per-term rounding boundary: two terms summing across a .5 edge —
    expected int pinned (per-term rounding).
28. Beyond-prefix glimpse: node seen only via a beyond-prefix scan at
    depth 2 but reachable via an emitted path at depth 3 — emits at
    hop 3 (residual documented).
29. `direction=out` + in-only hop-1 cp X (filtered from `cps`)
    reachable via an emitted depth-2 edge: X emits at hop 2 — the
    post-filter dedup domain pinned.
30. Deep sentinel item carries `paths[]` with the contributing edges
    (two-book split — sentinel excluded from positions only).
31. `*_by_hop` maps: keys asserted = frontier indices {2..max_hops};
    per-depth values asserted on a 3-hop fixture.
32. Single-network resolver (foreign set empty): zero foreign rows,
    no error — `ANY('{}')` path.
33. Sentinel seen as cp at level 1 emits its item at hop 1 and never
    re-emits at depth ≥2 (dedup covers sentinels).
34. Hop-3 item minPos = min first-via index in frontier_2 across its
    sequences (domain assertion, not just order).
35. `max_hops=1` coverage values: `counterparties_total=|frontier_2|`,
    `nodes_examined=0`, `nodes_remaining=total`, `max_nodes` absent —
    Phase-6 `len(positions)` parity (M2‴).

## 7. Non-goals

- No `risk_score`/policy change; no >5 hops; no graph expansion
  across networks; no new providers/paid tiers; no microservices;
  `POST /screenings options.max_hops` stays 0–2.

## 8. Residuals (declared)

- direction filters hop-1 only (deep evidence edges are bidirectional
  facts — documented, §2.8).
- Σ additive exposure (no mirror-source dedup inside sum; `min(100)`
  bound) — deviation from Phase-5 max-block, deliberate + documented.
- `which depth hit max_nodes` not represented in the single
  `truncated` reason — precedence vocabulary closed.
- Post-assembly counterparty lookups run under caller ctx — a BFS
  deadline cannot kill them (implementation improvement over the
  residual first drafted).
- Exactly-10k-record streams stay conservatively unfinished
  (Phase-6 carry-over).
- Nodes glimpsed only via beyond-prefix (never-emitted) scans are
  invisible to deeper frontiers — consistent with the emitted-prefix
  frontier domain, recorded via `positions_emitted`.
- Foreign labels score at parity with local (provenance on signal;
  declared §3).
