# Devin Cook — Phase 7 Advanced Tracing implementation

Implement the CONSENSUS plan at
`plans/20261104-0400-advanced-tracing/` — `plan.md` v3 +
`phase-01-frontier-and-scoring.md` + `phase-02-crosschain-and-api.md`
are the source of truth (critic debate closed with
PLAN_CONSENSUS_REACHED; do NOT re-design pinned semantics).

Follow AGENTS.md strictly. TDD: write the §6 tests (phase files own
the list) against the pinned behaviors, then implement.

## Working rules

- Dedicated test DBs only (`check_wallet_tracing_test`,
  `check_wallet_transaction_test`, `check_wallet_txapi_test`).
- `sqlc generate` after any `queries/` change; never hand-edit
  `internal/db/*.sql.go`.
- `gofmt -l .` must be empty; `go build ./...`, `go vet ./...`,
  `go test ./...` all green before you report done.
- Providers stay network-scoped; BFS NEVER crosses networks;
  cross-chain = foreign-label signals only (`kind:"label"` +
  `network` + `listed_address`).
- `exposure_score` never feeds `risk_score`; `POST /screenings`
  `options.max_hops` stays 0–2.
- Keep Phase-6 behavior bit-identical at max_hops≤2 (incl. direction
  interplay, sentinel lifecycle, coverage values) — test 21 pins it.

## Deliverables

1. All pinned mechanics implemented per §2.1–§4 (frontier
   generalization, dedup snapshot, cpAgg/frontier two-book split,
   forward-DFS path enumeration, coverage vocabulary, exposure-v1
   scoring, cross-chain labels, wiring: `MaxPagesFor(frontierIdx)`,
   `tracing.New(...,resolver,cfg)`, `TRACING_MAX_NODES_{2..5}`,
   `TXINDEX_MAX_PAGES_HOP3`, handler validation 1–5, OpenAPI update).
2. Tests 1–35 (phase-file ownership) + updated Phase-6 domain tests.
3. `reports/cook-report.md` in this plan dir: what was built, test
   matrix results, deviations (each with justification).
