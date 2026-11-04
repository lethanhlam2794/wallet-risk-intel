# Rebuttal — Round 5 → plan amendments applied

All findings accepted.

- R5-M1a: §3.2 corrected — DISABLED ⇒ status=disabled + error='disabled'
  (ErrKind exists), zero calls, no FAILED cooldown; typed failure ⇒
  status=failed; error cleared-on-success.
- R5-M1b: provider_status = persisted error ErrKind when non-empty
  (disabled included); "ok" ONLY after a succeeded attempt — no more
  false-ok on the default BSC path.
- R5-M1c: status=disabled re-evaluates Enabled() per request (zero
  cost); enabled ⇒ normal indexing path.
- R5-M1d: `stale` = derived boolean beside index_status (indexed_at vs
  IndexingFreshTTL) — composable over any status.
- R5-m2: matched_signals schema pinned {kind:sanctions_direct|label|
  listing, category, label?, entity?, programs[]?, source,
  verification_status?, network?, listed_address?} with per-kind
  applicability; LEGAL first in intra-tier order; SANCTIONED items
  distinguished by kind (scoring.Kind convention); bulk label query
  pins ListAllWalletLabels join set.
- R5-m3: head < fetch_from ⇒ no upstream call; serve persisted facts;
  head-behind-watermark coverage note.
- R5-m4: unfiltered self-row emitted ONCE with direction="self"
  (third wire value, §6); in|out filters both match; kills the UNION
  twin + keyset page-boundary hole.
- R5-m5: IndexingFreshTTL=10m + IndexingStaleAfter (vs EnsureIndexed
  duration; NOT EnrichmentDeadline) — named config keys.
- R5-n6: window_covered dead vocab dropped; partial⟺history_truncated
  pinned (same fact, one flag); rows_skipped cumulative;
  counterparties_total = positions after sentinel exclusion
  (max_hops=1 ⇒ nodes_*=0); snapshot bounded-drift declared +
  labels_snapshot_version added; source_type='api' convention;
  duration metric via call-site or OnDuration; openapi.yaml:14 stale
  description fix; max-count premise restated (no stream lists a real
  copy twice, ≥1 stream covered); empty stream = complete-to-fetch_to;
  error cleared-on-success; /transactions limit ≤100 + cursor-bounded
  cap declared; lowercase provider_status divergence documented;
  path ordering over normalized addresses; edge asset="native";
  transfer_index added to edge schema (§6); tests 21b (self-row,
  sentinel, max_hops edges, exposure filters).
