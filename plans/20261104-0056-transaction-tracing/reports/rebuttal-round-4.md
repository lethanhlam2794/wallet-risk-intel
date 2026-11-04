# Rebuttal — Round 4 → plan amendments applied

All findings accepted.

- R4-M1: window formula corrected — `fetch_from=max(0,w−reorg_tail+1)`,
  `fetch_to=min(head,fetch_from+window_cap)`; cap bounds the span;
  BSC advance ≈99,901/window declared; inclusive `[a..b]` notation.
- R4-M2: `wallet_index_state` gains `history_truncated BOOL` +
  `rows_skipped_{missing_contract,invalid_address} BIGINT`; `stale`
  derived at read (not stored); `rows_indexed`=cumulative;
  `provider_status` no-fetch = persisted outcome ("ok"|ErrKind).
- R4-M3: `.{k}` counting domain pinned — occurrences = max over
  per-stream counts in the assembled scan (W→W phantom dupes fixed;
  page-boundary reconciled by tail refetch; both-streams-truncated
  residual declared).
- R4-m4: test 22 restated — prefix property exercised over timing-free
  bounds (request/node truncation, pre-expired ctx, WithClock);
  deadline asserted only as truncated=deadline.
- R4-m5: `limit` last in precedence; pipeline assemble→filter→order→cut.
- R4-m6: intra-label precedence (MALICIOUS>SERVICE>ENTITY>OTHER, code
  asc); matched_signals[] schema pinned {kind,category,entity?,
  program?,label_source,verification_status?}; classification-only
  matches emit items (informational).
- R4-m7: bounds govern hop-2 positions only (hop-1 items unaffected);
  resolved = index+scan+match; every counterparty is an expansion
  position regardless of match.
- R4-m8: refresh applies to every EnsureIndexed in-request; direction
  in|out default both; max_hops 1=hop-1-only, 0|>2→400; W→W self-row
  matches direction in AND out.
- R4-m9: sentinels (0x0/0x…dead) excluded from expansion, still
  listed; ascending-index earliest-history bias declared.
- R4-m10: tests 18b/18c added (provider-switch reset;
  refresh-vs-cooldown; stale-INDEXING); self-transfer folded into 13.
- R4-n11: counterparties_total emitted; nodes_emitted→positions_emitted;
  node_indexed_until null for failed; snapshot_version null-able;
  stream cursors {streamA,streamB}; max_edges=request-global running
  count; provider_status="ok" on success.
- R4-n12: amount_raw pinned (rawContract.value/value hex); tokentx
  isError doc; value>0 scope exclusion ≠ rows_skipped; tx_hash
  lowercase + canonical hash inputs; source_type='provider',
  address_type='evm'; /transactions = UNION of from/to scans;
  NEW provider_request_duration_seconds metric (no reuse claim).
