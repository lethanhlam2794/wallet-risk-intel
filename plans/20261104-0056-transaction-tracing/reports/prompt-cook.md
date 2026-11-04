# Devin Cook — Phase 6: Transaction Tracing implementation

You are Devin Cook implementing an APPROVED plan in
`plans/20261104-0056-transaction-tracing/` (9 Critic rounds →
PLAN_CONSENSUS_REACHED). **plan.md + the 4 phase-*.md files are the
source of truth** — read all of them FIRST; every detail matters
(wire contracts, window arithmetic, identity derivation, coverage
fields, bound precedence, enums). Do NOT re-decide plan choices; if
a detail looks wrong, implement per plan and note it in
`reports/cook-report.md`.

## Definition of Done (all must pass; run before finishing)
- `gofmt -l .` empty; `go build ./...`; `go vet ./...`
- `go test ./...` (integration tests use DEDICATED dbs — see below)
- sqlc regen committed: `sqlc generate` clean after editing queries/
- OpenAPI parses (validate with the same tool CI uses if present —
  check Makefile/scripts; else `python3 -c "import yaml;
  yaml.safe_load(open('api/openapi.yaml'))"`)

## Test infrastructure (verified healthy)
- Postgres: `postgres://check_wallet:check_wallet@127.0.0.1:5433` —
  dedicated test databases only, e.g.
  `check_wallet_transaction_test`, `check_wallet_tracing_test`
  (NEVER `check_wallet` dev db). Pattern after `tests/` +
  `internal/testutil` existing helpers — reuse them.
- Redis at 127.0.0.1:6380 for anything cache-related.
- Unit tests beside source; API/integration in `tests/`. Provider
  upstreams MUST be stubbed/injected — no real network calls in tests.

## Work order (phase files = authoritative task lists)
1. phase-01: migration `000008_transactions.up.sql` (+down),
   `queries/transactions.sql` + `sqlc generate`, `TxProvider` seam in
   `internal/provider/provider.go` (new interface — do not break the
   existing `Provider` interface), `internal/provider/chainscan/` and
   `internal/provider/nodereal/` adapters wrapping
   `internal/provider/resilience`, config keys (enabled_chains,
   keys, page_size, max_pages, reorg_tail, IndexingFreshTTL,
   IndexingStaleAfter, per-adapter pool budgets).
2. phase-02: `internal/transaction/` EnsureIndexed (singleflight,
   freshness/cooldown/disabled gates, span-capped windows incl. tail
   overlap, content-hash `.{k}` max-per-stream counting, batch
   upsert → min-contiguous watermark → persisted coverage columns),
   ListTransactions (UNION from/to, direction="self", keyset DESC).
3. phase-03: `internal/tracing/` bounded BFS (ordered-position pull,
   contiguous-prefix assembly, per-kind matched_signals, cross-depth
   counterparty dedup, sentinel rules, total-order items, envelope
   coverage + truncated reasons).
4. phase-04: handlers + routes in internal/httpserver (Deps-injected
   TxProvider for E2E stubbing), cmd/api/main.go wiring (missing keys
   ⇒ DISABLED never crash), OpenAPI updates incl. screening enum fix
   at :1316 + stale desc at :14 + regenerate the 5 screening-*.json
   captures, metrics (tracing_nodes_examined_total + reuse
   provider_* + new provider_request_duration_seconds), tests 1–22
   (+18b/18c/21b) from plan §5 — ALL must exist and pass.

## Hard rules
- TDD: write the required tests as you build each phase (they are the
  spec's acceptance criteria).
- EVM normalization via `wallet.NormalizeEVM`; lowercase tx_hash;
  NUMERIC amounts only (never float).
- No external network calls at test time; adapters hit upstream only
  via injected HTTP client / resilience client.
- Keep provider quirks inside the adapters; services stay clean.
- Do not touch Phase 7 scope (3+ hops, cross-chain tracing) — max_hops
  >2 must 400.
- If NodeReal adapter details in the plan cannot be verified against
  the live docs/response shape during implementation, implement the
  DISABLED path faithfully and document the deviation — NEVER fake it.
- Write `reports/cook-report.md`: what was built per phase, commands
  run + results, deviations/notes, files changed.
