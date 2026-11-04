# Devin Critic — Phase 7 plan challenge (round 4)

Round 3: CHALLENGED, 3 majors + 9 minors + 4 test gaps
(`reports/critique-round-3.md`). All applied — `rebuttal-round-3.md`.
Verify against `plan.md` v3 (updated §2.1/2.2/2.4/2.7/2.9/§3/§4/§5/§6)
+ phase files, grounded in `internal/tracing/service.go` + related
code. Convergence is near — the prior round found 0 blockers and the
majors were all mechanism-level.

Re-hunt:
1. `cpAgg_d`/`frontier_d` two-book split: does any pinned behavior now
   break (e.g., sentinel in dedup emits once at min depth — is that
   asserted? cpAgg edge recording when far endpoint is root/self)?
2. Post-direction-filter dedup domain: at level 1 the clause reads
   "post-filter counterparty set of emitted-prefix node's scanned
   edges" — root IS the level-1 emitter; is the wording implementable?
   Does `direction` interplay with deeper-level unfiltered sets now
   hold under every reading?
3. Forward-DFS enumeration + reachability mask: implementable as
   stated over `parents` + `children`? Bound honest? Any case where
   the emitted 3 tuples ≠ the true top-3 (test 23 discriminates)?
4. Terminal-set semantics: dedup'd, unscheduled, uncapped-by-MaxNodes
   — can it be unbounded (item candidates at max_hops=5 with
   edges_examined 5000)? response_cap/limit binding declared?
5. Coverage consistency re-verify under the two-book split:
   `counterparties_total` scheduled-only vs `nodes_examined` — is
   `examined ⊆ scheduled positions` guaranteed?
6. Tests 29–32 discriminating? Any pinned behavior STILL untested?
7. Residual stale "v2" text, `visited` leftovers, phase-file drift —
   final polish pass.

Write `reports/critique-round-4.md`; end CHALLENGED or
PLAN_CONSENSUS_REACHED. Read-only; write ONLY in `reports/`.
