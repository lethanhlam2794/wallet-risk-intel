# Rebuttal — Round 7 → plan amendments applied

All findings accepted (final polish).

- R7-m1: item order key made total — hops asc, min contributing
  position index, counterparty normalized_address asc.
- R7-m2: per-endpoint direction filter enums pinned — /transactions
  {in,out,self} (self ⇒ self-rows only); /exposures {in,out}; phase
  files aligned.
- R7-n3: coverage.head_block = root's observed head this request;
  per-node heads internal to node_indexed_until.
- R7-n4: stale "all nodes_*=0" parenthetical replaced with the
  explicit enumeration (remaining=total).
- R7-n5: sentinels DO emit items when matched (counterparties, not
  positions; excluded from counterparties_total regardless).
- R7-n6: critic header updated to round count.
