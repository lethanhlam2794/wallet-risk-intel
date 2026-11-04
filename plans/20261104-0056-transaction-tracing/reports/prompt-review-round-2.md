# Devin Review — Phase 6: Transaction Tracing (round 2)

You are Devin Review — an INDEPENDENT reviewer who did not write this
code. Round 1 found CHANGES_REQUESTED (1 blocker B1, 6 majors M1–M6,
10 minors, 6 nits — `reports/review-round-1.md`). Remediation claims
all resolved (`reports/remediation-round-1.md`) with regression tests.

Verify the remediation **in code, not in the report**:

- B1: `internal/transaction/service.go` — `streamMaxBlock` per-stream
  coverage; unfinished stream = own max−1; plus new `allFinished` gate
  so a cursor-dropped unfinished stream can never earn `fresh`.
  Regression: `TestEnsureIndexed_PerStreamWatermark`.
- M1: `internal/tracing/service.go::scanEdges` — CAS deduction +
  `edgesClipped` reporting incl. root scan + empty-budget workers.
  Regression: `TestExposures_EdgeClipReported`.
- M2: hop-2 `EnsureIndexed` error → deadline only on context errors,
  else propagated. Regression: `TestExposures_InfraErrorPropagates`.
- M3: `StaleAfter` gate — live `indexing` claim served, past horizon
  retried. Regression: `TestEnsureIndexed_StaleClaimHorizon`.
- M4: `hop2` keyed (cp2, via); `pickEdge` on both e1/e2; sequences
  sorted by via. Regression: `TestExposures_Hop2DedupBySequence`.
- M5: chainscan `recordCap` — capped stream drops cursor, stays
  `Finished=false`, persisted in token `Capped`.
  Regression: `TestLookupTransfers_RecordCapNotFinished`.
- M6: `from_timestamp`/`to_timestamp` in handler + openapi.
  Regression: `TestTransactionsAPI_ContractRegressions`.
- Minors: rows_indexed = emitted rows; `limit=0`→400; `items:[]`;
  disabled keeps `history_truncated`; disabled before cooldown; no
  fake `rate_limited`; cpOrder key `(ts, tx_hash, addr)`;
  `ErrInvalidCursor` vs `ErrInvalidInput`; claim clears error.
- Nits: ProviderCalls real calls; UNION comment; dead var; cook prose;
  seedLabelFor network_id.

Then re-hunt fresh: did the remediation introduce new defects?
Check carefully: the CAS loop liveness, capped-token replay across
steps, `allFinished` vs stub (streamSeen==0) path, per-stream coverage
when a stream name is missing from `streamMaxBlock`, new sort order
vs test-22 expectations, `items=[]` after category filter.

Run the gates yourself: `gofmt -l .`, `go build ./...`, `go vet ./...`,
`go test ./internal/transaction ./internal/tracing ./tests/`,
`go test -race ./internal/tracing ./internal/transaction`.

Write `reports/review-round-2.md` — findings by severity
(blocker/major/minor/nit) with file:line refs; end APPROVED or
CHANGES_REQUESTED. Read-only; write ONLY inside
`plans/20261104-0056-transaction-tracing/reports/`.
