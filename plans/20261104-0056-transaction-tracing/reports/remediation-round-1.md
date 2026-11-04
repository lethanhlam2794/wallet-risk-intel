# Remediation — review-round-1 (Phase 6)

Verdict in: **CHANGES_REQUESTED** (1 blocker, 6 majors, 10 minors, 6 nits).
All findings accepted; each resolved below with file evidence and a
regression test where the behavior is testable.

## BLOCKER

### B1 — per-stream watermark (silent index loss + coverage over-claim)

`internal/transaction/service.go`: `maxBlock` is now tracked **per stream**
(`streamMaxBlock` map populated from `t.Stream`); an unfinished stream's
coverage = its own max−1, or `fetch_from−1` when it emitted no rows.
A fast `txlist` can no longer over-claim a lagging `tokentx`.

Companion fix found during remediation: `status=fresh` could be written
when a stream dropped its cursor unfinished (record-cap case) — the next
request inside FreshTTL would then serve `fresh` and never refetch.
`fresh` is now earned only when **every** declared stream finished
(`allFinished` gate added to the terminal switch).

Regression: `TestEnsureIndexed_PerStreamWatermark` — txlist finishes
@95000, tokentx unfinished @400 ⇒ `indexed_until_block=399`,
`status=partial`, `history_truncated=true`.

## MAJOR

### M1 — max_edges clip invisible + racy deduction

`internal/tracing/service.go::scanEdges`: rewritten. Deduction is a CAS
loop (`CompareAndSwap`) so concurrent workers can never overshoot
`MaxEdges`; any clip — boundary row or concurrent budget shrink — sets
`edgesClipped`, and a worker finding an empty budget also marks it.
Truncation switch: `edgesLeft <= 0 && (edgesClipped || positions >
examined)` → `truncated=max_edges`, including root-scan clips.

Regression: `TestExposures_EdgeClipReported` — 5 root edges vs
`MaxEdges=3` ⇒ `truncated=max_edges`, `edges_examined ≤ 3`.

### M2 — hop-2 errors mislabeled `deadline`

`internal/tracing/service.go`: an `EnsureIndexed` error inside the
worker sets `deadlineHit` only when `gctx.Err() != nil` or the error
is `context.DeadlineExceeded`/`Canceled`; all other errors return
through `errgroup` → `Exposures` → 500.

Regression: `TestExposures_InfraErrorPropagates` — DB outage ⇒
`Exposures` returns an error, never a 200 with `truncated=deadline`.

### M3 — `IndexingStaleAfter` was dead config

`internal/transaction/service.go`: new gate — `status='indexing'` with
`updated_at` within `StaleAfter` is served (another process owns the
claim; same-process callers already join via singleflight); past the
horizon the dead claim is retried.

Regression: `TestEnsureIndexed_StaleClaimHorizon` — live claim ⇒ zero
provider calls; `updated_at - 1h` ⇒ fetch proceeds.

### M4 — hop-2 paths emitted per-edge; wrong evidence pick

`internal/tracing/service.go`: `hop2` is now keyed `(cp2, via)` — one
`hop2Seq` per node sequence holding all e2 candidates. Item assembly
sorts **sequences** lexicographically over via addrs and applies the
pinned per-hop pick `pickEdge` (earliest ts → smallest amount →
(tx_hash, transfer_index)) to **both** e1 (root→via) and e2 (via→cp2).
Duplicate same-via edges can no longer crowd out distinct sequences at
`MaxPathsPair`, and output is schedule-independent.

Regression: `TestExposures_Hop2DedupBySequence` — 2 edges via same
intermediary ⇒ exactly 1 path, e2 = the smaller-amount edge.

### M5 — Etherscan 10k record cap read as stream end

`internal/provider/chainscan/adapter.go`: `pageToken` gains a `Capped`
set; when a stream's cumulative rows reach `recordCap` (10,000) on a
full page, the cursor is dropped **but** `Finished=false` is reported —
both in the current page and (via the token) on subsequent steps — so
coverage stops at the stream's own max block.

Regression: `TestLookupTransfers_RecordCapNotFinished` — 11 steps ×
1000 rows ⇒ stream stays `Finished=false`.

### M6 — query param names deviated from spec §9.9

`internal/httpserver/transactions.go` + `api/openapi.yaml`:
`from_ts`/`to_ts` → `from_timestamp`/`to_timestamp` (canonical spec
names; the old spellings are not silently accepted).

Regression: `TestTransactionsAPI_ContractRegressions` asserts the
`from_timestamp` filter actually narrows results.

## MINOR

1. `rows_indexed` now counts **emitted durable rows** (`buildRows`
   returns the row count), not raw provider sightings.
2. `/exposures?limit=0` → 400 (`n <= 0` check added to the handler).
3. `/exposures` empty result serializes `items:[]` (`items := []Item{}`,
   and the category-filter keep-list initialized non-nil).
4. `writeDisabled` preserves persisted `history_truncated`.
5. Disabled check reordered ahead of FAILED cooldown — a disabled
   provider records `disabled` instead of lingering under a stale
   `failed` row.
6. Local budget exhaustion no longer emits `provider_status:
   "rate_limited"` — the pool is ours; persisted state stands.
7. Expansion-order tiebreak is now `(earliest edge ts, tx_hash,
   normalized_address)` via `firstEdge` + sorted `cpOrder` (was
   transfer_index through scan first-appearance).
8. TX-index FAILED cooldown reuses `EnrichmentNegativeTTL` — **accepted
   as designed**: plan §3.2 pins the reuse (`FAILED ⇒ cooldown per
   EnrichmentNegativeTTL`); splitting the knob is a config-surface
   change deferred to Phase 7.
9. Bad `asset` now returns `BAD_REQUEST` (new `ErrInvalidCursor`
   distinguishes cursor errors → `INVALID_CURSOR`; filter errors →
   `INVALID_INPUT`-class 400).
10. `MarkWalletIndexing` clears `error` — a live claim no longer echoes
    a stale ErrKind.

Regression coverage: `TestMarkWalletIndexing_ClearsError` (10),
`TestTransactionsAPI_ContractRegressions` (M6, 2, 3),
`TestEnsureIndexed_BudgetZero` updated for (6).

## NIT

- `IndexOutcome.ProviderCalls` now counts real upstream calls
  (head + per-page UpstreamCalls), not steps.
- `UNION` comment corrected (OR-predicate bitmap scan).
- Dead `var _ = wallet.NormalizeEVM` removed (+ import).
- `noEdges` double-test collapsed.
- Cook-report prose `indexed_fresh|indexed_partial` → `fresh|partial`.
- `seedLabelFor` resolves `ethereum`'s real network_id.

## Gate re-run (post-remediation)

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `sqlc generate` | clean (MarkWalletIndexing regenerated) |
| `go test ./...` | all packages pass |
| `go test -race ./internal/tracing ./internal/transaction` | clean |
| OpenAPI parse | valid |
