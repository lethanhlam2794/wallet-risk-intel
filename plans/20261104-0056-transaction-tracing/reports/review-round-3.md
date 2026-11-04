# Devin Review — Phase 6: Transaction Tracing (round 3)

Independent re-review of the uncommitted Phase-6 diff after round-2
remediation (`reports/remediation-round-2.md` claims N1–N5 resolved).
Verified in code and by re-running every gate — not from the report.
Read-only; Phase-5 leftovers excluded per scope. Assumed risk: R2→R3
(compliance-visible coverage/watermark honesty; no auth/payment boundary).

## Verification evidence (run fresh for this review)

| Command | Result |
|---|---|
| `gofmt -l .` | empty ✓ |
| `go build ./...` | pass ✓ |
| `go vet ./...` | pass ✓ |
| `go test -count=1 ./internal/transaction ./internal/tracing ./internal/provider/chainscan ./tests/` | pass (10.4s / 8.9s / 3.9s / 95.0s — real test DBs at :5433, Redis :6380) |
| `go test -count=1 -race ./internal/transaction ./internal/tracing` | clean (12.8s / 11.0s) |

---

## Round-2 findings — verification status

### N1 (major) — zero-page fetch over-claims `fetch_to` — **FIXED**

`internal/transaction/service.go:358` — the implicit-stream arm now
requires `token == "" && fetchErr == nil && !budgetStop` for the
`fetchTo` claim; otherwise `maxBlock−1`, else `fetchFrom−1`, then the
`newWatermark < watermark` no-regress guard (`:384-386`) preserves the
prior watermark. The companion terminal arm already carries
`!budgetStop` (`:409`), so a `steps==0 && budgetStop` run lands
`default` → `partial` + `history_truncated` with `indexed_until`
NULL/preserved — never `fetchTo`. Verified `token=="" && budgetStop`
implies `steps==0` (a done-token breaks the loop inside the iteration,
before the next `tryTake`), so the fallback is exactly the zero-page
case the finding described. The BSC-8-pool scenario in the finding now
persists `partial`+null watermark instead of a fake `fetchTo`. ✔

### N2 — `recordCap` only on full pages — **FIXED**

`adapter.go:289-292` — the cap check `(*cur-1)*size+len(rows) >=
recordCap` runs **before** `!more`, so cumulative ≥ 10000 marks
`tok.Capped[action]` and drops the cursor on any page — full, short, or
cap-straddling for a `PageSize` that doesn't divide 10,000 (e.g. page 34
at offset 300 → `33*300+100 = 10000` → `Capped`). Declared trade-off
confirmed: a stream of exactly 10,000 records stays `Finished=false`
(conservative under-claim, refetches tail) — the safe direction for an
index-honesty invariant. ✔

### N3 — `TestExposures_InfraErrorPropagates` hollow — **FIXED**

`internal/tracing/service_test.go:432-437` — `stubTx.hook` runs inside
`LookupTransfers` under the stub mutex and calls `pool.Close()` via
`sync.Once` only when `addr == cpA`. Traced the fault path end-to-end:

- Root `EnsureIndexed` is synchronous (service.go:95) and completes
  before the worker pool spawns (`:201`) — the pool is alive for all of
  root's indexing.
- cpA's `ensure()` order: `GetOrCreate` → `loadState` →
  `MarkWalletIndexing` → `LookupTransfers` (hook → `pool.Close()`).
  `stub.pages[cpA]` is unset → empty page → `buildRows` early-returns
  `"[]"` without touching the pool → `UpsertTransactions` skipped →
  first post-close DB call is `UpsertWalletIndexState` inside
  `writeTerminal` → `pgxpool.ErrClosed`.
- `ErrClosed` is a plain `errors.New` value — not context-shaped — so
  the worker's `:228-229` classification falls to `return err` →
  errgroup → `Exposures` → 500. The fault lands exactly on the M2
  branch.

Discrimination verified: with M2's `deadlineHit`-on-any-error restored,
the same run yields `err=nil`, `truncated="deadline"`, `res≠nil` → the
test fails. Determinism verified: `RequireTestPoolDSN` builds a
per-test pool (`pgxpool.New` + `t.Cleanup(pool.Close)` — double-Close
is safe); a single position means exactly one worker reaches cpA while
the other three exit via `i >= len(positions)`; `once` is defensive
(idempotent second call can't occur — the page's done token ends the
loop). No flakiness vector found. ✔

### N4 — `ProviderCalls` arithmetic — **FIXED**

`service.go:250` sets `out.ProviderCalls = 1` unconditionally after
`HeadBlock` returns (a failed head still counts — before the `err`
check), and `:311` accumulates `page.UpstreamCalls` per step → total is
`1 + Σ UpstreamCalls_i`, the real call count. Consistent with the
budget settlement: `tryTake(1)` head + `tryTake(1)` per step +
`charge(U_i−1)` ⇒ `Used() = 1 + ΣU_i` — the internal field and the wire
value now agree. ✔

### N5 — dead `ixState.claimed` — **FIXED**

`service.go:143-146` — field gone; grep finds no reference anywhere. ✔

---

## NEW findings (fresh hunt)

### MINOR

#### O1 — The N1 fix has no regression test: the `steps==0 && budgetStop` path is exercised by nothing

`internal/transaction/service_test.go`: `TestEnsureIndexed_BudgetZero`
(`:317-333`) manually spends the pool *before* the call, so it exits on
the pre-head `BudgetExhausted` arm (`service.go:239-248`) — the
`tryTake(1)` for the head never runs. The N1 path is the *next* gate:
head succeeds on the last pool unit, the first page's `tryTake` fails →
`budgetStop`, `steps=0` → `fetchFrom−1` fallback. No test puts the
budget there (`Budget: map[string]int{stub.Name(): 2}` in the tracing
suite is the closest but still indexes a page). A stub page that needs
`head + 1` calls with `NewBudget(1)` would discriminate: bug restored →
`indexed_until=fetchTo`; fixed → NULL/`fetchFrom−1`. This is the same
hollow-coverage class round 2 flagged as N3 — the remediation fixed
N3's test properly but didn't add one for N1. The code is correct by
inspection and all gates pass; the fix is simply unprotected against
re-regression.

#### O2 — `truncated=max_provider_requests` can fire without causal truncation under `max_hops=1`

`internal/tracing/service.go:510` — the reason arm is
`budget.Exhausted() && nodesRemaining > 0`. Under `max_hops=1` the plan
pins `nodesRemaining = counterpartiesTotal` (plan.md:268-270, 293-294),
so the arm fires whenever the root index lands the pool exactly on its
limit while completing: a fully-`fresh` root + complete hop-1 answer
reports `truncated="max_provider_requests"` — a bound that was reached
but truncated nothing the request asked for. The sibling `max_nodes`
arm is explicitly gated `&& maxHops == 2` (`:512`); the budget arm is
not, even though `nodesRemaining` under `max_hops=1` reflects request
shape (positions exist but expansion was never requested), not a bound
that bit. Reachable: the existing `max_provider_requests` test fixture
(`Budget{chainscan:2}`, root = head+1 page ⇒ `used==limit`) produces it
verbatim under `max_hops=1`. Pre-existing since round 1 — not opened by
the r2 fixes — and conservative-direction (over-reports boundedness,
never hides it; `coverage.root.index_status` still shows `fresh`), but
on a compliance surface the reason misattributes causality. Honest when
the bound actually truncated the root index (partial index ⇒ incomplete
hop-1 edges ⇒ reason apt); false only in the exact-exhaustion-complete
corner. Suggested shape: fire the arm only when the bound actually
truncated something emitted (e.g. `maxHops == 2` gate, or require
`rootOutcome.BudgetExhausted`/partial root).

### NIT

#### O3 — A failed step's partial `UpstreamCalls` are uncounted

`internal/provider/chainscan/adapter.go:301-306` and
`nodereal/adapter.go:327-330`: if `txlist`'s `accountPage` succeeds and
`tokentx`'s fails, `LookupTransfers` returns `nil, err` and discards the
page — its `UpstreamCalls=1` never reaches the service, so
`out.ProviderCalls` (`service.go:311`) and `budget.Used()` (wire
`coverage.provider_calls`, `tracing/service.go:535`) each undercount the
real upstream calls by ≤1 on a failed run. Same for the `charge(U−1)`
settlement. Internal bookkeeping only; conservative direction (the pool
permits ≤1 extra real call, never hides failure — the run is `failed`
regardless).

---

## Adversarial pass results (prompt's re-hunt list)

1. **`allFinished` + `!budgetStop` — full terminal-switch enumeration.**
   Every loop exit lands a consistent (status, watermark) pair:
   - `fetchErr` (any step) → `failed` + ErrKind + guarded coverage +
     preserved `history_truncated` (`:404-408`, `:470-473`).
   - done-token + all streams finished + `fetchTo==head` + no
     budget-stop → `fresh` + `fetchTo` (`:409-411`).
   - budget-mid (`steps≥1`, `token!=""`) → `partial` + per-stream
     coverage; budget-zero (`steps==0`) → `partial` + `fetchFrom−1`/
     preserved; window-cap (`fetchTo<head`, all done) → `partial` +
     `fetchTo` (pinned bound, plan.md:365-366); capped stream done →
     `partial` + stream's own `max−1`.
   No arm writes `fresh`/`fetchTo` without every declared stream
   finished and every page fetched. ✔
2. **`fetchFrom−1` for a `streamSeen` member missing from
   `streamMaxBlock`** — correct and safe: only a rowless *unfinished*
   stream takes it (rowless+finished is `continue`d; rowless+unfinished
   can't arise from chainscan in-run — a zero-row page is `more=false`).
   Under tail overlap (`fetchFrom < watermark`), `fetchFrom−1 ≤
   watermark` → the no-regress guard preserves prior coverage; any
   under-claim refetches. An unfinished stream whose refetched rows sit
   at/below the old watermark still computes `own−max−1 < watermark` →
   guard catches. Over-claim impossible via this arm. ✔
3. **Capped-token replay** — `Capped` survives encode/decode
   (`{"a":3,"c":{"txlist":true}}` round-trips; `decodeToken` re-inits a
   nil map, `:214-216`); the `finished` init `cur==nil && !Capped`
   (`:251-252`) keeps a capped stream `Finished=false` on every later
   step while a sibling cursor lives; both-nil → `encodeToken ""` →
   `allFinished=false` → `partial`. Backward-compatible with pre-Capped
   tokens. ✔
4. **`firstEdge`/`cpOrder` sort vs test-22; `pickEdge` divergence** —
   confirmed intentional: `firstEdge` picks a cp's earliest edge in
   scan order `(ts, tx_hash, transfer_index)` for *position* ordering;
   the `cpOrder` comparator then applies the pinned `(firstEdge.ts,
   firstEdge.tx_hash, addr)` key (`:145-155`); `pickEdge` applies the
   pinned *evidence* rule `(ts → amount_raw → tx_hash,transfer_index)`
   (`:636-658`) to both e1 and e2. Distinct jobs, distinct keys —
   `TestExposures_Deterministic`'s cpC/cpA/cpB order holds because
   distinct ts dominates either key. Sentinel item ordering uses its
   cpOrder rank — a coherent reading of plan.md:301-303 ("a sentinel
   item uses its own rank in the hop-1 expansion ordering as its
   index"); uniform-rank yields the same relative order for
   non-sentinels. ✔
5. **CAS loop liveness + `edgesClipped`** — `left` only decreases
   (CAS-subtracts `≤ avail`), so `avail ≥ 0` invariantly; `use=0` hits
   `CAS(0,0)` which trivially succeeds → return, no spin; a failed CAS
   implies a sibling committed progress → bounded retries. `clipped`
   marks both the boundary-probe clip (`len(rows) > use`) and the
   skipped-scan case (`remain ≤ 0` early return — conservative: the
   node may have had edges). Reason requires `edgesLeft ≤ 0 &&
   (clipped || positions > examined)` — exact-zero-budget-with-nothing-
   lost correctly reports `null`. ✔
6. **M2 test determinism** — verified above (N3): single position →
   one worker; hook serialized under stub mutex; first post-close DB
   call deterministically `UpsertWalletIndexState`; per-test pool —
   no cross-test poisoning, no ordering assumption beyond the
   guaranteed sequential root index. ✔
7. **Other diff surface since round 2** — only the five claimed touch
   points changed (coverage arm condition + comment, cap-check order +
   comment, stub `hook` field + rewritten M2 test, `ProviderCalls`
   accumulation, `claimed` removal). Modified non-new files
   (`errors.go`, `provider.go`, `config.go`, `metrics.go`,
   `testdb_integration_test.go`, `openapi.yaml`, `main.go`) are all
   phase-5/6 scope — idempotency error codes, `TxProvider` contract,
   indexing/tracing knobs, `IncTracingNodesExamined`, seeded-source
   list update — no scope creep, no unrelated edits. `ProviderCalls=1`
   sits before the head-error check so a failed head still counts —
   consistent with the N4 claim. ✔

## Domain invariants — still held

- `providerStatus` total rule unchanged (`:535-545`): persisted ErrKind
  verbatim, `"ok"` only for `fresh|partial` with empty error, `null`
  otherwise; budget exhaustion emits no verdict.
- `partial` + `history_truncated` written atomically (`:413-416`,
  `:470`); `failed` preserves prior truncation truth; `disabled`
  preserves it too (`:444`).
- `RelDirect|RelIndirect` only — `path_verified=false` everywhere;
  `temporal_order_valid` is a timestamp ordering, not an ownership
  claim; API test greps the payload for ownership vocabulary.
- Cross-chain/unresolved listings → `listing`/`cross_chain_listing`,
  lose every precedence contest; never `SANCTIONED`.
- `reported`/`unverified` `verification_status` carried verbatim into
  `matched_signals`.
- Raw-integer `amount_raw` strings end-to-end (`NUMERIC(78,0)`);
  `DO UPDATE` mutable set unchanged; `.k` max-per-stream identity
  unchanged; provider-switch reset still untrusts foreign watermarks.

## Test quality

N3's regression is now genuinely discriminating (traced the fault path
— it would fail with the bug restored). Gates all green on real
infrastructure. Remaining gaps: O1 above (no zero-page budget-stop
test); no test asserts `truncated` stays `null` under
`max_hops=1`+exact-exhaustion (would pin O2's corner); the carried-over
`direction=in|out` E2E gap on `/exposures` still stands.

## Verdict

**APPROVED**

All five round-2 findings are genuinely fixed in code with correct
mechanics — N1 closes the last over-claim arm of the coverage switch
(both the watermark and the status arm now require `!budgetStop`), N2
moves the cap check ahead of `!more`, N3's test is now discriminating
rather than ceremonial, N4's arithmetic finally matches its claim, and
N5's dead field is gone. The fresh hunt found no blocker or major: O1
(missing N1 regression test) and O2 (causality-free
`max_provider_requests` under `max_hops=1`, pre-existing) are safe to
schedule; O3 is a bookkeeping nit. The index-honesty invariants this
phase exists to protect hold in every arm of the terminal switch.

> Ledger note: a recurring pattern is visible — *fix lands without a
> discriminating regression test* (N3 in round 2, N1's missing test
> now). Recording this as a learning-ledger occurrence is skipped: the
> review's write scope is restricted to `reports/` per the prompt.
