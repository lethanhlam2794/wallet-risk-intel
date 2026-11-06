You are Devin Cook, responsible for executing the implementation plan in:
`/home/lethanhlam/Desktop/Profile/check-wallet/plans/20261106-2345-warm-seed-worker/plan.md`

Phase files (same directory):
- `phase-01-seed-queries-config.md`
- `phase-02-seeder-core.md`
- `phase-03-worker-wiring.md`

Working directory: `/home/lethanhlam/Desktop/Profile/check-wallet`
Go module root (ALL go commands run here): `/home/lethanhlam/Desktop/Profile/check-wallet/apps/api`

Binding task prompt: `/home/lethanhlam/Desktop/Profile/check-wallet/docs/prompts/20261106-2322-claude-code-proactive-warm-seed.md`

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md
- Engineering standard: ~/.codex/rules/engineering-standard.md
- Learning ledger: /home/lethanhlam/Desktop/Profile/learning/learning.md
- Reports dir: /home/lethanhlam/Desktop/Profile/check-wallet/plans/20261106-2345-warm-seed-worker/reports
If AGENTS.md or learning.md is unreadable: STOP and return `Status: NEEDS_CONTEXT` — do not guess paths or proceed.

### Execution Rules & Engineering Standard
1. Strictly follow Test-Driven Development (TDD):
   - Red: Write `internal/warmindex/seed_test.go` FIRST and confirm it fails.
   - Green: Implement `internal/warmindex/seed.go` (+ phase-1 queries/config) until tests pass.
   - Refactor: Clean up without breaking existing behaviors.
   - Practical order: phase 1 artifacts (queries + sqlc generate + config knob) must exist before tests compile — do phase 1 first, then Red → Green for phase 2, then phase 3 wiring.
2. Zero Hallucination:
   - Tests run against the REAL dedicated test DB: `postgres://wallet:devonly@127.0.0.1:5433/check_wallet_warmindex_test?sslmode=disable` via `testutil.RequireTestPoolDSN` — the harness auto-creates, migrates, and truncates. Postgres is already running on port 5433.
   - Never assume code works without running `go build`, `go vet`, `gofmt -l .`, and `go test`.
3. Definition of Done per Phase:
   - Run phase-specific unit and integration tests.
   - Update the phase file frontmatter to `status: completed` and check off success criteria `[x]` when each phase verifies.
   - `go build ./...` must compile with zero errors.
4. Concurrency & Data Integrity:
   - The only dedup mechanism is the existing `EnqueueWarmItem` upsert — do NOT add run-tracking state.
   - Follow the plan's D2 error taxonomy exactly: canonical pre-check (`NormalizeEVM(addr) == addr`, only after `IsEVMAddress` passes) failure → `errors++`; post-check `Enqueue` failure → abort with error; ctx death → abort.
5. Hard constraints (from the binding prompt — violations are BLOCKERs):
   - NO new migrations, NO schema changes. NO edits to `drain.go`, `queries/warm_index.sql`, `tracing/warm.go`, or the 50/100 priorities.
   - NO `internal/provider/trongrid` import; TRON stays inactive.
   - NO new API endpoints, NO openapi changes, NO commits, NO `.env` changes, NO new dependencies.
   - `seed_test.go` MUST reuse the package helpers in `queue_test.go`/`drain_test.go` (testDSN, netByCode, warmRow*, insertWarmItem, addrW*, stubTx, testCfg, checkerFunc) — redeclaring them is a compile blocker.
   - sqlc: run `cd apps/api && sqlc generate` (v1.30.0 is installed); `git diff internal/db/` must be scoped to the new `warm_seed.sql.go` only.
   - File scope: NEW `internal/warmindex/seed.go`, `internal/warmindex/seed_test.go`, `queries/warm_seed.sql` + generated output; EDIT `cmd/worker/main.go` (register `-mode warmseed`), `internal/config/config.go` (`WarmSeedBatchSize` knob only).
   - Mask addresses in logs via `warmindex.MaskAddr`; per-source log line emits all six counter keys; seed priorities 20/10/5 strictly below `PriorityRiskyLabel` (50).
6. Reporting:
   - When all phases verify, report: files changed, counter names, the exact enqueue-path call graph, test counts, build/vet/fmt status.
   - Save your full final report to `plans/20261106-2345-warm-seed-worker/reports/cook-report.md` as well as returning it.

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
