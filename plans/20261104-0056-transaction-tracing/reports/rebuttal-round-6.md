# Rebuttal — Round 6 → plan amendments applied

All findings accepted.

- R6-m1: `stale` added to §3.5 /transactions envelope (derived:
  indexed_at vs IndexingFreshTTL; NULL ⇒ true).
- R6-m2: max_hops=1 ⇒ nodes_examined/positions_emitted/nodes_failed=0,
  nodes_remaining=counterparties_total — sum preserved.
- R6-m3: one item per counterparty — dedupe across depths, hops=min
  depth, min-hop paths only; dedup precedes category selection.
- R6-m4: indexed_at = last terminal write (one upsert w/ status+error)
  = cooldown AND stale clock; gate = {fresh,partial}∧indexed_at≤TTL;
  indexing never satisfies (singleflight join within StaleAfter);
  not_indexed → fetch path; provider_status total on
  {fresh,partial,failed,disabled}.
- R6-m5: `head_block` (nullable; populated when head fetched) on both
  envelopes — head-behind self-describing.
- R6-m6: phase files synced (matched_signals per-kind schema + LEGAL +
  kind rule; labels_snapshot_version + root.stale; direction="self"
  emission; head<fetch_from clause; join set named; headers dropped
  round metadata).
- R6-n7: item ordering = hops asc then min contributing position
  index; node_indexed_until = attempted positions only; self-row
  always emits "self"; max_edges order named; matched_signals field
  shapes pinned (entity={id,name}, programs[]={code,name},
  source=ds.code); direction filters hop-1 edges before derivation;
  one picked edge per hop position; truncated=null untruncated;
  partial+flag written together / cleared on unbounded success;
  block_timestamp always present (mined rows only); internal/
  transaction + internal/tracing package names; tests 21b extended;
  limit default 50.
