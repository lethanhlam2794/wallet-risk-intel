# Rebuttal — Critic round 4 (both majors + minors + gaps applied)

**M1‴** — one-word repair applied: DFS iterates frontier_2 ∩ mask in
ascending ADDRESS order (tuple order = via₂ address-lexicographic);
frontier order is the scheduling key only. Test 23 fixture hardened:
≥2 via₂ roots with frontier-order ≠ address-order.

**M2‴** — decoupled "scheduled" (attempts/max_nodes) from "counted":
`counterparties_total` = Σ |frontier_d| over MATERIALIZED books
d ∈ {2..max_hops} — frontier_2 materializes at max_hops=1 →
total = |frontier_2|, examined = 0, remaining = total, exactly
Phase-6's unconditional `len(positions)` and the
`max_hops_1_no_expansion` test. Test 35 pins the values.

Minors: §2.2 dedup-vs-sentinel candidacy clarified (dedup far
endpoints excluded entirely; sentinels keep item/path candidacy);
direction filter applies BEFORE `cpAgg_2`; by-hop keys = materialized
frontier indices ({2:n} at max_hops=1); §6 "plan v2" heading,
phase-01 "hundredths"/"visited"/`depth` leftovers swept.

Test gaps → 33 (dedup'd sentinel min-depth), 34 (hop-3 minPos
domain), 35 (max_hops=1 coverage values); test 23 fixture pinned.
