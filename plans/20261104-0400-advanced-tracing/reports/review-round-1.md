# Review — Phase 7 Advanced Tracing, round 1

**Verdict: APPROVED**

Risk class: **R3** — API behavior change, indexing path change, concurrent
graph semantics (engineering-standard). Reviewed against the actual working
tree (Phase-7 files are untracked; reviewed full file contents, not diffs).
Reviewer ran all gates independently — no claim is taken from cook-report
prose.

## Commands run (this reviewer, not Cook's)

| Gate | Result |
|---|---|
| `go test -count=1 ./internal/tracing/ ./internal/transaction/ ./tests/` | PASS — tracing 16.6s, transaction 7.2s, tests 46.1s |
| `go test -race -count=1 ./internal/tracing/` | PASS — 30.6s, no races |
| `go vet ./...` | clean |
| `go build ./...` | clean |
| `gofmt -l .` | empty |

The test suite runs against a real Postgres (5433) + Redis with only the
`provider.TxProvider` seam stubbed (returns real `TransferPage`/`UpstreamCalls`
shapes — honors the runtime contract, not a distorting mock).

## Findings

No blockers, no majors. Two minors (both OpenAPI documentation), four nits.

### minor

1. **OpenAPI description points at the wrong field path for `truncated`.**
   `api/openapi.yaml:524` reads "`coverage.truncated` names the binding
   reason" — the wire field is `data.truncated`, a sibling of `coverage`
   (`internal/tracing/model.go:158`; example at `openapi.yaml:623` correctly
   places it). Description-only defect; schema + wire agree.

2. **OpenAPI example is internally inconsistent.** `api/openapi.yaml:620`
   shows `transaction_evidence: []` for an item whose `paths[]` carries 3
   edges — the real wire emits the deduped union (3 edges;
   `internal/tracing/service.go:442,469` → `dedupEvidence` at
   `service.go:936-952`). Schema-valid but factually misleading for
   integrators; the fixture mirrors `TestExposuresAPI_Advanced`, which does
   not assert this field — the gap went unnoticed.

### nit

3. By-hop map domains are documented as `{2..max_hops}`
   (`api/openapi.yaml:1823,1843`) but the implemented (and plan-pinned,
   plan.md:128-143) domain is `{2..max(max_hops,2)}` — at `max_hops=1` the
   materialized frontier_2 appears with zero attempts. Implementation is
   correct; the YAML text is loose.

4. `.env.example` carries none of the Phase-6/7 knobs
   (`ETHERSCAN_API_KEY`, `NODEREAL_API_KEY`, `CHAINSCAN_*`, `TXINDEX_*`,
   `TRACING_*`) — partly pre-existing (Phase-6 vars), partly new
   (`TXINDEX_MAX_PAGES_HOP3`, `TRACING_MAX_NODES_{2..5}`).

5. `internal/platform/metrics/metrics.go` — `tracing_nodes_examined_total`
   help still says "Hop-2 expansion nodes attempted" though the counter now
   covers all depths.

6. Plan §5 residual text is stale: post-BFS evidence lookups now run under
   the caller ctx (`service.go:140-143,383-411`), so a BFS deadline can no
   longer surface as a 500 there — strictly better than the declared
   residual and documented in cook-report:39-40. No action needed; flagged
   so the plan record isn't misread later.

## The cook deviation — pull-gate removal (verified clean)

Phase-6 gated each ordered-position pull on `budget.Exhausted()`. Cook
removed the loop-top gate; positions now always pull and let
`EnsureIndexed`'s own `tryTake` decide. Verified against the code and the
dedicated runtime test:

- **Never-attempted accounting is preserved.** Pull increments `examined`
  before `EnsureIndexed` (`service.go:249-250`); the `out.BudgetExhausted`
  path decrements both counters (`service.go:270-276`), leaves `done=false`,
  and the position joins `nodes_remaining` (`service.go:528`, per-hop
  `service.go:623-627`). `st.examined` counts attempts only.
- **A never-attempted position causes no indexing side effects.** In
  `EnsureIndexed`, `tryTake(1)` (transaction/service.go:249) precedes
  `HeadBlock`, `MarkWalletIndexing` (:290) and all terminal writes; the
  budget path only fills persisted state into the outcome (:252-257). Two
  pre-budget writes DO exist — `wallets.GetOrCreate`
  (transaction/service.go:139) and the provider-switch
  `ResetWalletIndexSource` (:191-208) — but both are free-path hygiene: the
  reset only *clears* a claim the current adapter cannot honor (never
  overclaims coverage), and a bare wallet row asserts nothing. Verified
  benign.
- **The intended new semantic is real and bounded:** a position that
  resolves free from persisted index state completes `done=true`, extends
  the emitted prefix, and seeds `frontier_{d+1}` — "the pool gates provider
  calls, not pull-through resolution of already-indexed state." This cannot
  spin unboundedly: pulls are capped by `maxNodes` per depth
  (`service.go:242`), workers exit after their first post-exhaustion attempt
  (`service.go:306-308`), non-free pulls short-circuit at `tryTake`.
- **No coverage overclaim.** Free-beyond-budget resolutions count as
  `nodes_examined` (real attempts against persisted state) and earn
  `node_indexed_until` entries (`service.go:284`); their scanned edges are
  discarded from `cpAgg`/`parents`/`children` when they land beyond the
  emitted prefix (`service.go:333` iterates `i < st.emitted` only).
  `edges_examined` still counts the discarded scans — matching Phase-6's
  own pinned "attempted incl. discarded beyond-prefix" wording
  (phase-6 plan.md:271).
- **Runtime-pinned, not just asserted in prose:**
  `TestExposures_BeyondPrefixGlimpse` (service_test.go:1217-1350) holds a
  `wallet_index_state` row lock so position `a2` stalls on the provider-
  switch reset while `a1/a3/a4` drain the 8-call pool; `a2` returns
  `BudgetExhausted` (pulled, never attempted, `stub.called(a2)=0`), the
  emitted prefix stops at `a1`, the beyond-prefix glimpse of `cpX` never
  seeds `frontier_3`, and pre-indexed `viaY` resolves at depth-3 for zero
  provider calls (`provider_calls=8` exactly). Both halves of the deviation
  are exercised. `TestExposures_PrefixTruncationDeep` + `MaxDepthReachedBudget`
  pin the complementary scheduled-zero-attempt accounting.

## Pinned-semantics verification (all hold)

- **Reading-A frontier** — `cpAgg_{d+1}` built only from emitted-prefix
  scans (`service.go:331-353`); clean intermediaries seed deeper frontiers
  (`ForeignLabel` test's clean `cpB` → `frontier_3`); beyond-prefix scans
  preserve no parents/children.
- **Dedup** — snapshot = root ∪ post-direction-filter level-1 counterparties
  ∪ frontier positions ≤d (registered at `service.go:224` before level-d
  scans) ∪ emitted scans' counterparties ≤d−1 (`service.go:362-366`);
  dedup'd far endpoints excluded from *candidacy* (`service.go:337`), not
  just expansion. Sentinel re-emission is dedup-covered
  (`SentinelNoReemit`).
- **Two-book** — `candSet.order` (all candidates incl. sentinels) vs
  `positions` (non-sentinel subset) at `service.go:648-672`; deep sentinels
  emit items with real path evidence, consume zero provider calls
  (`DeepSentinel`, `SentinelOrder`).
- **Ordering** — candidates by `unionFirstEdge` compared `(ts, tx_hash,
  addr)` (`service.go:653-663`); paths via reverse-reachability mask +
  forward DFS from via-2 seeds in ascending **address** order =
  via₂-most-significant tuple order over the full set, cut at
  `MaxPathsPair` (`service.go:693-798`). `PathTupleOrdering` is the
  discriminating fixture (frontier order ≠ address order; `(hi,Q)` tail
  drops).
- **Coverage** — materialized (`mapDomain = max(max_hops,2)`) vs scheduled
  ({2..max_hops}) domains (`service.go:514-527`); `max_nodes` fires only on
  scheduled frontiers exceeding the per-depth cap (`MaxHops1Coverage`:
  |frontier_2|=3 > cap 1 at max_hops=1 → `truncated=nil`);
  `examined+remaining=total` floored ≥0; `max_depth_reached` counts ≥1
  attempt incl. attempted-then-clipped, excludes scheduled-zero-attempt
  (`MaxDepthReachedBudget`, `DeadlineDeep`); `node_indexed_until` covers
  attempted positions only, incl. failed (null watermark).
- **Scoring exposure-v1** — per-term integer half-up
  `(base×vf×cf×decay + 800)/1600`, literal Σ, cap 100, null-if-all-zero
  (`score.go:91-129`); code-exception `SANCTIONED=85` precedes tier;
  `MALICIOUS_ACTIVITY=70`; classifications 0; listings 20; disputed vf=0.
  `ExposureScoreRounding` pins the .5 boundaries (24.5→25; two terms → 50;
  59.5→60). `exposure_score` never emits literal 0; never touches
  `risk_score`/`scoring_policy_version` (asserted at the JSON level,
  service_test.go:841-846).
- **Foreign labels** — `BulkMatchLabelsCrossNet` matches normalized address
  on resolver-active foreign nets, selects `w.address AS listed_address`
  (original representation preserved — checksum-case fixture proves it);
  signals carry `network`+`listed_address`, skip `consider()`
  (`service.go:1060-1066`), solely-foreign → `cross_chain_listing`
  (`service.go:1086-1088`), score at full label weight (declared parity);
  BFS never calls providers for foreign nodes (positions derive from
  same-net edges only). `SingleNetResolver` covers `ANY('{}')`.
- **Direction** — hop-1 edge set only (`service.go:181-186`); deep scans
  bidirectional; post-filter dedup domain lets a filtered hop-1 cp emit at
  hop-2 (`DirectionDedupDomain`, `DirectionScope`).
- **Path evidence** — `path_verified` constant false (`model.go:95`);
  `hop_count = len(edges)`; adjacent-pair temporal validity
  (`service.go:789-792`); `pickEdge` rule ts→amount→(tx_hash,
  transfer_index) (`service.go:869-891`); `transaction_evidence` deduped
  union by `(tx_hash, transfer_index)`.
- **Bounds** — `max_hops ∈ {1..5}` default 2, 0/>5 → 400
  (`service.go:1122-1125`, handler `transactions.go:139-148`); screening
  `options.max_hops` still rejects >2 and feeds only `scoring.Evaluate`,
  never tracing (screening/service.go:214-216 — untouched semantics).
- **Truncation** — post-assembly precedence `deadline >
  max_provider_requests > max_nodes > max_edges > response_cap > limit`
  (`service.go:563-576`) matches plan.md:151-152 verbatim; `max_edges`
  guard `edgesLeft≤0 && (clipped || remaining>0)` is equivalent to the
  pinned `clipped || (edgesLeft≤0 && remaining)` because every clip path
  drains `edgesLeft` to ≤0 (`service.go:822-834`).
- **Wire honesty** — `items := []Item{}` never null (`service.go:419`,
  asserted `items:[]` E2E); `exposure_policy_version` always emitted;
  `tracing_method=bounded_bfs`; disabled provider → `provider_status:
  "disabled"`, `index_status:"disabled"`, empty items, never a clean result
  (live-exposures-disabled.json capture).

## Bit-identity at max_hops ≤ 2

Phase-6 regression tests unchanged in behavior (the only edits are the two
boundary tests moved from `>2`→400 to `>5`→400, per plan test 21):
`Hop1Labeled`, `Hop2`, `EdgeRules` (sentinel emits, self-edge dropped,
root-returning edge produces no item), `Bounds` (max_nodes /
max_provider_requests / max_hops=1 no-expansion), `SanctionsPrecedence`,
`Deterministic` (first-edge position order), `EdgeClipReported`,
`InfraErrorPropagates`, `Hop2DedupBySequence` (sequence dedup + pickEdge).
Path ordering generalizes Phase-6's own pin ("3 lexicographically-first
node sequences", phase-6 plan.md:260-261) — identical at d=2. Coverage
vocabulary at `max_hops=1` reproduces the Phase-6 contract verbatim
(examined=emitted=failed=0, remaining=total — `MaxHops1Coverage`).
Sentinel lifecycle, direction interplay, truncation reasons identical.

## Honesty & concurrency

- Loops are bounded everywhere: pulls ≤ min(positions, maxNodes);
  `pathsFor` DFS is depth-bounded (children only point d→d+1, path length
  strictly increases) and capped at MaxPathsPair; `scanEdges` CAS can never
  overshoot MaxEdges.
- Provider failure never becomes clean: failed/disabled → `nodes_failed`
  (`service.go:287-291`); infra errors propagate as errors, never fake a
  `deadline` (`service.go:258-268`, `InfraErrorPropagates` closes the pool
  mid-flight); unknown≠safe (no-data items simply don't emit; scores are
  null, not 0).
- Shared state is race-clean (`-race` suite green): all cross-goroutine
  state is atomics (`edgesLeft`, `edgesClipped`, `edgesExamined`,
  `examined`, `failed`, `deadlineHit`, `nextPos`, `ex`), `sync.Map`
  (`nodeUntil`), or disjoint `st.results[i]` elements joined by
  `g.Wait()`; `sets`/`dedup`/`children`/`posDepth`/`levels` are only
  written by the main goroutine between levels; `sentinels` is a read-only
  package map. Worker pool drains deterministically on budget exhaustion,
  deadline, or queue end — no leaks.
- Carried-over residuals (declared Phase-6/7, unchanged):
  `max_provider_requests` can name at `max_hops=1` when the root alone
  drains the pool (frontier_2 materialized → remaining>0); over-cautious
  `deadline` marking if the deadline lapses between level completion and
  the post-check (`service.go:317-319`) — under-claims, safe direction;
  singleflight can propagate a sibling request's ctx cancellation as
  `deadline` (approximately honest).

## Recommendation

Approve. The implementation matches every pin; the declared deviation is
sound, runtime-pinned, and introduces no new semantics beyond its stated
residual. Fix the two OpenAPI documentation defects (findings 1–2) at
commit time — they misdescribe an otherwise correct contract.
