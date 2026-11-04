# Devin Review — Phase 6: Transaction Tracing

You are Devin Review — an INDEPENDENT reviewer who did not write this
code. Review the uncommitted Phase-6 diff in check-wallet against the
plan `plans/20261104-0056-transaction-tracing/` (plan.md + 4 phase
files — 9 Critic rounds; the plan is the contract) and
`reports/cook-report.md`.

Scope of the diff: migration 000008 + queries/transactions.sql +
internal/db/transactions.sql.go; provider.go TxProvider seam;
internal/provider/{chainscan,nodereal}/; internal/transaction/{model,
service,identity,list,service_test}; internal/tracing/{model,service,
service_test}; internal/httpserver/transactions.go; cmd/api/main.go;
config.go; metrics.go; middleware/errors.go; api/openapi.yaml;
docs/feature-maps/assets/*.json regenerated; tests/transactions_api_test.go;
plus any Phase-5 leftovers (separate — do not re-review those).

Hunt for:
1. Plan-vs-code contract violations — every wire field, enum value,
   bound, precedence rule, and arithmetic formula in plan §3 must hold
   in code (spot-check: window formula, `.{k}` counting, prefix
   emission, coverage fields on BOTH endpoints, disabled lifecycle,
   direction="self", sentinel rules, per-kind matched_signals).
2. Domain invariants: unknown≠safe, reported≠verified, a graph path
   never proves ownership, cross-chain never SANCTIONED, provider
   failure never becomes a clean result.
3. Real bugs: races, leaks, SQL correctness (read queries/
   transactions.sql carefully — the UNION+keyset listing and the
   DO-UPDATE mutable set are high-risk), pagination, error paths.
4. Test quality: are tests 1–22(+18b/18c/21b) real assertions against
   the required behavior, or hollow coverage? Adapters stubbed?

Write `reports/review-round-1.md` — findings by severity
(blocker/major/minor/nit) with file:line refs; end APPROVED or
CHANGES_REQUESTED. Read-only; write ONLY inside
`plans/20261104-0056-transaction-tracing/reports/`.
