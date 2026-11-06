# RE-REVIEW REQUEST — Round 2 (verify remediations, issue final verdict)

You are the same independent Devin Review session that issued CHANGES_REQUESTED in round 1 (report: `plans/20261106-1946-tron-enablement-prep/reports/review-round-1.md`). The orchestrator has applied remediations. Re-verify each finding and issue a final verdict.

## READ-ONLY MANDATE (unchanged)

You MUST NOT edit, create, or delete any file. Verification only. The tree hash is captured before and after your session.

## Round-1 findings and claimed remediations

1. **[Major] `adapter.go` trx `limit` unclamped** — indexer `PageSize=1000` → TronGrid HTTP 400.
   - Claimed fix: new const `upstreamLimitMax = 200` (adapter.go); `size` clamped after `PageSize` defaulting in `LookupTransfers`; new test `TestLookup_LimitClamped` sends `PageSize: 1000` and asserts every wire `limit` ≤ 200.
   - Verify: the clamp covers BOTH the trx stream and any other `limit` consumer; test actually inspects the query param upstream receives.

2. **[Minor] `mapper.go:80` `ret[]` shorter than `contract[]` → silent `IsError=false`**.
   - Claimed fix: `i >= len(r.Ret)` now returns `ErrMalformed` (honest malformed, never fabricated success); new test `TestLookup_RetContractMismatch`.
   - Verify: fix is inside the `TransferContract` loop so scope-excluded rows (e.g. `TriggerSmartContract` anchors) are NOT newly broken by ret-length issues they don't need.

3. **[Minor] untested fixtures**: `success:false` envelope, txinfo `Error` path, multi-`TransferContract` tx.
   - Claimed: new tests `TestLookup_SuccessFalseEnvelope` (ErrDegraded), `TestLookup_TxInfoErrorPath` (ErrDegraded, no page emitted), `TestLookup_MultiContractTx` (2 transfers, parallel `ret[1]` feeds `IsError=true`).
   - Verify each asserts the claimed error kind/behavior, not just "some error".

4. **[Nit] doc drift**: `'evm'` literal line numbers, "37 packages"→36, "only stdlib" claim.
   - Claimed: citations updated to `transactions.sql:30`, `ofac_publish.sql:57`, `labels_publish.sql:12`, `stablecoin_freeze.sql:61` (in `docs/tron-indexer-semantics.md`, `reports/audit-drift-check.md`, `phase-01`, `phase-03`); `pipeline-log.md` now says 36 packages; `final-report.md` now says "go.mod/go.sum untouched; stdlib + pre-existing `golang.org/x/time`".
   - Verify line numbers against `grep -n "'evm'" apps/api/queries/*.sql`.

## Required verification (fresh terminal execution)

- `cd apps/api && gofmt -l .` → empty
- `go vet ./...` and `go build ./...` → exit 0
- `go test -count=1 -v ./internal/provider/trongrid/` → all tests pass (was 23, now 28 incl. 5 new)
- `go test -count=1 ./internal/wallet/ ./internal/transaction/` → pass
- Confirm no new files outside `internal/provider/trongrid/`, no migration/go.mod/go.sum drift, no `trongrid` imports outside the package.

Note: a `TestExposures_PrefixTruncationDeep` flake appeared in one full-suite run under parallel load; `internal/tracing` is untouched by this diff and the test passes on re-run in isolation. Assess whether this is relevant.

## Output

Write your verdict to `plans/20261106-1946-tron-enablement-prep/reports/review-round-2.md` — WAIT, read-only mandate: do NOT write files. Instead print the full report to stdout headed `## Verdict: APPROVED` or `## Verdict: CHANGES_REQUESTED` with per-finding verification evidence and any remaining required actions.
