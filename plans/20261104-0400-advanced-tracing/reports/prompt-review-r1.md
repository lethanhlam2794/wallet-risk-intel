# Devin Review — Phase 7 Advanced Tracing (round 1)

You are an independent reviewer. The plan at
`plans/20261104-0400-advanced-tracing/` reached PLAN_CONSENSUS_REACHED
(7 critic rounds) and Cook reports implementation complete —
`reports/cook-report.md`.

Review the ACTUAL DIFF — `git status`/`git diff` over the working
tree (Phase-7 changes live in `internal/tracing/`,
`internal/transaction/`, `queries/transactions.sql`,
`internal/db/transactions.sql.go`, `internal/config/config.go`,
`internal/httpserver/transactions.go`, `cmd/api/main.go`,
`api/openapi.yaml`, `internal/tracing/service_test.go`,
`tests/transactions_api_test.go`).

Verify against plan pins + invariants:

1. **Pinned semantics**: Reading-A frontier (clean intermediaries seed
   deeper — only emitted-prefix nodes' edges seed frontier_{d+1});
   dedup snapshot ≤ d−2 post-direction-filter at level 1; two-book
   cpAgg/frontier; materialized-vs-scheduled coverage; forward-DFS
   address-tuple order; per-term integer rounding; null-if-all-zero;
   foreign labels skip consider(); solely-foreign →
   cross_chain_listing; BFS never crosses networks.
2. **Bit-identity at max_hops≤2**: item ordering, coverage values,
   truncation reasons, sentinel lifecycle, direction interplay.
3. **The cook deviation**: loop-top `budget.Exhausted()` pull-gate
   removal — is never-attempted accounting truly identical
   (`out.BudgetExhausted` path)? Any new semantics introduced
   (e.g., can a position now resolve BEYOND budget and expand the
   frontier — does that over- or under-claim coverage)?
4. **Honesty invariants**: no unbounded loops, watermarks never
   overclaim, provider failure never clean, unknown never safe,
   path_verified=false, items [] not null.
5. **Race/concurrency**: shared maps across depth loops, CAS edge
   budget, worker pool lifecycle.
6. **OpenAPI vs wire**: fields, enums, nullable, example validity.
7. Run the tests yourself: `go test -count=1 ./internal/tracing/
   ./internal/transaction/ ./tests/`; `go vet ./...`; `gofmt -l .`.

Write `reports/review-round-1.md` — verdict APPROVED or
CHANGES_REQUESTED with severity-classified findings (blocker/major/
minor/nit) + evidence (file:line). Read-only; write ONLY inside
`plans/20261104-0400-advanced-tracing/reports/`. You may RUN tests
(read-only commands) but must not edit source.
