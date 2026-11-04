# Rebuttal — Round 8 → plan amendments applied

- R8-m1: sentinel item ordering pinned — its position index = own rank
  in the hop-1 expansion ordering (it has a deterministic rank even
  though never expanded); total key restored.
- R8-n2: phase-01 relabel — indexed_until_block=watermark,
  indexed_at=last terminal write.
