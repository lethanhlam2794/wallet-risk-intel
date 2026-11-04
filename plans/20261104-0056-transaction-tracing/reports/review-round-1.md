# Devin Review — Phase 6: Transaction Tracing (round 1)

Independent review of the uncommitted Phase-6 diff against
`plans/20261104-0056-transaction-tracing/` (plan.md + 4 phase files; the
plan is the contract) and `reports/cook-report.md`. Read-only; Phase-5
leftovers excluded per scope.

## Verification evidence (run fresh for this review)

| Command | Result |
|---|---|
| `gofmt -l .` | empty ✓ |
| `go build ./...` | pass ✓ |
| `go vet ./...` | pass ✓ |
| `go test -count=1 ./internal/transaction/ ./internal/tracing/` | pass (7.1s / 5.6s — real test DBs, not skips) |
| `go test -count=1 -run 'TestTransactionsAPI|TestExposuresAPI' ./tests/` | pass (7.8s) |

Schema, migration, wire enums, and generated sqlc were diffed line-by-line
against plan §3.1–§3.5 and spec §7.13/§9.9–9.10/§11.

---

## BLOCKER

### B1 — Watermark coverage uses a *global* max block for *per-stream* unfinished streams → silent permanent index loss + coverage over-claim

`internal/transaction/service.go:284` declares a single `maxBlock`
accumulated over **all** transfers from **all** streams and steps
(`service.go:315-319`). The per-stream coverage loop then charges every
unfinished stream `cov = maxBlock - 1` (`service.go:355-369`):

```go
for name := range streamSeen {
    if finishedStreams[name] { continue }
    var cov int64
    if maxBlock > 0 { cov = maxBlock - 1 }   // ← GLOBAL max, not the stream's own
    else            { cov = fetchFrom - 1 }
    if cov < newWatermark { newWatermark = cov }
}
```

`Transfer.Stream` is already populated (`provider.go:226-227`,
`chainscan/mapper.go:40,59`, `nodereal/mapper.go:41`), so a per-stream max
is available — it is simply not tracked.

Concrete scenario (the common case for active wallets): `txlist` finishes
3 rows ending at block 95,000; `tokentx` returns a full page spanning
blocks 100–400 → `maxBlock = 95000` → `cov_tokentx = 94999` →
`indexed_until_block = 94999`, `status=partial`. The next window is
`[94999−tail+1, …]` — every token transfer in `(400, 94935]` is **never
fetched again**. The index permanently under-indexes while claiming
coverage it does not have — a silent violation of the core invariant
"index completeness is never claimed without evidence" (plan.md:49). The
error is asymmetric (global ≥ per-stream max ⇒ coverage only ever
overstated), which makes it strictly dangerous.

Test coverage confirms the hole: `stubTx`/`apiStub` never populate
`page.Streams`, so every service test exercises only the
`len(streamSeen) == 0` stub branch (`service.go:345-353`) where a single
implicit stream makes the global max coincidentally correct.

**Required fix:** track `maxBlock` per stream name (bucket
`page.Transfers` by `t.Stream`); unfinished stream coverage = its own
max−1, or `fetchFrom−1` when it emitted no rows.

---

## MAJOR

### M1 — `max_edges` bound is unenforceable as written: clipped scans are never reported, and the running count races

Two independent defects:

1. **The truncation fact is discarded.** `scanEdges` fetches `remain+1`
   rows to detect the boundary, clips to `remain`, and throws the
   overflow evidence away (`internal/tracing/service.go:563-565`). The
   truncated-reason condition `edgesLeft.Load() <= 0 && len(positions) >
   examined` (`service.go:494`) only fires when unexamined positions
   remain. If the edge budget clips the **root** scan, or the scan of the
   last pulled position while all positions still complete, the response
   emits `truncated: null` — and items are built from silently-clipped
   edge sets (counterparties beyond the clip vanish entirely, even from
   `counterparties_total`). The plan requires every bound to name its
   reason (plan.md:217-219, test 6; phase-03:9). `queries/transactions.sql:68-69`
   comments that mid-scan truncation is deterministic — it is, but it is
   also invisible.

2. **The running count is not atomically reserved.**
   `remain := left.Load()` … `left.Add(-len(rows))` (`service.go:550-567`)
   under concurrency-4 lets two workers each observe `remain=5000`, each
   scan ~5000 rows, and drive the counter negative — the request-global
   `max_edges` (plan.md:209) is exceeded by up to (concurrency−1)×
   remainder. `Budget.tryTake` correctly CAS-reserves (`transaction/
   model.go:79-92`); `edgesLeft` needs the same pattern.

### M2 — Any `EnsureIndexed` error in hop-2 is misreported as `truncated: "deadline"` and swallowed

`internal/tracing/service.go:208-213`: on `err != nil` from
`s.tx.EnsureIndexed` the worker stores `deadlineHit` and returns —
regardless of the error's nature. `EnsureIndexed`'s error path is
DB/system errors (wallet resolve, `UpsertTransactions`,
`UpsertWalletIndexState`, `MarkWalletIndexing` — `transaction/
service.go:129-156, 184-199, 275-278, 329-338, 451-463`): a real DB outage
inside hop-2 is masked as a deadline and produces a 200 with a false
reason instead of an error or an honest failure. Sibling code does it
right — the `scanEdges` error path checks `gctx.Err()` and propagates
non-context errors (`service.go:239-246`). Classify deadline only when
`gctx.Err() != nil` / `errors.Is(err, context.DeadlineExceeded)`;
otherwise propagate or mark the node failed.

### M3 — `IndexingStaleAfter` is plumbed end-to-end and never read — the stale-claim horizon is dead config

`internal/config/config.go:65,113` → `cmd/api/main.go:243` →
`transaction.Config.StaleAfter` (`service.go:25`), defaulted
(`service.go:39-41`), then **never consulted**. `ixState.claimed`
(`service.go:146`) is declared and never set. Every `indexing` row is
retried unconditionally (`service.go:224-226`).

Plan §3.2 (plan.md:130-132; phase-02:6) pins: within `IndexingStaleAfter`
a persisted `indexing` claim is in-flight (in-process callers join via
singleflight — which does work), **past** it the row becomes retryable.
The implementation makes all claims instantly retryable, so the horizon
does nothing: a second process (or a post-restart replica seeing a
1-second-old live claim) launches a duplicate concurrent index. The
single-process case is saved by singleflight, but the knob is dead and the
cook report claims the horizon works (cook-report.md:50-51). Either honor
`updated_at < now−StaleAfter ⇒ serve persisted state`, or delete the
config and declare the residual explicitly.

### M4 — Hop-2 `paths[]` are emitted per-edge, not per-node-sequence; evidence pick and ordering deviate from the pinned rules

`internal/tracing/service.go`:

- `hop2[cp2]` appends one `hop2Path` **per e2 edge** (`:292-294`), so N
  edges via the same node produce N `paths[]` entries of the **same** node
  sequence — the plan pins "3 lexicographic-first node sequences over
  normalized addrs" (phase-03:8). Duplicate sequences crowd out distinct
  via-sequences at the `paths[:MaxPathsPair]` cut (`:380-382`).
- `sort.Slice(paths, via<)` is unstable: for two same-`via` entries the
  retained set after the cut varies run-to-run (`:377-382`) —
  nondeterministic evidence, contrary to test-22's intent.
- `e1` is `agg.edges[0]` — the direction-filtered list's first element in
  `(ts, tx_hash, transfer_index)` scan order (`:317-325`) — not the pinned
  per-hop pick `earliest ts → smallest amount → (tx_hash,transfer_index)`
  (phase-03:8; `pickEdge`/`edgeLess` exist at `:585-607` and are unused for
  e1). Evidence edge differs from the contract when same-ts edges have
  different amounts.

Group `hop2` by (cp2, via) with a single e2 pick per sequence, sort
sequences lexicographically over via addrs, and apply `pickEdge` to both
edges.

### M5 — Etherscan's 10k-record cap silently truncates heavy wallets and reports the stream finished

`internal/provider/chainscan/adapter.go`: `more = len(rows) >= size`
(`:339`) treats a short/empty page as stream end, and `status=0` with a
`[]` result is a finished stream (`:322-327`). Upstream caps
`page*offset` at 10,000 records — the page after the cap returns an empty
result that is indistinguishable from true end-of-stream. A wallet with
>10k transfers of one kind in the window gets its stream marked
`Finished`, which contributes `fetch_to` to the watermark
(`transaction/service.go:356-360`) — full-window coverage claimed while
only the first 10k records were seen. Ethereum has **no** window cap
(`cmd/api/main.go:252` maps only `bsc`), so the unbounded window makes
>10k records realistic for exactly the high-activity wallets this API
screens. Track cumulative rows per stream (or detect the cap-adjacent
empty page) and mark the stream unfinished so the watermark stops at the
stream's own boundary — never declare coverage past it.

### M6 — `/transactions` filter param names deviate from plan + spec

Handler and OpenAPI implement `from_ts`/`to_ts`
(`internal/httpserver/transactions.go:91-108`; `api/openapi.yaml:473-478`),
while plan §3.5 pins `from_timestamp`/`to_timestamp` (plan.md:409) and
spec §9.9 says the same (`docs/tailieu.md:1292-1293`). A consumer built
against the contract sends `from_timestamp`; Gin silently drops the
unknown param and returns unfiltered data — a silent contract violation on
a compliance API (over-inclusive, so it hides nothing, but the filter
silently fails). Rename the params or accept both spellings with the
spec'd name canonical.

---

## MINOR

1. **`rows_indexed` counts raw provider transfers, not persisted rows.**
   `int64(len(transfers))` is passed to all `writeTerminal` calls
   (`service.go:386,389,394`), but `transfers` includes skipped rows and
   duplicate per-stream sightings (W→W seen on both streams counts twice
   for one row — `identity.go:130`). Skipped rows are double-counted in
   `rows_indexed` + `rows_skipped_*`. Use the emitted row count from
   `buildRows`.

2. **`?limit=0` bypasses the limit bound on `/exposures`.** The handler
   lacks the `n <= 0` check its `/transactions` sibling has
   (`httpserver/transactions.go:149-155` vs `:84`); `ValidateRequest`
   accepts 0 (`tracing/service.go:817`), and `limitCut` requires
   `limit > 0` (`:459`) — explicit `?limit=0` yields an unbounded item
   list (until `response_cap`), against OpenAPI `minimum: 1`
   (`api/openapi.yaml:537`).

3. **Empty `/exposures` emits `"items": null`.** `var items []Item`
   (`tracing/service.go:345`) vs OpenAPI `type: array`
   (`api/openapi.yaml:1691`). `/transactions` correctly initializes
   `[]TxItem{}` (`transaction/list.go:58`).

4. **`writeDisabled` drops `history_truncated`.** The upsert omits the
   flag → `EXCLUDED.history_truncated = false` (`service.go:416-422`).
   A `partial`+truncated wallet whose provider goes disabled loses its
   truncation flag — understates persisted coverage incompleteness.

5. **Disabled check ordered after FAILED cooldown.** `status==failed &&
   cooldown` serves the stale `failed` row (error = old ErrKind) for up to
   60s instead of recording `status=disabled`, `error='disabled'`
   (`service.go:216-223`) — the plan's disabled lifecycle wants the
   disabled terminal persisted, not a lingering failed verdict.

6. **Local budget exhaustion reported as `provider_status:
   "rate_limited"`.** `service.go:240-241` labels our own pool exhaustion
   with a provider verdict the provider never gave (the test asserts it —
   `service_test.go:332`). It is honest truncation elsewhere, but the
   ErrKind misattributes the failure source.

7. **Position-order tiebreak uses `transfer_index`, not
   `normalized_address`.** `cpOrder` inherits first-appearance order in
   the `(block_timestamp, tx_hash, transfer_index)` scan
   (`service.go:122-142`; `queries/transactions.sql:81`), while the
   declared expansion order is `(earliest edge ts, tx_hash,
   normalized_address)` (phase-03:6). Same-tx multi-counterparty ties
   order by the content hash instead of the address — deterministic, but
   not the pinned key.

8. **TX-index FAILED cooldown reuses `EnrichmentNegativeTTL`.**
   `cmd/api/main.go:244` couples the indexing cooldown to the enrichment
   knob — retuning enrichment silently changes index retry posture.

9. **Bad `asset` → `INVALID_CURSOR`.** `ErrInvalidInput` maps everything
   to `INVALID_CURSOR` (`httpserver/transactions.go:117-120`), so
   `?asset=bad` reports a cursor error.

10. **`MarkWalletIndexing` keeps the stale `error` column.** The claim
    preserves `error` (`queries/transactions.sql:91-94`); concurrent
    readers during a healthy re-index see `index_status='indexing'` with
    the previous ErrKind on `provider_status`.

---

## NIT

- `IndexOutcome.ProviderCalls` counts **steps**, not upstream calls
  (`service.go:245,325`) — dead internal field (the wire uses
  `budget.Used()`); misleading bookkeeping for future consumers.
- Stale comment claims a "UNION of the from-scan and to-scan"; the query
  is an `OR` over both indexes, no UNION (`queries/transactions.sql:32-35`).
- `var _ = wallet.NormalizeEVM` dead doc reference
  (`tracing/service.go:823`).
- `noEdges` computed then the same condition re-tested inline
  (`tracing/service.go:231-238`).
- Cook report prose uses `indexed_fresh|indexed_partial`
  (cook-report.md:44,48-49) while migration/code/wire use
  `fresh|partial` — doc-only drift.
- `seedLabelFor` hardcodes `network_id=1` in the API test
  (`tests/transactions_api_test.go:240`) — works only because ethereum is
  seed row 1.

---

## Domain invariants — held

Verified in code, not just tests:

- **unknown ≠ safe / failure never clean:** `providerStatus` emits the
  persisted ErrKind, `"ok"` only for `fresh|partial` with empty error,
  null otherwise (`service.go:512-521`); disabled writes
  `status=disabled,error='disabled'` with zero upstream calls; budget
  exhaustion serves persisted facts, never a fake empty.
- **reported ≠ verified:** label signals carry `verification_status`
  verbatim (`tracing/service.go:748-755`).
- **path ≠ ownership:** vocabulary is `direct_counterparty|indirect_hop`
  only; `path_verified=false` everywhere; the API test greps the payload
  for ownership terms (`tests/transactions_api_test.go:229-232`).
- **cross-chain never SANCTIONED:** both `CrossChainObservations` and
  `UnresolvedNetworkListings` map to `listing`/`cross_chain_listing` and
  lose every precedence contest (`service.go:758-773`); test 10/19
  verifies at real-DB level.
- **DO UPDATE mutable set** is exactly
  `{confirmation_status, block_number, block_timestamp, is_error}`
  (`queries/transactions.sql:17-21`) — reorg-tail revalidation works.
- **`.{k}` counting** is max-per-stream (`identity.go:176-181`);
  self-row/`direction`/`is_error`/`sentinel`/`max_hops` semantics all
  correct and tested; window formula, no-fetch head-lag path, refresh
  propagation to hop-2, budget rollback of raced `examined`, and
  singleflight all verified correct.

## Test quality

Genuinely real: dedicated test DBs (`check_wallet_transaction_test`,
`_tracing_test`, `_txapi_test`), dynamic `TruncateAll`, DB-backed
assertions on state rows and wire fields, and adapters exercised through
`httptest` servers with realistic envelopes — no hollow stubs.

Coverage gaps that let the findings through:

- No test sets `stub.streams` with mixed finished/unfinished streams at
  divergent blocks — B1 is structurally untested.
- Cook admits test-6 partiality for `deadline`/`max_edges`/`response_cap`
  — M1's unreachable reason lived in that gap.
- No test of the **prefix property** under a mid-sequence
  never-attempted node (only determinism is asserted); `nodes_failed`
  path is untested end-to-end.
- Test 13's "same tx via two different wallets' streams → one row" claim
  has no test — dedup is structurally sound but unverified.
- No `direction=in|out` E2E on `/exposures`; no `from_ts` filter test;
  no assertion that a mutable column actually *changes* on tail refetch
  (test 9's revalidation claim is asserted only indirectly via
  idempotency).

## Verdict

CHANGES_REQUESTED

B1 must be fixed before merge — it silently corrupts the index's core
honesty guarantee on the most common wallet shape. M1–M4 are direct
plan-contract violations; M5 is a real provider-cap data loss the plan
missed and the code inherits; M6 is a public-contract deviation.
