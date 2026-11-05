You are Devin Cook, responsible for executing the implementation plan in:
`/home/lethanhlam/Desktop/Profile/check-wallet/plans/20261105-2205-warm-index-queue/plan.md`

Working directory: `/home/lethanhlam/Desktop/Profile/check-wallet`
Go module root: `apps/api/` — ALL go commands run there.

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md
- Engineering standard: ~/.codex/rules/engineering-standard.md
- Learning ledger: /home/lethanhlam/Desktop/Profile/learning/learning.md
- Reports dir: /home/lethanhlam/Desktop/Profile/check-wallet/plans/20261105-2205-warm-index-queue/reports/
- Source task contract: docs/prompts/20261105-2155-codex-warm-index-queue.md (its design contract + MUST NOTs are binding)
If AGENTS.md or learning.md is unreadable: STOP and return `Status: NEEDS_CONTEXT` — do not guess paths or proceed.

### Scope
Implement ALL FOUR phases in order — read each phase file fully:
- `phase-01-schema-queries.md` — migration 000023 + queries/warm_index.sql + `make sqlc`
- `phase-02-warmindex-package.md` — `internal/warmindex` (Queue/Enqueue + Drainer + decision table + constants)
- `phase-03-tracing-hook.md` — `internal/tracing/warm.go` + hook in service.go
- `phase-04-worker-drain.md` — `cmd/worker -mode warmdrain` + config envs + **cmd/api/main.go SetEnqueuer wiring** + `.env.example`

### Execution Rules & Engineering Standard
1. Strict TDD: Red (failing test) → Green → Refactor, per phase file steps.
2. Real environment only: integration tests use `testutil.RequireTestPoolDSN(t, "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_warmindex_test?sslmode=disable")`. Postgres is docker-compose on :5433 — if down, `cd apps/api && make deps-up` first.
3. Definition of Done per phase: phase tests green; `cd apps/api && go build ./...` exit 0; update the phase file frontmatter `status: completed` and check `[x]` its success criteria.
4. Do NOT git-commit anything — the review gate audits `git diff`.
5. Do NOT modify `transaction.Service`, `EnsureIndexed`, singleflight, budget, or killswitch internals — the warm lane consumes `EnsureIndexed` only via its public seam with per-provider single-member adapter lanes (plan D1). Interactive path byte-identical.
6. Do NOT write raw interpolated SQL; all queries via sqlc-generated `*db.Queries` (the claim UPDATE is a named sqlc query).
7. Respect every AGENTS.md domain invariant — esp.: provider failure never becomes success; GAMBLING/EXCHANGE never raise risk/enqueue; masked-addr logging only; no secrets in code/logs; `normalized_address` = lowercase hex only.
8. Constants per plan: `LeaseDuration=15m`, `DrainTimeout=5m` (<lease), `MaxAttempts=3`, `PrioritySanctionsMatch=100`, `PriorityRiskyLabel=50`, risky set = {SCAM,PHISHING,HACK,RANSOMWARE,MIXER} only.
9. If a plan detail conflicts with real code or the contract — STOP and return `Status: NEEDS_CONTEXT` with the conflict; do not improvise.
10. Write your implementation report to `plans/20261105-2205-warm-index-queue/reports/implementation-report.md` (files changed, tests added, commands + exit codes, deviations).

### Required acceptance tests (from the contract — all must exist and pass)
- Concurrent drain passes never claim the same item (SKIP LOCKED).
- Duplicate (net,addr) enqueue → one row, priority monotone.
- Ledger at cap → drain skips, zero provider calls.
- Killswitch-inactive provider → not attempted, stays pending.
- Crash mid-claim → pending after lease expiry (attempts+1 per D10).
- EnsureIndexed failure → attempts+1, masked log, ≥3 → dead.
- Fresh `partial` wallet_index_state row → drain makes real upstream calls (the Refresh/deepening regression).
- GAMBLING/EXCHANGE neighbor → no enqueue; sentinel → no enqueue; enqueue error → Exposures still succeeds.

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
