# Cook Report — Phase 7 Advanced Tracing

Implements `plan.md` v3 + `phase-01-frontier-and-scoring.md` +
`phase-02-crosschain-and-api.md` (PLAN_CONSENSUS_REACHED; pinned
semantics unchanged).

## 1. What was built

### Frontier generalization (§2.1–§2.6)

`internal/tracing/service.go` was rewritten around a per-depth
expansion loop `d = 2..max_hops` (was a single hop-2 loop):

- `cpAgg_d`/`frontier_d` two-book split at every depth — `candSet`
  carries `cands` (all item/path candidates incl. sentinels) and
  `positions` (expansion subset after sentinel + dedup exclusion).
- `frontier_{d+1}` built from **all scanned edges of the emitted-prefix
  nodes at d** (Reading A) — clean intermediaries seed deeper
  frontiers; beyond-prefix scans never record `parents`/`children`.
- `dedup` snapshotted per level build: root ∪ queued frontier
  positions ≤ d ∪ post-filter counterparty sets of emitted-prefix
  scans ≤ d−2 (level-1 set is post-`direction`-filter = Phase-6 `cps`;
  deep sets unfiltered). Dedup-set far endpoints are excluded from
  candidacy entirely — min-depth emission, uniform at the terminal set.
- §2.3 ordering key over the union of all parent contributions:
  firstEdge (ts → tx_hash → transfer_index) then candidate address.
- Per-depth caps `Config.MaxNodesPerDepth [4]int`; `defaults()` fills
  `TRACING_MAX_NODES_{2..5}` with the legacy `TRACING_MAX_NODES` as the
  depth-2 fallback. `Concurrency` shared across depth loops.
- `transaction.Service.MaxPagesFor(frontierIdx int)`: 0/root → Hop0
  fallback, 2 → HopN, ≥3 → the new shared `MaxPagesHop3` tier
  (`TXINDEX_MAX_PAGES_HOP3`, default 1).
- Forward-DFS path enumeration over `parents`/`children` (≤3 sequences
  per item, tuple order via₂-most-significant by address); every path
  reports `temporal_order_valid` = adjacent-pair nondecreasing ts.
- `direction=` filters the hop-1 edge set only; deep scans are
  bidirectional facts.
- Request-global provider pool + edge CAS budget + deadline shared
  across all depths; `bctx` bounds BFS work only (evidence/assembly
  under caller ctx).

### Coverage vocabulary (§2.7)

`counterparties_total` = Σ |frontier_d| over materialized indices
{2..max_hops} (frontier_2 materialized unconditionally → `max_hops=1`
Phase-6 parity); `positions_emitted` = Σ contiguous prefixes;
`max_depth_reached` = deepest frontier with ≥1 attempt; new per-hop
maps `frontier_by_hop`, `nodes_examined_by_hop`,
`frontier_emitted_by_hop`, `nodes_remaining_by_hop`; `nodeUntil` spans
attempted positions at every depth.

### exposure-v1 scoring (§4)

`internal/tracing/score.go`: per-term integer half-up rounding under
denom 1600 — `base × decay(sixteenths) × vf(tenths) × cf(tenths)` —
Σ literal over evidence rows (declared Σ-additive deviation), cap 100,
`nil` when all terms are 0. Bases: `sanctions_direct` 100, label code
`SANCTIONED` 85, `MALICIOUS_ACTIVITY` label 70, other tiers 0,
sanctions `listing` 20. `exposure_policy_version="exposure-v1"` on
every `Result`; `exposure_score` never feeds `risk_score`.

### Cross-chain labels (§3)

`queries/transactions.sql`: `BulkMatchLabels` gains `wl.confidence`;
new `BulkMatchLabelsCrossNet` (`$1=addrs`, `$2=foreign network_ids`,
projects `n.code AS network_code`, `w.address AS listed_address`) —
`sqlc generate` run, generated file untouched by hand. `tracing.New`
takes `resolver *network.Resolver`; foreign rows emit `kind:"label"`
signals with `network` + `listed_address` (original representation),
skip `consider()` (never local category winners), score at full label
weight; solely-foreign items → `category="cross_chain_listing"`. BFS
never crosses networks — foreign addrs never become positions or
provider calls.

### Wiring

- `cmd/api/main.go`: existing resolver passed to `tracing.New`.
- `internal/httpserver/transactions.go`: `max_hops` validation 1–5.
- `api/openapi.yaml`: summary/description, `max_hops` enum [1..5] + 400
  text, item `hops` enum [1..5], nullable `exposure_score`,
  `exposure_policy_version`, per-hop coverage maps, worked example
  (3-hop cross-chain item).
- `internal/screening` untouched — `options.max_hops` stays 0–2.

## 2. Test matrix (plan §6, all 35 pinned + Phase-6 suite)

| # | Where | Result |
|---|---|---|
| 1 (3-hop, temporal) | `TestExposures_Hop3` | PASS |
| 2 (max_hops domain, screenings ≤2) | `TestValidateRequest`; `TestTransactionsAPI`; `TestScreenings_MaxHopsRejected` | PASS |
| 3 (per-depth max_nodes) | `TestExposures_DepthCapMaxNodes`; `TestExposures_Bounds/max_nodes` | PASS |
| 4 (min-depth dedup) | `TestExposures_MinDepthDedup` | PASS |
| 5 (ancestor cycle) | `TestExposures_CycleExclusion` | PASS |
| 6 (clean intermediary seeds) | `TestExposures_Hop3`, `TestExposures_ForeignLabel` | PASS |
| 7 (deep sentinel) | `TestExposures_DeepSentinel` | PASS |
| 8 (direction root-only) | `TestExposures_DirectionScope` | PASS |
| 9 (coverage vocabulary) | `TestExposures_Hop3`, `TestExposures_MaxHops1Coverage` | PASS |
| 10 (foreign label provenance) | `TestExposures_ForeignLabel`, `TestExposuresAPI_Advanced` | PASS |
| 11 (no cross-net expansion) | `TestExposures_ForeignLabel` | PASS |
| 12–14 (score arithmetic, kind split, null) | `TestExposures_ExposureScore`, `TestExposures_ExposureScoreNull`, `TestExposureScoreRounding` | PASS |
| 15 (risk_score untouched) | `TestExposures_ExposureScoreNull`; `TestExposuresAPI_Advanced` (payload has no `risk_score`) | PASS |
| 16 (per-depth index tier) | `TestExposures_PageTiers`; `TestService_MaxPagesFor` (tx) | PASS |
| 17 (determinism) | `TestExposures_DeterministicDeep` | PASS |
| 18 (mid-sequence unattempted) | `TestExposures_PrefixTruncationDeep` | PASS |
| 19 (deadline mid-depth) | `TestExposures_DeadlineDeep` | PASS |
| 20 (API E2E) | `TestExposuresAPI_Advanced` | PASS |
| 21 (Phase-6 bit-identity) | full suite updated for 1–5 domain, all green | PASS |
| 22 (max_hops=1 no max_nodes) | `TestExposures_MaxHops1Coverage` | PASS |
| 23 (tuple ordering) | `TestExposures_PathTupleOrdering` | PASS |
| 24 (sentinel minPos) | `TestExposures_SentinelOrder` | PASS |
| 25 (foreign full weight) | `TestExposures_ForeignLabel` | PASS |
| 26 (max_depth_reached) | `TestExposures_MaxDepthReachedBudget` (26a), `TestExposures_DeadlineDeep` (26b) | PASS |
| 27 (per-term rounding) | `TestExposureScoreRounding` | PASS |
| 28 (beyond-prefix glimpse) | `TestExposures_BeyondPrefixGlimpse` | PASS |
| 29 (post-filter dedup domain) | `TestExposures_DirectionDedupDomain` | PASS |
| 30 (sentinel path evidence) | `TestExposures_DeepSentinel` | PASS |
| 31 (*_by_hop key domain) | `TestExposures_Hop3`, `TestExposures_MaxHops1Coverage` | PASS |
| 32 (single-network resolver) | `TestExposures_SingleNetResolver` | PASS |
| 33 (sentinel no re-emit) | `TestExposures_SentinelNoReemit` | PASS |
| 34 (hop-3 minPos domain) | `TestExposures_Hop3MinPos` | PASS |
| 35 (max_hops=1 parity) | `TestExposures_MaxHops1Coverage` | PASS |

Phase-6 domain tests updated only where the pinned contract changed:
`max_hops` 0/6 → 400 and 1–5 → 200 (API), `ValidateRequest` accepts
1–5, `max_hops=1` coverage counts materialized frontier_2. All other
Phase-6 cases run unchanged and green — bit-identical at `max_hops≤2`.

## 3. Deviations

1. **Worker-loop `budget.Exhausted()` pull-gate removed.**
   The per-depth loop no longer checks `budget.Exhausted()` before
   `nextPos` pulls; positions are still attempted in order and the
   `out.BudgetExhausted → examined−1` path performs identical
   never-attempted accounting. Justification: the pinned §6-28
   "resolved-beyond-prefix" scenario is *inexpressible* with the gate —
   `tryTake` fails iff `used ≥ limit`, which is exactly the gate's
   condition, so any budget gap also blocks the next level's pulls,
   including zero-cost fresh-serve resolutions of already-indexed
   nodes. The provider pool bounds provider *calls*, not pull-through
   resolution of persisted index state. `§6-26a` (scheduled-but-zero-
   attempts) is unaffected — `BudgetExhausted` still reports the same
   examined/remaining/provider-call values.

2. **Test-28 fixture uses `wallet_index_state` lock, not `wallets`.**
   Holding a row lock on `wallets` also blocks the root index's
   `BulkResolveWallets` batch (a2 is a counterparty), freezing the
   whole pipeline. Instead a2's `wallet_index_state` is seeded with a
   foreign `source_id` (`nodereal`) and locked: its provider-switch
   `ResetWalletIndexSource` UPDATE stalls *before* the head `tryTake`,
   `wallets` is untouched, and the released attempt deterministically
   finds the drained pool. viaY is pre-indexed fresh so the depth-3
   pull resolves it for free.

3. **`sanctions_snapshot_version`/`labels_snapshot_version` now `null`
   in the regenerated disabled-lifecycle capture.** The capture was
   regenerated through the real engine+DB (REGEN_CAPTURES harness);
   the test environment holds no published snapshots — honest `null`,
   matching the disabled-provider theme of the artifact.

## 4. Verification

```
gofmt -l .     → (empty)
go build ./... → ok
go vet ./...   → ok
go test ./...  → all packages PASS
                 internal/tracing 22.7s, internal/transaction 6.4s,
                 tests 40.6s (incl. new §6-20 E2E)
```

Docs updated: `docs/guidebook-transaction-tracing.md`,
`docs/feature-maps/transaction-tracing.md`, regenerated
`docs/feature-maps/assets/live-exposures-disabled.json` +
`live-transactions-disabled.json` (`REGEN_CAPTURES=1` harness kept in
`tests/transactions_api_test.go`).
