# Devin Review — Phase 6: Transaction Tracing (round 2)

Independent re-review of the uncommitted Phase-6 diff after round-1
remediation (`reports/remediation-round-1.md` claims all 23 findings
resolved). Verified in code and by re-running every gate — not from the
report. Read-only; Phase-5 leftovers excluded per scope.

## Verification evidence (run fresh for this review)

| Command | Result |
|---|---|
| `gofmt -l .` | empty ✓ |
| `go build ./...` | pass ✓ |
| `go vet ./...` | pass ✓ |
| `go test -count=1 ./internal/transaction ./internal/tracing ./internal/provider/chainscan ./internal/provider/nodereal` | pass (9.9s / 8.5s / 3.9s / 1.7s — real test DBs) |
| `go test -count=1 -run 'TestTransactionsAPI\|TestExposuresAPI' ./tests/` | pass (2.6s) |
| `go test -count=1 -race ./internal/tracing ./internal/transaction` | clean (23.2s / 31.5s) |

---

## Round-1 findings — verification status

### B1 — per-stream watermark + `allFinished` gate — **FIXED**

`internal/transaction/service.go:290` declares `streamMaxBlock`
populated per `t.Stream` (`:328-330`); the per-stream coverage loop
charges an unfinished stream **its own** max−1, or `fetchFrom−1` when it
emitted no rows (`:365-382`). `allFinished` (`:397-402`) gates `fresh`
(`:409`) — a cursor-dropped unfinished stream can no longer earn it.
Regression `TestEnsureIndexed_PerStreamWatermark` (service_test.go:542)
genuinely exercises the declared-streams branch the round-1 suite never
reached: `indexed_until=399`, `partial`, `history_truncated`. ✔

**But the same switch still has a hole — see new MAJOR N1 below.**

### M1 — CAS deduction + `edgesClipped` — **FIXED**

`scanEdges` (tracing/service.go:572-603): `use = min(len(rows), avail)`
under a `CompareAndSwap` loop; `clipped` set on any clip and on the
empty-budget early return (`:577-579`). Truncation switch `:514-516`
fires `max_edges` when `edgesLeft ≤ 0 ∧ (clipped ∨ positions>examined)`.

Liveness audit of the CAS: `left` starts at `MaxEdges` and every CAS
subtracts `≤ avail`, so `avail ≥ 0` invariantly — no negative use, no
slice panic; `CAS(0,0)` trivially succeeds so the `use=0` case returns
instead of spinning; a failed CAS implies a sibling committed progress.
No liveness defect. ✔

`TestExposures_EdgeClipReported` (service_test.go:396) is real: 5 root
edges vs `MaxEdges=3` → `truncated=max_edges`, `edges_examined ≤ 3`. ✔

### M2 — hop-2 error propagation — **FIXED**

`service.go:224-234`: `deadlineHit` only when `gctx.Err() != nil` or the
error is `context.DeadlineExceeded/Canceled`; otherwise `return err` →
errgroup → `Exposures` → 500. Matches the sibling `scanEdges` error
branch (`:258-264`). ✔

Test caveat — `TestExposures_InfraErrorPropagates` closes the pool
*before* `Exposures`, so the root's `GetOrCreate` fails long before the
hop-2 worker branch executes. The regression would still pass with the
M2 bug restored. Code is correct by inspection; see MINOR 2 for the
test-adequacy gap.

### M3 — `StaleAfter` gate — **FIXED**

`service.go:225-230`: `status='indexing' ∧ updated_at ≤ now−StaleAfter`
serves persisted facts (cross-process live claim); past the horizon it
falls through to the fetch path. Ordered correctly after the fresh /
disabled / FAILED-cooldown cases. `TestEnsureIndexed_StaleClaimHorizon`
(service_test.go:569) asserts both arms against real DB rows — live
claim → zero lookups + `index_status='indexing'`; 1 h-old → retried. ✔

Remnant: `ixState.claimed` (service.go:146) is still declared and never
set — dead field (nit, below).

### M4 — hop-2 keyed by node sequence — **FIXED**

`hop2 map[string]map[string]*hop2Seq` keyed `(cp2, via)` (`:298-323`);
sequences sorted by via addr (`:397`) — since root and cp2 are fixed per
item, via-addr order *is* the lexicographic node-sequence order the plan
pins — cut at `MaxPathsPair` per **sequence** (`:398-399`); `pickEdge`
applied to both `agg.edges` (e1) and `seq.e2s` (e2) (`:408`).
`TestExposures_Hop2DedupBySequence` (service_test.go:437) verifies one
path per sequence and the smallest-amount e2 pick. Determinism:
`vias` keys are unique map keys; items re-sorted `(hops, minPos,
counterparty)`. Test-22 (`TestExposures_Deterministic`) expectations
still hold — the pinned `(ts, tx_hash, addr)` key yields the same order
on distinct-ts fixtures. ✔

### M5 — chainscan `recordCap` — **FIXED** (with a residual, MINOR 1)

`adapter.go:289-292`: a full page reaching cumulative `recordCap` marks
`tok.Capped[action]`, drops the cursor, and leaves `finished[action]`
false — reported `Finished=false` in `page.Streams`. Token replay across
steps verified: `finished` init (`:250-253`) keeps a capped stream
unfinished while the sibling cursor lives; when both cursors are nil,
`encodeToken` returns `""` and the service's `allFinished` gate (B1
companion) holds `partial`. `TestLookupTransfers_RecordCapNotFinished`
(adapter_test.go:121) is real — 10 × 1000-row pages to the cap, cursor
gone, stream unfinished. ✔

### M6 — `from_timestamp`/`to_timestamp` — **FIXED**

Handler (`httpserver/transactions.go:91-108`), OpenAPI
(`api/openapi.yaml:473-478`), spec §9.9 (`docs/tailieu.md:1290-1293`)
all agree. Old `from_ts` spellings are gone — not silently accepted.
`TestTransactionsAPI_ContractRegressions` proves the filter narrows
results (not merely parses). ✔

### Minors — all verified

1. `rows_indexed` = emitted rows — `buildRows` returns `len(rows)`
   (identity.go:209) → `rowCount` (service.go:340, 408, 415). ✔
2. `?limit=0` → 400 on /exposures (transactions.go:155). ✔
3. `items:[]` — `items := []Item{}` (:360) and `keep := []Item{}` (:437)
   — the category-filter keep-list is non-nil. ✔
4. `writeDisabled` preserves `history_truncated` (service.go:444). ✔
5. Disabled re-check precedes FAILED cooldown (service.go:218 vs :222). ✔
6. Budget exhaustion emits no fake `rate_limited` — `fillFromState`
   only; `TestEnsureIndexed_BudgetZero` asserts `ProviderStatus` nil
   (service_test.go:331-332). ✔
7. `cpOrder` key is `(firstEdge ts, tx_hash, addr)` (service.go:145-155)
   — `transfer_index` no longer the tiebreak. ✔
8. FAILED-cooldown reuse of `EnrichmentNegativeTTL` — accepted as
   designed; verified plan §3.2 pins it verbatim (plan.md:130). ✔
9. `ErrInvalidCursor` → `INVALID_CURSOR`, `ErrInvalidInput` →
   `BAD_REQUEST` (transactions.go:117-126; list.go:73, 93). ✔
10. `MarkWalletIndexing` clears `error` — `error = NULL` in query
    (queries/transactions.sql:95) and regenerated sqlc
    (transactions.sql.go:327); `TestMarkWalletIndexing_ClearsError`. ✔

### Nits — verified

- UNION comment → corrected to OR-predicate bitmap
  (queries/transactions.sql:32). ✔
- `var _ = wallet.NormalizeEVM` + import gone from tracing/service.go. ✔
- `noEdges` collapsed to a single named condition (service.go:251-253). ✔
- Cook-report prose `indexed_fresh|indexed_partial` → `fresh|partial`
  (grep: clean). ✔
- `seedLabelFor` resolves ethereum's real `network_id` via subquery
  (transactions_api_test.go:240). ✔
- `ProviderCalls` — **the attempted fix is still wrong**; see nit N4.

---

## NEW findings (fresh hunt)

### MAJOR

#### N1 — Zero-page fetch still claims `fetch_to`: the `len(streamSeen)==0` arm conflates "no cursor seen" with "stream completed" → silent permanent index loss (B1-class)

`internal/transaction/service.go:296-300`: the pagination loop breaks on
`budgetStop` *before* the first `LookupTransfers` when the request pool
covers the head call but not page 1. That leaves `steps=0`,
`token=""` (the initial value, not a done-token), `streamSeen` empty.

The stub/implicit-stream arm then reads `token=="" && fetchErr==nil` as
"complete window" (`:355-364`):

```go
case len(streamSeen) == 0:
    // Stub/implicit stream: done token ⇒ complete window.
    if token == "" && fetchErr == nil {
        newWatermark = fetchTo        // ← zero rows fetched, full-window claim
    }
```

`!budgetStop` keeps `fresh` unreachable, so the row lands as
`partial` + `indexed_until_block = fetchTo` + `history_truncated`
(`:409-416`) — the *worst* watermark possible for a wallet with zero
fetched rows. The next request computes
`fetch_from = fetchTo − tail + 1` and **skips the entire window
permanently** — the identical silent-index-loss mechanism B1 blocked on,
through a different arm of the same switch the remediation touched.

Reachable with default wiring, not just misconfiguration:

- `/exposures` shares one pool across root + all hop-2 nodes. On BSC
  (`IndexingBudgetBSC=8`), let root consume 4 calls and node-1 consume 3
  (head + two single-stream pages — common when one stream finishes
  early). `used=7`; node-2 passes the `Exhausted` gate, its head call
  takes unit 8, its first page's `tryTake` fails → `budgetStop`,
  `steps=0`, `token=""` → the node persists
  `indexed_until=fetchTo` with **zero transfers indexed**. Its
  `node_indexed_until` reports `fetchTo` while its edge set is empty —
  hop-2 silently incomplete *and* coverage-falsified; subsequent
  `/transactions` requests serve the fake watermark inside FreshTTL.
- `/transactions` reaches it via `TXINDEX_BUDGET_*=1`.

Suggested shape: the `fetchTo` arm needs `steps > 0` (or `!budgetStop`)
— `steps==0 && budgetStop` should leave `fetchFrom−1` so the
`newWatermark < watermark` no-regress guard preserves prior state.

### MINOR

#### N2 — `recordCap` detection only fires on a *full* page — page sizes that don't divide 10,000 leave a residual over-claim

`adapter.go:285-292`: the cap check sits after `!more` (`len(rows) <
size` → `finished`). For a `PageSize` that doesn't divide 10,000 (e.g.
300), the cap-straddling page (page 34 → `34×300=10200 > 10000`) is
served by upstream as a short page or an empty `status=0` — both read as
`more=false` → `Finished=true` → coverage claims `fetchTo` while
sub-cap rows (9901–10000) are unfetched and everything beyond the cap is
invisible. `TXINDEX_PAGE_SIZE` is operator-configurable; the default
1000 divides 10,000 evenly so the default path is safe — this is a
defensive residual, not a live default defect.

#### N3 — `TestExposures_InfraErrorPropagates` doesn't exercise the branch it regresses

`service_test.go:420-432`: `pool.Close()` makes the root's
`wallets.GetOrCreate` fail before any position is pulled — the hop-2
worker error path (`service.go:224-234`) is never reached. Reintroduce
M2's `deadlineHit`-on-any-error bug and this test still passes. Code is
correct by inspection, but the regression coverage is hollow — the same
class of gap round 1 flagged for B1's stub streams. A discriminating
test needs a stub/provider that errors non-contextually *only* for a
hop-2 node (or a DB fault injected after the root index).

### NIT

#### N4 — `ProviderCalls` "real calls" formula is still wrong

`service.go:312-315`:

```go
out.ProviderCalls = 1 + int64(steps-1)*1 // = steps — not head+steps
if page.UpstreamCalls > 1 {
    out.ProviderCalls += int64(page.UpstreamCalls) - 1
}
```

The `=` assignment each step clobbers prior `+= UpstreamCalls−1`
accumulations, so the final value is `steps + U_last − 1` — neither the
old step-count nor real calls (`1 + Σ UpstreamCalls_i`; with 2-call
pages it's under by `n−1`). Harmless today — the field is `json:"-"`,
the wire reads `budget.Used()` (`:535`), no test asserts it — but the
nit fix does not deliver what it claims; misleading bookkeeping
persists.

#### N5 — `ixState.claimed` is dead state left over from M3

`service.go:146` — declared, never set or read. The stale-claim gate
reads `st.row.UpdatedAt` directly; delete the field.

---

## Adversarial pass results (prompt's re-hunt list)

- **CAS loop liveness** — clean: `avail ≥ 0` invariant, `CAS(0,0)`
  trivially terminates the `use=0` case, a failed CAS implies sibling
  progress. No spin, no negative-slice panic.
- **Capped-token replay across steps** — clean: `finished` init
  `cursor==nil && !Capped` keeps a capped stream `Finished=false` on
  every subsequent page while its sibling's cursor lives; both-nil →
  `encodeToken ""` → service's `allFinished` holds `partial`.
- **`allFinished` vs stub (`streamSeen==0`) path** — **fails**: N1.
- **Missing stream names in `streamMaxBlock`** — safe direction:
  declared-but-rowless stream → `fetchFrom−1` (under-claim refetches);
  undeclared `t.Stream` rows never raise coverage. Over-claim only via
  adapter contract violation.
- **New sort order vs test-22** — consistent: `(firstEdge ts, tx_hash,
  addr)` matches `TestExposures_Deterministic`'s cpC/cpA/cpB order.
- **`items=[]` after category filter** — `keep := []Item{}`, non-nil.
- **Probe-row semantics in `scanEdges`** — correct both ways:
  `len(rows) ≤ remain` proves the scan complete (the +1 probe would
  have returned `remain+1` if more existed → no clip → no reason);
  `len(rows) > use` (boundary row or concurrent shrink) → `clipped`.
- **`remain ≤ 0` early return marks `clipped`** — conservative but
  honest: the skipped scan may have had edges; reporting `max_edges`
  only when `edgesLeft ≤ 0` anyway. Not a false positive.
- **`hop2`/`minPos`/`viaPos`** — `viaPos == results index` (positions
  are distinct cps); `minPos` = min via position = "min contributing
  position index" per phase-03. `agg == nil` guard is unreachable
  defense-in-depth.
- **`charge` overdrawing the pool** — allowed and correct: prepaid
  `tryTake` bounds new work; `charge(U−1)` settles real calls past the
  limit so `Used()` stays truthful.

## Domain invariants — still held

- `providerStatus` total rule unchanged (`:535-545`); disabled /
  budget-exhausted paths emit no unearned verdict.
- `partial`+`history_truncated` written together (`:413-416`);
  `failed` preserves prior truncation truth (`:470-473`).
- `reported`/`unverified` verification_status carried verbatim; no
  ownership vocabulary anywhere (`RelDirect|RelIndirect` only).
- Cross-chain/unresolved listings never promote to SANCTIONED.
- DO UPDATE mutable set unchanged; max-per-stream `.k` counting
  unchanged; reorg-tail refetch path unchanged.

## Test quality

New regression tests are real (dedicated DBs, DB-backed assertions,
upstream-shaped httptest fixtures, 10-page cap walk). Remaining gaps:
N3 above; no test puts `budgetStop` at `steps==0` (would have caught
N1); no multi-step UpstreamCalls>1 assertion for `ProviderCalls` (would
have caught N4); still no `direction=in|out` E2E on /exposures (carried
from round 1).

## Verdict

**CHANGES_REQUESTED**

All six majors and the blocker claim are genuinely fixed in code with
real regression coverage — the remediation is high quality. But the
fresh hunt found **N1**: a zero-page fetch still persists
`indexed_until_block = fetchTo` through the `len(streamSeen)==0` arm —
the same silent-coverage-over-claim invariant B1 blocked on, reachable
with default BSC pool sizing via /exposures. It sits in the switch the
remediation edited, so it cannot be waved through as out-of-scope.
N1 must be fixed before merge; N2–N5 are safe to schedule but should be
recorded.
