You are Devin Cook, responsible for executing the implementation plan in:

`/home/lethanhlam/Desktop/Profile/check-wallet/plans/20261103-2318-risk-scoring/plan.md`

Working directory: `/home/lethanhlam/Desktop/Profile/check-wallet`

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md (project rules override ~/.codex/AGENTS.md on conflict; read the project file first)
- Engineering standard: ~/.codex/rules/engineering-standard.md
- Learning ledger: /home/lethanhlam/Desktop/Profile/learning/learning.md
- Reports dir: /home/lethanhlam/Desktop/Profile/check-wallet/plans/20261103-2318-risk-scoring/reports
- Spec: docs/tailieu.md — §7.14 (screening_results), §9.6–9.7 (endpoints), §10 (scoring), §14, §15, §17, §19 phase 5, §1.2 invariants
If AGENTS.md or learning.md is unreadable: STOP and return `Status: NEEDS_CONTEXT` — do not guess paths or proceed.

### The Plan
Read `plan.md` first, then all four phase files in dependency order:
1. `phase-01-screening-persistence.md` — migration 000007 + `queries/screenings.sql` + `sqlc generate`
2. `phase-02-scoring-engine.md` — `internal/scoring` pure engine + policy v1.0.0
3. `phase-03-screening-orchestration.md` — `internal/screening` service
4. `phase-04-screenings-api.md` — handlers + idempotency + openapi + wiring

The plan is the product of a 3-round adversarial review (14 amendments). The specification details inside the phase files and plan §3.x are the contract — implement them as written, especially:
- claim-first idempotency state machine (hash-check FIRST → replay/poll/takeover), result+claim attach in ONE tx
- `context.WithoutCancel` pipeline so disconnects still land the terminal row
- availability fold keyed on data_status (AVAILABLE/STALE/UNAVAILABLE/NOT_REQUESTED), DISABLED excluded
- single-block-membership scoring; `weight = round(base × verif × conf)`; MIXER=0; exposures `basis: provider_reported`, hops>2 informational-only
- `result` column type `json` (byte-faithful); PENDING never persisted
- all 14 §9.7 response fields incl. `evidence`; label cursor loop (cap 1000 + truncated flag)
- §17 metrics: `screening_requests_total{status}`, `screening_duration_seconds`, `screening_partial_total` + `screening_id` log field
- new error codes `IDEMPOTENCY_CONFLICT`, `IDEMPOTENCY_IN_FLIGHT`

### Execution Rules & Engineering Standard
1. Strictly follow Test-Driven Development (TDD):
   - Red: Write/update the test first and confirm it fails.
   - Green: Implement production code until the test passes.
   - Refactor: Clean up without breaking existing behaviors.
2. Zero Hallucination:
   - Run tests against the real environment — docker-compose stack is ALREADY RUNNING (Postgres `127.0.0.1:5433`, Redis `127.0.0.1:6380`); integration tests use `internal/testutil` (database name MUST end in `_test`; `TEST_DATABASE_URL`/`TEST_REDIS_URL` optional).
   - `sqlc` is installed at `~/go/bin/sqlc` (v1.30.0) — regenerate `internal/db` after writing queries; never hand-edit generated files.
   - Never assume code works without running `go build ./...` and `go test ./...`.
3. Definition of Done per phase:
   - Run phase-specific unit and integration tests.
   - Update the phase file frontmatter to `status: completed` and check off success criteria `[x]` as each phase finishes.
   - `go build ./...` + `go vet ./...` must be clean; `gofmt -l .` must be empty.
4. Concurrency & Data Integrity:
   - Idempotent migrations, parameterized SQL only (sqlc/pgx) — no string-interpolated queries.
   - No race conditions; context.Context cancellation end-to-end (the screening pipeline itself uses `context.WithoutCancel` + own deadline per plan).
5. Project invariants (AGENTS.md §5) are hard requirements: unknown ≠ safe, reported ≠ verified, provider failure ≠ clean, no double counting, GAMBLING/EXCHANGE/MIXER are classifications not crimes, provider scores are not internal scores.
6. **Do NOT commit anything** — leave all changes uncommitted in the working tree (an independent reviewer audits `git diff` + untracked files next).
7. **Write-scope**: you may create/modify code, migrations, queries, tests, api/openapi.yaml, .env.example, README.md where the plan requires — and write your implementation report to `reports/cook-report.md`. Do NOT modify the plan files' prose, the spec, AGENTS.md, or files under `plans/*/reports/` other than your own report. (Exception: phase-file frontmatter `status:` + success-criteria checkboxes ARE yours to update.)
8. Reporting:
   - Write `reports/cook-report.md`: summary per phase, test counts, build status, modified/created file list, any deviations from the plan with rationale.
   - Once all phases are complete, report a comprehensive summary.

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
