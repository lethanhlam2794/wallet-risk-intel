# Rebuttal — Critic round 3 (all findings accepted)

**M1″** — pinned the uniform reading with Phase-6-preserving scoping:
`dedup` counterparty clause uses POST-`direction`-filter sets at level
1 (identical to Phase-6 `cps`), unfiltered at deeper levels; the
terminal candidate set is dedup'd identically (min-depth uniform at
every level) but is NOT a scheduled frontier — excluded from
`counterparties_total`, no MaxNodes cap, items bounded by
response_cap/limit. `counterparties_total` redefined over scheduled
frontiers only. Test 29 pins the direction-filtered emit.

**M2″** — two-book split pinned per depth: `cpAgg_d` (all scanned cps,
item+path evidence — sentinels/visited far endpoints retained) vs
`frontier_d` (expansion subset excluding sentinels + dedup). Deep
sentinel items emit WITH paths. Test 30 added.

**M3″** — mechanism repinned: tuple order is via₂-most-significant →
FORWARD lexicographic DFS over `children` lists recorded during
frontier build, pruned by a per-item reverse-reachability mask (one
reverse BFS over parents DAG), cut after MaxPathsPair. Honest bound
O(Σ|parents| + MaxPathsPair×d×P); §5 stale bound replaced.

Minors: m1″ by-hop keys = frontier index {2..max_hops} (terminal set
in none); m2″ per-term INTEGER half-up, decay as sixteenths; m3″ §5
fixed; m4″ tests 22–28+ assigned to phases; m5″ gloss →
"scheduled-but-zero-attempts not counted, attempted-then-clipped
counts"; m6″ MaxPagesFor(1) → defensive root-tier; m7″ `$2`=resolver
active IDs ∖ {request net}, params `$1`addrs/`$2`ids; m8″ test 10
seeds checksum-case original; m9″ title→v3, model.go attribution.

Test gaps → tests 29 (direction-filtered cp), 30 (deep sentinel
paths), 31 (by-hop values), 32 (empty foreign set).
