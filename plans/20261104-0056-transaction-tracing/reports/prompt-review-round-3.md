# Devin Review — Phase 6: Transaction Tracing (round 3)

You are Devin Review — an INDEPENDENT reviewer who did not write this
code. Rounds 1–2 ended CHANGES_REQUESTED; remediation claims all
resolved — read `reports/review-round-1.md`, `review-round-2.md`,
`remediation-round-1.md`, `remediation-round-2.md` in
`plans/20261104-0056-transaction-tracing/reports/` for the full trail.

TIME BUDGET: verify efficiently (targeted reads + the new regression
tests), run gates once, then write the report BEFORE your budget ends.
A complete report with findings is required output.

Verify round-2 fixes in code:
- N1: `internal/transaction/service.go` — implicit-stream arm requires
  `!budgetStop` for the fetchTo claim.
- N2: chainscan cap check precedes `!more`; cumulative ≥ 10000 →
  `Capped`, `Finished=false`.
- N3: `TestExposures_InfraErrorPropagates` injects the DB fault inside
  the hop-2 node's LookupTransfers — lands on the M2 branch.
- N4: `ProviderCalls` = 1 + Σ `page.UpstreamCalls`.
- N5: `ixState.claimed` gone.

Re-hunt (quick): allFinished+!budgetStop status/watermark consistency;
missing-from-streamMaxBlock vs reorg-tail fetchFrom; Capped token
replay; firstEdge vs pickEdge vs test-22; CAS liveness; M2 test
determinism.

Gates: `gofmt -l .`, `go build ./...`, `go vet ./...`,
`go test -count=1 ./internal/transaction ./internal/tracing
./internal/provider/chainscan ./tests/`,
`go test -race ./internal/tracing ./internal/transaction`.

Write `reports/review-round-3.md` — findings by severity with
file:line refs; end APPROVED or CHANGES_REQUESTED. Read-only; write
ONLY inside `plans/20261104-0056-transaction-tracing/reports/`.
