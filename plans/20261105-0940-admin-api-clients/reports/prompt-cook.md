You are Devin Cook, responsible for executing the implementation plan in:
`/home/lethanhlam/Desktop/Profile/check-wallet/plans/20261105-0940-admin-api-clients/plan.md`

Read `plan.md` + `phase-01-domain-persistence.md` + `phase-02-http-contract.md` — they carry the full negotiated spec (adversarial review already happened; follow the design decisions table exactly, do not re-litigate it).

Working directory: `/home/lethanhlam/Desktop/Profile/check-wallet`
IMPORTANT — monorepo: the Go module `checkwallet` lives under `apps/api/`. ALL go commands (`go build`, `go test`, `go run`, `sqlc generate`) run with cwd `apps/api`. sqlc binary: `~/go/bin/sqlc` (or `make sqlc` inside apps/api).

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md
- Engineering standard: ~/.codex/rules/engineering-standard.md
- Learning ledger: /home/lethanhlam/Desktop/Profile/learning/learning.md
- Reports dir: /home/lethanhlam/Desktop/Profile/check-wallet/plans/20261105-0940-admin-api-clients/reports
If AGENTS.md or learning.md is unreadable: STOP and return `Status: NEEDS_CONTEXT` — do not guess paths or proceed.

### Environment Setup (before tests)
1. `cd apps/api && docker compose up -d` then wait for postgres+redis healthy (`docker compose ps`).
2. Integration tests run against real DB/Redis — the `internal/testutil` harness self-migrates and truncates; `apiclient` tests MUST use `RequireTestPoolDSN` with `check_wallet_apiclient_test` (package-private convention).

### Execution Rules & Engineering Standard
1. Strictly follow Test-Driven Development (TDD), phase order 1 → 2:
   - Red: write/update the test first and confirm it fails.
   - Green: implement production code until the test passes.
   - Refactor: clean up without breaking existing behaviors.
2. Zero Hallucination:
   - Run tests against the real environments (postgres :5433, redis :6380).
   - Never assume code works without running compiler checks and tests.
   - sqlc regeneration: extend `apps/api/queries/api_clients.sql`, then `make sqlc` — verify `apps/api/internal/db/api_clients.sql.go` regenerated and compiles.
3. Definition of Done per phase:
   - Phase-specific tests pass + `go build ./...` clean + `gofmt -l .` empty.
   - Update phase file frontmatter `status: completed` and check off success criteria `[x]`.
4. Contract precision (from the plan — all are pinned, implement exactly):
   - `cw_` + base64url(32 crypto/rand bytes) keys; `middleware.HashAPIKey` is THE digest impl.
   - View = `{id,name,role,is_active,created_at,last_seen_at}` — never `key_hash`/`api_key`.
   - `pg_advisory_xact_lock(hashtext('apiclient.admin_guard'))` + post-state admin-count guard; self-demote/self-deactivate → 409 `ADMIN_LOCKOUT` (self no-op `{role:"admin"}` and self-rename allowed).
   - `env_managed` warning on ANY patch of a bootstrap-hash row.
   - Structured `slog.Info` per mutation; `request_id` passed from handler (`middleware.GetRequestID(c)`).
   - POST 201 carries `Cache-Control: no-store`; `DisallowUnknownFields` on POST+PATCH; `PATCH {}` no-op; `limit`≤0/non-int→400, >200 clamps, default 50; invalid cursor→400; `items:[]` on empty.
   - name validation: trim + `utf8.RuneCountInString` 1–150; role ∈ {reader,admin}.
   - Admin group gate: `d.Admin != nil || d.Clients != nil`.
5. No Test Manipulation: never weaken, disable, or delete existing tests.
6. Reporting: comprehensive summary — phases done, test counts, build status, modified/created files.

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
