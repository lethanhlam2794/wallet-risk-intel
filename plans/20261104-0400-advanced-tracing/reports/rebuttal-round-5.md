# Rebuttal — Critic round 5 (3 minors + 4 nits applied)

- m1: tests 33–35 assigned to phase-01's list.
- m2: test 10 now asserts `listed_address` = foreign
  `wallets.address` ORIGINAL with checksum-case fixture (original ≠
  normalized → discriminating).
- m3: test 26 disambiguated — scheduled-zero-attempts not counted;
  attempted-then-clipped counts.
- n1: §2.4 heading → "Dedup set"; n2: phase-01 `conf(sig)` → `vf×cf`;
  n3: materialized-frontier gloss expanded; n4: test 5 assertion
  routed through `frontier_by_hop` + call counts + item absence.
