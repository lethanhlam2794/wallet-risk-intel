# Phase 2 — Lazy indexing service `internal/transaction` (plan §3.2)

status: pending

## Tasks
- [ ] `EnsureIndexed` singleflight(network_id,wallet_id) → freshness gate = `status∈{fresh,partial}` ∧ indexed_at ≤ IndexingFreshTTL (10m; failed 60s cooldown — `refresh` bypasses freshness only; `indexing` never satisfies gate — singleflight join within IndexingStaleAfter, retryable past it; `not_indexed` always fetches; `disabled` re-evaluates Enabled() per request) → inclusive window `[fetch_from=max(0,w−reorg_tail+1) .. fetch_to=min(head,fetch_from+window_cap)]` — `head < fetch_from` ⇒ NO upstream call (serve persisted + head_block on wire) → `{kind}:{h}[.{k}]` max-per-stream counting → batch upsert (DO UPDATE mutables; tail-absent rows left as-is) → persist history_truncated+rows_skipped_*+status+error in one upsert → watermark advances to min-streams-contiguous (empty stream = complete-to-fetch_to) after commit
- [ ] failed: status=failed+ErrKind persisted (indexed_at=terminal write = cooldown clock); partial rows served; provider_status=ErrKind
- [ ] disabled: status=disabled+error='disabled'; zero calls; no unearned cooldown
- [ ] `ListTransactions`: UNION from/to scans, keyset DESC, self-row once w/ direction='self'; envelope {items,index_status,stale(derived),provider_status(persisted error ErrKind | "ok" only after succeeded attempt),indexed_until_block,indexed_at,head_block(null on no-fetch),history_truncated,index_scope,rows_indexed,rows_skipped,next_cursor}; refresh=1; direction in|out|self (self ⇒ self-rows only) / asset(native|<contract>) / timestamps / limit≤100 default 50
- [ ] `history_truncated` = fetch stopped on bound; persisted ⇒ honest on fresh/cooldown serves

## Success criteria
- no-fetch responses carry persisted facts; provider_status never false-ok; BSC warm windows ≤100k span; head-lag = no call; watermark = contiguous min
