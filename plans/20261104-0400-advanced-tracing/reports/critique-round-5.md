# Devin Critic — Round 5: Advanced Tracing plan challenge (v3, final hunt)

Scope: `plans/20261104-0400-advanced-tracing/` (plan.md v3 + 2 phase
files + `reports/rebuttal-round-4.md`), verified against
`internal/tracing/{service,model,service_test}.go`,
`internal/transaction/service.go`, `queries/transactions.sql`,
`internal/db/transactions.sql.go`,
`internal/scoring/{policy,engine}.go`,
`internal/httpserver/transactions.go`,
`internal/screening/{handler,mapper}.go`,
`internal/sanctions/{service,model}.go`,
`internal/network/resolver.go`, `internal/config/config.go`,
`cmd/api/main.go`, `api/openapi.yaml`,
`migrations/{000001_init,000005_labels}.up.sql`,
`tests/transactions_api_test.go`, and
`docs/guidebook-transaction-tracing.md`.

## Round-4 findings — resolution status

- **M1‴ RESOLVED.** plan.md:182-185 now reads "sequences emit via
  lexicographic DFS from frontier_2 positions in **ascending ADDRESS
  order** restricted to the reachability mask — matching
  via₂-most-significant tuple order (**frontier order is the
  scheduling key, NOT the tuple key**)". Level-0 iteration now matches
  the pinned output. Verified against the concrete break: root edges
  root→b (ts=1), root→a (ts=2), a<b → DFS starts at a (address order)
  → emits [a-path, b-path] = Phase-6's `vias` sort (service.go:397) —
  bit-identical. Test 23 (plan.md:335-337) now carries the
  discriminating fixture: ≥4 sequences, ≥2 via₂ roots, frontier order
  ≠ address order.
- **M2‴ RESOLVED.** plan.md:128-134 counts MATERIALIZED position-books
  with the explicit carve-out "frontier_2 is materialized at
  max_hops=1 (built for ordering/dedup even though never scheduled),
  so total = |frontier_2|" — Phase-6's unconditional `len(positions)`
  (service.go:160-166). Scheduled gates only attempts + `max_nodes`
  (plan.md:132,152-154). The by-hop domain is pinned to materialized
  frontier indices with "`{2:n}` with examined 0" at max_hops=1
  (plan.md:139-142) — m4‴/m6‴ both covered. Test 35 (plan.md:363-365)
  pins the values. Verified all three regimes: max_hops=1 →
  materialized {2}, scheduled {}, total=|f2|, remaining=total;
  max_hops≥2 → materialized = scheduled = {2..max_hops}; terminal
  cpAgg excluded from all counts.
- Minors m1‴–m6‴: landed except one survivor — §2.4's heading still
  reads "Visited set" (n1 below). Verified: §2.2 now splits sentinel
  vs dedup candidacy (plan.md:51-53); "the filter applies BEFORE
  `cpAgg_2` construction, exactly as Phase-6 filters before `cps`"
  (plan.md:76-77); §6 heading now "(pinned — plan v3)" (plan.md:285);
  phase-01:16 reads "integer per-term rounding (sixteenths decay,
  half-up)", phase-01:9 "unified dedup set", phase-01:19
  `MaxPagesFor(frontierIdx int)`.
- Test gaps 33/34/35: added and discriminating — but orphaned (m1
  below).

## Fresh hunt — the five prompt questions

1. **Remaining contradictions between sections.** None at the
   mechanism level; all three named axes verified consistent:
   - *dedup vs cpAgg*: §2.2's split — sentinels stay item/path
     candidates in cpAgg_d; dedup members "excluded from candidacy
     entirely" — coheres with §2.4 "emits no item and no position".
     Reproduces Phase-6: root-returning edges die in dedup (root ∈
     dedup ≡ service.go:306-308), hop-1-cp edges die in dedup
     (post-filter set ≡ `cps`, service.go:309-311), self-edges
     excluded in both (service.go:127-129,303-305). A dedup member can
     never be a via (excluded from positions) nor a parent of a later
     candidate (never scanned at that level), so exclusion loses no
     evidence.
   - *scheduled vs materialized*: consistent — materialized =
     {frontier_2 always} ∪ {3..max_hops}; scheduled = {2..max_hops}
     minus the max_hops=1 frontier_2. Attempts/max_nodes only inside
     scheduled; counts over materialized. `examined ⊆ scheduled ⊆
     materialized` ⇒ `remaining ≥ 0` is guaranteed, floor defensive.
   - *tuple order vs frontier order*: consistent — frontier order
     (firstEdge.ts, tx_hash, addr; §2.3) is used only for scheduling,
     position indices (minPos), and the contiguous prefix; tuple order
     drives emitted `paths[]` via address-ordered DFS. No cross-talk.
   The surviving contradictions are confined to test-spec wording
   (m2, m3 below).
2. **Every pinned mechanism implementable without guessing?** Yes.
   Re-derived end-to-end: per-depth loop (frontier_d → scheduled
   expansion → emitted prefix → cpAgg_{d+1} → items); parents DAG +
   per-item reverse-BFS mask + address-ordered forward DFS emits the
   true first-MaxPathsPair tuples (inside the mask every node has a
   completing suffix → the declared O(Σ|parents| +
   MaxPathsPair×d×P) bound is honest); per-step `pickEdge` gives
   `Edges[len=d]`; adjacent-pair temporal rule is transitive; e₁ for
   via₂ = Phase-6's `pickEdge(agg.edges)`; dedup_d formulas reproduce
   Phase-6's exclusion domain at every level (dedup_3 = {root}∪`cps`
   exactly); `max_edges` phrasing ("edgesClipped or edgesLeft ≤0 with
   unexamined") is provably equivalent to Phase-6's compound guard
   (clipped ⇒ edgesLeft=0 — CAS only decrements); scoring terms
   re-verified: (85·10·10·16+800)/1600=85, (70·10·10·4+800)/1600=18,
   (20·10·10·1+800)/1600=1 — min non-zero term ≥1 ⇒ Σ=0 ⟺ all
   zero-weight ⟺ null; `0` never on wire.
3. **Tests 1–35 discriminating, phase-owned, fixture-defined?**
   1–32 verified in round 4 stand (each discriminates; ownership
   complete for that range). New defects: tests 33–35 are owned by no
   phase (m1); test 10's `listed_address` assertion contradicts §3
   under the very fixture rebuttal-3 seeded (m2); test 26's second
   clause is ambiguous on exactly the axis §2.7 resolved (m3).
4. **Residual `visited`/"v2"/"hundredths" drift gone?** Almost —
   grep-clean except plan.md:68's heading "### 2.4 Visited set" (body
   uses `dedup`) and phase-01:16's `conf(sig)` (v2 single-factor
   vocabulary; §4 pins vf×cf).
5. **Fresh defects from the latest edits?** The materialized-books
   reword and the address-order reword introduced no mechanism
   regressions (verified max_hops ∈ {1,2,3} end-to-end, and the
   direction+dedup interplay at level 1 is still Phase-6's `cps`
   verbatim — filter inside the build loop, service.go:129-141). One
   bookkeeping regression: adding tests 33–35 to §6 without extending
   the phase-01 range (m1).

## Part 1 — Blockers / Majors

None. Both round-4 majors landed cleanly and no fresh mechanism-level
contradiction exists. The plan's pinned mechanisms are now each
implementable to a unique consistent reading.

## Part 2 — Minors

- **m1. Tests 33–35 own no phase.** phase-01:27 lists "1–5, 8, 9,
  12–19, 21–24, 26–31"; phase-02:34 lists "6, 7, 10, 11, 20, 25, 32".
  The union covers 1–32; tests 33 (dedup'd sentinel min-depth), 34
  (hop-3 minPos domain), 35 (max_hops=1 coverage values) — all
  `internal/tracing/service_test.go` tests — are assigned nowhere.
  Same class as round-3 m4″ (repaired then for 22–28); the new tests
  were added to §6 without extending the owning range. One-clause
  repair: phase-01's list gains "33–35".
- **m2. Test 10 asserts `listed_address`=normalized addr — contradicts
  §3's original-representation pin under the seeded fixture.**
  plan.md:307 vs plan.md:212-213 ("`listed_address`:<foreign
  `wallets.address` original representation>"). Rebuttal-3 claims
  m8″'s repair: "test 10 seeds checksum-case original" — under that
  fixture wallets.address ≠ normalized_address, so the pinned value
  (original) differs from the asserted value (normalized). As written
  the test either fails against the §3-conformant implementation, or
  the fixture was never actually differentiated and m8″ is
  unresolved. One-clause repair: "`listed_address` = foreign
  `wallets.address` original (checksum-seeded fixture)".
- **m3. Test 26's "empty-prefix → not counted" is ambiguous on the
  axis §2.7 resolved.** plan.md:343-344. §2.7 (plan.md:145-148) now
  splits "scheduled-but-zero-attempts does NOT count" from
  "attempted-then-clipped DOES count". Read as "attempted frontier
  with empty emitted prefix" (deadline mid-first-attempt — reachable;
  examined stays ≥1, prefix 0), the clause contradicts the pinned
  rule. Read as "unattempted/empty frontier" it is consistent but
  redundant with clause 1. One-clause repair: "frontier never
  attempted → not counted" (drop "empty-prefix" or qualify it as
  never-attempted).

## Part 3 — Nits (cosmetic, non-blocking)

- **n1.** plan.md:68 heading "### 2.4 Visited set" — the one
  `visited` survivor; the body and all dependents use `dedup`.
- **n2.** phase-01:16 `conf(sig)` — stale v2 single-factor name; §4
  pins the two-factor `vf`/`cf` structure.
- **n3.** §2.7's gloss "built for ordering/dedup" understates why
  frontier_2 exists at max_hops=1 (Phase-6 derives `positions`
  unconditionally); the operative sentences are correct — gloss only.
- **n4.** Test 5's "assert frontier_3 contents" — frontier membership
  is not a wire surface; the assertion resolves through
  `frontier_by_hop` values + provider-call counts + item absence.
  Loose wording, implementable.

## Part 4 — Positive confirmations

- The two-book split is now airtight at every level: cpAgg keeps all
  scanned counterparties (minus self-edges/dedup) for item+path
  evidence; frontier keeps the expansion subset. Phase-6 reproduces
  exactly at max_hops≤2 — `cps`/`positions`/`hop2` map have direct
  counterparts and identical membership rules, verified line-by-line
  against service.go:116-166,298-324.
- The terminal-set semantics are coherent and bounded: dedup'd
  uniformly (dedup_{h+1} covers scans ≤ h−1 → every shallower
  candidate), unscheduled, uncounted, item-bounded by
  response_cap/limit; candidates bounded by edges_examined ≤ 5000;
  per-candidate lookups under the same ctx (deadline → 500, declared).
- The direction interplay survives formal verification: post-filter
  level-1 set ≡ Phase-6 `cps` verbatim; filtered-out cp X is absent
  from cpAgg_2 AND dedup → legitimately re-emittable at hop ≥2 via
  unfiltered deep scans — matching Phase-6's hop2 eligibility exactly
  (test 29 discriminates).
- Sentinel lifecycle verified at all depths: hop-1 sentinel ∈
  post-filter cp set → dedup-covered forever (test 33); deep sentinel
  = cpAgg candidate, never position, dedup'd at level+2; sentinel-cp
  items keep real path evidence via parents maps (test 30); never a
  provider call (test 7).
- Coverage invariants hold constructively at all max_hops:
  `examined + remaining = total` (attempts only inside scheduled
  frontiers; dedup ⇒ single membership); `positions_emitted` = Σ
  prefixes (0 at max_hops=1 = Phase-6); `max_depth_reached` =
  deepest attempted frontier (zero-attempt excluded, ceiling
  max_hops); `node_indexed_until` spans attempted positions, dedup ⇒
  unique keys.
- Cross-chain wiring is real and grounded: `wallets.address` vs
  `normalized_address` both exist (000001_init.up.sql:18-19);
  `wl.confidence` exists with its CHECK constraint
  (000005_labels.up.sql:48,57); `ListActiveNetworks`-built resolver
  gives the ∖{request-net} ID set; `ANY('{}')`/NULL both yield zero
  rows (test 32); `signals()`/`consider()` skip is phase-02-listed;
  `kind` stays inside the closed enum (openapi.yaml:1621-1623).
- Scoring isolation verified: `POST /screenings` `options.max_hops`
  is a provider-reported-hops bound (handler.go:95, mapper.go:353)
  with no structural link to tracing depth — freezing it at 0–2 is
  safe, not arbitrary; `exposure_score` never feeds `risk_score`
  (new field + new policy version).
- The OpenAPI touch-list in phase-02 matches reality exactly:
  summary "Bounded 1-hop/2-hop" (:508), `enum: [1, 2]` + "0 or `>2`
  → 400" (:525-526), item `hops` `enum: [1, 2]` (:1681) — all
  present as described; `network`/`listed_address` already exist on
  ExposureSignal (:1640-1641).
- Test 21's premise is accurate — exactly two tests assert 3→400:
  `TestValidateRequest` (service_test.go:285-299) and the API suite
  (transactions_api_test.go:184-188).
- Cost bound is honest: per-depth MaxNodes_d × MaxPages_d under the
  request-global provider pool, edge CAS, and deadline; assembly
  O(Σ|parents| + MaxPathsPair×d×P) per item, items ≤ emitted cps ≤
  5000. Every bound exits via a named truncation reason.

## Assessment

The v3 revision is now mechanism-complete: both round-4 majors are
repaired as specified, and a full adversarial pass over §2.1–§5 finds
no remaining contradiction between dedup/cpAgg, scheduled/materialized,
or tuple/frontier orderings — each is pinned to a unique consistent
reading that I verified reproduces Phase-6 bit-identically at
max_hops≤2 (including the `direction` interplay) and generalizes
soundly to depth 5. What remains is confined to test-spec wording and
bookkeeping: three pinned tests lost their phase owner when appended
to §6 (m1 — the same defect class as round-3 m4″), one test assertion
contradicts the wire field it verifies under its own hardened fixture
(m2), one test clause kept an ambiguity the plan resolved elsewhere
(m3), and two cosmetic drifts survived the sweep (n1, n2). Every item
is a one-clause repair; none requires a design decision.

**CHALLENGED** — 0 blockers, 0 majors, 3 minors, 4 nits.
