# Rebuttal — Round 3 → plan amendments applied

All 21 findings accepted; plan.md amended in place (still self-contained).

- R3-M1: tail refetch restored — fetch window is now
  `(max(0,watermark−reorg_tail+1)..cap]` with per-chain reorg_tail
  (ETH 64/BSC 100); tail-absent rows left as-is, residual declared.
- R3-M2: `transfer_index` → **content-derived** `{kind}:{sha256(immutable)[0:16]}`
  + `.{k}` occurrence suffix; context-free identity, true cross-wallet
  dedup, immutable-key invariant literal. Test 13 extended with the
  cross-wallet-same-tx case.
- R3-M3: prefix emission mechanism — workers pull positions in declared
  order, assembler emits maximal contiguous resolved prefix (FAILED =
  resolved, nodes_failed); beyond-prefix counted in nodes_examined;
  coverage gains nodes_emitted; determinism test 22 added.
- R3-M4: `is_error=false` on all edge/counterparty queries; test 21.
- R3-M5: coverage.root{index_status,provider_status,indexed_until_block,
  indexed_at,history_truncated,index_scope,rows_indexed,rows_skipped}.
- R3-m6: rows_skipped{missing_contract,invalid_address} + index_scope
  on both envelopes.
- R3-m7: category = highest-precedence (SANCTIONED>label>cross_chain);
  matched_signals[] all detail; filter matches all signals.
- R3-m8: PageToken encodes {fromKey,toKey}; max_pages counts stream-
  pair steps; head-fetch counts toward provider pool.
- R3-m9: §11.5 fields pinned per path entry; data_coverage/truncated
  envelope-only (declared §6).
- R3-m10: `limit` added to truncated enum; deterministic item order.
- R3-m11: refresh bypasses freshness only; stale INDEXING > deadline ⇒
  retry; confirmations>0→confirmed restored; tail-absent residual.
- R3-m12: LatestSnapshotPublished once per request.
- R3-m13: bulk label-match query added to phase-01/§3.4.
- R3-m14: resilience.NewClient wrap + per-adapter limiters stated.
- R3-n15: DO UPDATE no-op convention (updated_at=wallets.updated_at)
  for full RETURNING map.
- R3-n16: `eth_blockNumber` proxy name corrected.
- R3-n17: request-global pool stated.
- R3-n18: DESC sort, direction in|out, asset=native|<contract>,
  timestamp bounds inclusive.
- R3-n19: cross_chain_listing covers unresolved-network; documented.
- R3-n20: lowercase status enum; `source_id` column.
- R3-n21: edge-pick final tiebreak (tx_hash,transfer_index); 3 paths =
  lexicographic-first node sequences; per-node edge order declared.
