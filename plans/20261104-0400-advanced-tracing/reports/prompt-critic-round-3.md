# Devin Critic — Phase 7 plan challenge (round 3)

Round 2 was CHALLENGED (1 blocker + 7 majors + 12 minors + 7 test
gaps — `reports/critique-round-2.md`). Author applied all fixes —
`reports/rebuttal-round-2.md`. Verify against `plan.md` v3 + phase
files, grounded in real code (`internal/tracing/service.go`,
`internal/transaction/service.go`, `queries/transactions.sql`,
`internal/scoring/policy.go`, `api/openapi.yaml`).

Then re-hunt fresh:

1. Visited snapshot formula (§2.4): verify "≤ d−2 emitted-prefix
   scans" is now correct AND doesn't reopen a min-depth hole — can a
   node emit at a LATER depth than its true min? Can a cycle edge
   slip through before its far endpoint lands in visited?
2. minPos two-domain rule: is "min first-via index in frontier_2"
   evaluable at depth ≥3 (does the position struct retain the chain
   back to frontier_2)? Any cross-domain comparison leakage?
3. Lazy best-first enumeration: is "sorted parent lists + best-first
   merge" actually implementable over the `parents` map without full
   materialization? Is the declared O bound honest?
4. Scoring over evidence rows: at item-build time are the label rows
   still in scope (signals() discards them — what threads them)? Is
   per-term rounding + literal Σ + min(100) now fully evaluable?
   Decay on foreign label with `network` ≠ request net — anything
   broken?
5. `ANY($2)` resolver IDs: empty foreign-ID set semantics (pgx array
   NULL/empty edge case)?
6. Coverage consistency: `counterparties_total` = Σ frontiers vs
   `nodes_remaining_by_hop` + global remaining — does
   `examined+remaining=total` still hold when a node is attempted at
   d but ALSO counted in an earlier frontier it never entered (it
   can't — verify)?
7. New test list 22–28: each actually discriminating? Any pinned
   behavior still untested?

Write `reports/critique-round-3.md`; end CHALLENGED or
PLAN_CONSENSUS_REACHED. Read-only; write ONLY in `reports/`.
