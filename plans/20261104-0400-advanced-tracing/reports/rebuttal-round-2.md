# Rebuttal — Critic round 2 (all findings accepted)

**B1′** accepted — visited counterparty clause repinned "at scan
levels ≤ d−2, emitted-prefix scans only" with an explicit snapshot
point; candidates (produced at level d−1) no longer self-excluded.

**M1′** accepted — `max_nodes` now scoped to SCHEDULED frontiers
(d ≤ max_hops); max_hops=1 preserves the Phase-6 `maxHops==2` guard.
Test 22 added.

**M2′** accepted — minPos two domains pinned explicitly: hop-1 =
cpOrder rank (sentinels included, unchanged); hop-d≥2 = min first-via
index in frontier_2 (generalizes viaPos). Test 24 added.

**M3′** accepted — pre-truncation dropped (it could lose the true
first tuple at d≥4); enumeration is lazy best-first in tuple order,
cut after MaxPathsPair; honest bound O(MaxPathsPair×d×P). Test 23
pins emitted tuples.

**M4′** accepted — evaluator consumes evidence ROWS (not wire
signals): rows carry `category_type`; SANCTIONED code-exception
precedence pinned ("code first, then tier"); `wl.confidence` added
to both label queries → two-factor `vf×cf` matching Phase-5 `weighted()`.

**M5′** accepted — visited counterparty clause scoped to emitted-prefix
scans; the glimpse blackout is declared residual + coverage field.

**M6′** accepted — query now `network_id = ANY(resolver foreign IDs)`;
resolver has a real runtime role (ID set); per-depth union call.

**M7′** accepted — `max_depth_reached` repinned as deepest frontier
index ATTEMPTED (0 = none; built-never-attempted and empty-prefix not
counted; ceiling = max_hops). Test 26 added.

Minors: m1′ per-term rounding pinned; m2′ `MaxPagesFor(frontierIdx)`;
m3′ keep `TXINDEX_MAX_PAGES_HOPN` name, new HOP3; m4′
`MaxNodesPerDepth [4]int` + shared Concurrency; m5′ "strictly
increases"; m6′ `listed_address` = foreign `wallets.address`; m7′
parity declared; m8′ guidebook/feature-map touch added to phase-02;
m9′ JSON tag `json:"exposure_score"` no omitempty; m10′ vacuous bullet
removed; m11′ test 21 reworded + MaxHops doc comment in scope; m12′
signals() foreign-row distinction noted in phase-02.

Test gaps: tests 22–28 added covering every flagged case.
