# Rebuttal — Round 2 → plan amendments applied

All 22 findings accepted and applied; plan.md restored to fully
self-contained form (N-M1 fix — §1/§2/tests 1–20 all inline).

- N-M1: restored §1 scope, §2 base invariants, tests 1–12 verbatim
  inline (plan self-contained again).
- N-M2: per-adapter calls_per_index + sustained rates published;
  BSC declared deadline-bound by design; bsc max_provider_requests=8
  config default; monthly quota noted.
- N-M3: 100k window cap for nodereal; cold-start ~1300-call convergence
  arithmetic + lazy incremental posture declared.
- N-M4: `is_error` column declared as §6 deviation (no schema
  contradiction); upsert = ON CONFLICT **DO UPDATE** on mutable set
  {confirmation_status,block_number,block_timestamp,is_error} — tail
  revalidation now actually works.
- N-M5: INTERNAL excluded in MVP for BOTH adapters (declared §6 +
  `index_scope` coverage) — no `i:*` needed; future INTERNAL documented
  as `i:{ordinal}`; union-of-streams ordinal rule stated.
- N-M6: deterministic expansion order (earliest_ts,tx_hash,address);
  truncation emits prefix; coverage nodes_examined+nodes_remaining+
  nodes_failed.
- N-m7: closed category enum incl. cross_chain_listing (emits item);
  verification_status propagates.
- N-m8: `PageToken string` opaque; NextPageToken contract.
- N-m9: envelope coverage + provider_status + nodes_failed slots.
- N-m10: category post-assembly; max_hops default 2; refresh applies
  to /exposures.
- N-m11: enabled_chains config (bsc excluded by default, overridable).
- N-m12: all consumers enumerated incl. 5 JSON fixtures + enum kept
  open-ended {provider_reported_only,bounded_bfs}.
- N-m13: zero-value external excluded; NFT out of scope; missing
  contract/from skipped+counted; index_scope declared.
- N-m14: index_status closed enum; provider = source_id FK.
- N-m15: root-returning + self-loop paths excluded at any hop.
- N-n16: batch commits before watermark (crash-safe) stated.
- N-n17: lowercase pending|confirmed; nodereal computes confirmations.
- N-n18/19: one data_source per adapter; provider-switch resets
  watermark.
- N-n20: sanctions snapshot version in envelope coverage.
- N-n21: tolerant timestamp parse; reuse provider_* metrics family.
- N-n22: `asset=native` filter value; JSON cut at item boundary.
