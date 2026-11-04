# Devin Critic — Round 4: Advanced Tracing plan challenge (v3)

Scope: `plans/20261104-0400-advanced-tracing/` (plan.md v3 + 2 phase
files + `reports/rebuttal-round-3.md`), verified against
`internal/tracing/{service,model,service_test}.go`,
`internal/transaction/service.go`, `queries/transactions.sql`,
`internal/scoring/{policy,engine}.go`,
`internal/httpserver/transactions.go`, `internal/network/resolver.go`,
`internal/config/config.go`, `cmd/api/main.go`, `api/openapi.yaml`, and
`tests/transactions_api_test.go`.

## Round-3 findings — resolution status

- **M1″ RESOLVED** (with one new defect — M2‴). The uniform reading is
  pinned: terminal candidate set dedup'd identically, unscheduled, no
  MaxNodes cap, items bounded by response_cap/limit; the level-1
  counterparty clause is POST-direction-filter (Phase-6 `cps`), deeper
  levels unfiltered; test 29 pins the direction-filtered deep emit.
- **M2″ RESOLVED.** Two-book split pinned per depth: `cpAgg_d` retains
  sentinel + dedup far endpoints for item/path evidence; `frontier_d`
  excludes them from positions. Test 30 discriminates. One wording
  conflict survives (m1‴).
- **M3″ PARTIALLY RESOLVED.** The mechanism is now workable — forward
  adjacency via `children` lists, per-item reverse-reachability mask,
  honest bound O(Σ|parents| + MaxPathsPair×d×P), §5 restated. But the
  level-0 iteration order is still pinned wrong (M1‴): the described
  enumeration emits a non-tuple order under reachable inputs.
- Minors m1″–m9″: resolved in plan text; two propagation leftovers
  (m2″ → phase-01 "hundredths"; m6″ → param name `depth`) plus residual
  cosmetics (m3‴).
- Test gaps → tests 29–32 exist, each discriminates its target, and
  phase ownership is now complete (phase-01: 1–5,8,9,12–19,21–24,26–31;
  phase-02: 6,7,10,11,20,25,32 — all 32 covered).

## Fresh hunt — the seven prompt questions

1. **Two-book split.** Mechanically sound: root lands in `cpAgg_d` via
   "ALL scanned counterparties" and is suppressed by dedup (Phase-6's
   `cp2==root → continue` equivalent); self-edges excluded explicitly;
   sentinel far endpoints retain (parent, edge) contributions and emit
   items with real path evidence at their min depth. The sentinel-in-
   dedup case (hop-1 sentinel re-glimpsed at depth ≥3) resolves
   correctly through §2.4 — emit once at min depth — but §2.2's gloss
   contradicts §2.4 for exactly this case (m1‴) and the composition is
   untested (g3‴).
2. **Post-filter dedup domain.** Implementable: the level-1 emitter is
   root — "always resolved" (service.go:106 comment) — so "emitted-
   prefix node's scanned edges at scan level 1" evaluates to the root
   scan, and "post-filter" yields Phase-6 `cps` verbatim (sentinels
   included). Deeper levels contribute `cpAgg_{k+1}` keys of emitted-
   prefix scans — unfiltered per pin. The direction interplay holds
   under the natural reading (the filter removes edges from the SET
   before either book, §2.8) — one hardening nit (m6‴).
3. **Forward-DFS + mask.** Implementable as stated over `parents` +
   `children`: parents DAG ⊆ emitted prefix by construction (Reading
   A — beyond-prefix scans are never recorded); the mask is one
   reverse BFS from cp over positions; children lists are addr-sorted;
   inside the mask there are no dead ends, so the per-item bound
   O(Σ|parents| + MaxPathsPair×d×P) is honest; aggregate bounded by
   ≤5000 item candidates. BUT the level-0 iteration order contradicts
   the pinned output — M1‴ — and test 23 does not yet discriminate it
   (g1‴).
4. **Terminal set.** Bounded: |cpAgg_{max_hops+1}| ≤ edges scanned at
   the terminal level ≤ `edges_examined` ≤ 5000 (request-global CAS);
   per-candidate label/sanctions/foreign lookups bounded under the
   same ctx (deadline → 500 declared residual); item count bounded by
   `response_cap`/`limit` (§2.4 sentence, §2.7 precedence). Declared
   and honest.
5. **`examined ⊆ scheduled positions`.** Guaranteed: attempts occur
   only inside scheduled-frontier worker loops (`nextPos` pull +
   examined++ around `EnsureIndexed`, BudgetExhausted race → −1 =
   never attempted); dedup guarantees single-frontier membership.
   `examined + remaining = total` holds — **except the total side
   itself is mis-defined at max_hops=1** (M2‴).
6. **Tests 29–32.** Each discriminates (29 → post-filter dedup domain;
   30 → two-book sentinel evidence; 31 → by-hop key domain + values;
   32 → `ANY('{}')` empty foreign set). Remaining gaps: g1‴–g4‴.
7. **Stale text.** §6 heading still reads "(pinned — plan v2)";
   phase-01 carries "hundredths + half-up", "unified visited set",
   `MaxPagesFor(depth int)`; §2.4 heading says "Visited set" while the
   body uses `dedup`.

## Part 1 — Majors

### M1‴. §2.9 enumerates frontier_2 "in frontier order" — via₂ must iterate in ADDRESS order; as written the emitted sequences break tuple order (and the max_hops≤2 bit-identical pin)

plan.md:175-176: "sequences emit via lexicographic DFS from frontier_2
positions **in frontier order**, following sorted children lists".

Tuple order is pinned as "lexicographic over the address tuple
(via₂,…,via_d), root excluded" — via₂ most significant. For the
emitted ≤MaxPathsPair sequences to equal the true first tuples, the
level-0 iteration — the via₂ candidates (frontier_2 ∩ mask) — must run
in ascending ADDRESS order. "Frontier order" is the §2.3 scheduling
key (firstEdge.ts, firstEdge.tx_hash, addr): a different, separately-
pinned order used for position pulling and the contiguous prefix —
not for tuple enumeration.

Concrete break at d=2 (so the max_hops≤2 pin is hit, not just deep
hops): root edges root→b (ts=1) and root→a (ts=2), with a<b as
addresses; cp reachable via both. Phase-6 sorts seqs by via address
(service.go:397) and emits paths[] [a-path, b-path]. DFS "in frontier
order" starts at b (earlier firstEdge) → emits [b-path, a-path]:
different paths[] order on the wire. With ≥4 via₂ roots the
MaxPathsPair cut also selects the wrong SET — a lexicographically-
smaller via₂ sitting later in frontier order is cut while a frontier-
earlier larger one is kept — violating "the first 3 sequences in
tuple order over the FULL sequence set".

Same class as M3″ — a described mechanism that produces a different
output than the pinned one — though the repair is local: iterate
frontier_2 ∩ mask in ascending address order (the same ordering the
children lists already use); "frontier order" belongs only to
scheduling.

### M2‴. `counterparties_total` scheduled-only formula yields 0 at max_hops=1 — Phase-6 reports len(positions); the "unchanged" claim fails on the wire

§2.7: "`counterparties_total` = Σ |frontier_d| over SCHEDULED
frontiers (d ≤ max_hops; positions queued for expansion…)". The same
section then defines scheduling: "frontier_2 built but not scheduled
(max_hops=1) never fires [max_nodes]" — i.e. at max_hops=1 frontier_2
is explicitly unscheduled, so the Σ is empty:

- v3 literal: `counterparties_total` = 0, `nodes_remaining` = 0−0 = 0.
- Phase-6 (service.go:166,469-472): `counterparties_total` =
  len(positions) unconditionally, `nodes_remaining` = same at
  max_hops=1 — pinned by the existing test
  `max_hops_1_no_expansion` (service_test.go:277-281, asserts
  `NodesRemaining == 1` with one hop-1 cp).

Three statements cannot all hold: the scheduled-only formula, the
"built but not scheduled" vocabulary, and "At max_hops≤2 this equals
today's hop-1 count — unchanged" (plus §1.4's bit-identical pin).
Following the formula literally ships a wire regression on every
max_hops=1 response (the entire hop-1 coverage block collapses to 0).

Repair — decouple "scheduled" (gates attempts + `max_nodes`: strictly
d ≤ max_hops) from "counted" (position book materialized): pin
"`counterparties_total` counts every materialized positions frontier —
frontier_2 always materializes as the hop-1 position book and counts
whether or not expansion runs (Phase-6 semantic); frontiers d ≥ 3
materialize only when scheduled (d ≤ max_hops); the terminal candidate
set never materializes positions." The `*_by_hop` key domain needs the
same clarification or Σ `frontier_by_hop` ≠ `counterparties_total` at
max_hops=1 (m4‴).

## Part 2 — Minors

- **m1‴ §2.2 gloss contradicts §2.4 for dedup members.** "Sentinel and
  dedup-set far endpoints remain here: **they may emit items** with
  real path evidence" vs §2.4 "a candidate already in dedup emits no
  item and no position at the deeper level". Sentinels may emit; dedup
  members may not (they emit at min depth instead). Split the
  sentence — "sentinel far endpoints remain item/path candidates;
  dedup-set far endpoints are retained for evidence but emit no item
  (§2.4)". Also worth stating why dedup members are retained at all
  (harmless dead weight for items) or that retaining-vs-dropping is
  immaterial.
- **m2‴ phase-01:16 stale rounding text.** score.go bullet still says
  "hundredths + half-up rounding" — m2″'s fix (per-term INTEGER
  half-up, decay as sixteenths) landed in §4 but was not propagated.
- **m3‴ Naming leftovers.** §2.4 heading "Visited set" vs the body's
  `dedup`; phase-01:9 "unified visited set"; §6 heading "(pinned —
  plan v2)" under a v3 title; phase-01:19 `MaxPagesFor(depth int)` vs
  plan's `frontierIdx` (m6″ fixed semantics, not the name).
- **m4‴ `*_by_hop` domain vs total at max_hops=1.** Keys pinned
  {2..max_hops} → empty map at max_hops=1; if frontier_2 counts in
  `counterparties_total` (required by M2‴'s repair) the map's domain
  diverges from the total's. Pin the by-hop domain = materialized
  frontiers and state whether Σ `frontier_by_hop` =
  `counterparties_total` must hold.
- **m5‴ `cpAgg_2` "ALL scanned counterparties" readable as pre-filter.**
  At level 1 the direction filter removes edges from the SET before
  either book sees them (Phase-6 filters inside the `cps` build loop,
  service.go:129-134) — §2.4/§2.8 + the bit-identical pin force this
  reading, but a one-liner ("cpAgg_2 = Phase-6 `cps`; the direction
  filter precedes both books") removes the only available misreading.
- **m6‴ `positions_emitted`/`nodes_examined_by_hop` per-depth semantics
  at max_hops=1 unstated.** Under M2‴'s repair, frontier_2 counts in
  the total while contributing 0 to attempted/emitted — already
  consistent with the definitions, but the empty-vs-`{2:0}` map shape
  for zero-attempt scheduled/unscheduled frontiers is unpinned (test
  31 fixes a 3-hop fixture only).

## Part 3 — Test gaps

1. **Test 23 fixture shape unpinned.** To discriminate M1‴ the ≥4
   sequences must span ≥2 distinct via₂ roots AND those roots'
   frontier order must diverge from their address order (otherwise a
   frontier-order implementation passes). Pin: ≥2 via₂ roots, ≥4
   total sequences, via₂ frontier-order ≠ address-order. No existing
   test pins paths[] ORDER for multi-via items even at d=2 — the
   tuple-order contract rests entirely on this test.
2. **Dedup'd sentinel emits once at min depth** — hop-1 sentinel
   re-glimpsed at depth ≥3 emits no second item. Composition of
   §2.2+§2.4; untested (tests 4/7/30 cover the parts, not the
   collision case m1‴ names).
3. **hop-d≥3 minPos domain** — "min over contributing sequences of the
   FIRST via's position index in frontier_2": needs two hop-3 items
   whose order differs under via₂-index vs via₃-index (or vs
   counterparty). Test 17 pins determinism, not the rule.
4. **max_hops=1 coverage under the new vocabulary** — the existing
   `max_hops_1_no_expansion` test catches M2‴ only if it survives the
   v3 rewrite untouched; pin explicitly that `counterparties_total`,
   `nodes_remaining` keep Phase-6 values at max_hops=1 (folds into
   test 21's "all else green" but is worth naming given the formula
   change).

## Part 4 — Positive confirmations

- dedup_d under the uniform reading reproduces Phase-6 exactly:
  dedup_3 = {root} ∪ frontier_2 positions ∪ post-filter level-1 set =
  {root} ∪ `cps` — identical to service.go:306-311's exclusion domain.
  Direction interplay holds: filtered cps never enter dedup via level
  1 (test 29), deeper levels contribute unfiltered sets (§2.8 residual
  stands).
- The reachability mask makes the honest bound work: inside the mask
  every node has a completing suffix, so DFS visits ≤ ~MaxPathsPair×d
  mask nodes × P child-scans; mask build ≤ Σ|parents| ≤ edges_examined;
  aggregate ≤ 5000 items × that — bounded, ctx-deadline-guarded.
- Terminal set is bounded and its bindings are declared (5000-edge
  ceiling; response_cap/limit precedence in §2.7).
- `examined ⊆ scheduled positions` holds by construction (attempts
  only inside scheduled loops; dedup → single frontier membership);
  the ≥0 floor stays defensive.
- Scoring arithmetic verified end-to-end under the integer rule:
  (85×10×10×16+800)/1600 = 85 (SANCTIONED label, hop-1);
  (70×10×10×4+800)/1600 = 18 (SCAM verified/high, hop-3, 17.5→18);
  (20×10×10×1+800)/1600 = 1 (listing, hop-5, 1.25→1) — min non-zero
  term ≥1 ⟹ Σ=0 ⟺ all-zero-weight ⟺ null: the `0`-never-on-wire
  invariant is real. `weighted()` parity holds (same (num+half)/denom
  shape).
- Cross-chain: `BulkMatchLabelsCrossNet` $1/$2 order pinned against
  the existing convention inversion; `ANY('{}')`/NULL both yield zero
  rows (test 32); resolver exclusion `∖ {request net}` pinned;
  `CatCrossChain` reuse for solely-foreign-label items is deliberate
  and consistent (kind distinguishes signal provenance).
- All 32 pinned tests have phase owners; phase-02's list covers the
  httpserver error strings ("must be 1 or 2" ×2) and OpenAPI summary/
  enum/example updates.

## Assessment

The v3 revision lands the round-3 intent structurally: the two-book
split, the post-filter dedup domain, and the forward-DFS + mask
mechanism are all implementable as described, and the terminal set is
honestly bounded. Two mechanism-level defects remain — both in
sentences added during the repair. M1‴: the level-0 enumeration order
is pinned to the wrong order (scheduling order, not tuple order) and
breaks pinned output even at max_hops=2. M2‴: the scheduled-only
`counterparties_total` formula contradicts the plan's own
"built-but-not-scheduled" vocabulary, its "unchanged" claim, and a
passing Phase-6 test at max_hops=1. Both are one-clause repairs, and
both are exactly the class round 3 scored as major: normative text
that contradicts a pinned behavior under a reachable input.

**CHALLENGED** — 0 blockers, 2 majors, 6 minors, 4 test gaps.
