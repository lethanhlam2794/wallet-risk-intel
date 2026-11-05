You are Devin Review. You must execute an evidence-based code review (`ck-code-review --pending`) on the pending changes in `/home/lethanhlam/Desktop/Profile/check-wallet`.

Working directory: `/home/lethanhlam/Desktop/Profile/check-wallet`
Monorepo note: the Go module is `apps/api` — all go commands run there.

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md
- Engineering standard: ~/.codex/rules/engineering-standard.md
- Learning ledger: /home/lethanhlam/Desktop/Profile/learning/learning.md
- Reports dir: /home/lethanhlam/Desktop/Profile/check-wallet/plans/20261105-0940-admin-api-clients/reports
If AGENTS.md or learning.md is unreadable: STOP and return `Status: NEEDS_CONTEXT` — do not guess paths or proceed.

### Review Scope & Principles
1. Examine `git status` and `git diff` for all pending changes. Reference plan: `plans/20261105-0940-admin-api-clients/plan.md` (+ phase files — the negotiated contract; verify the implementation matches pinned decisions).
2. This is an R4 change — auth boundary (credential issuance + admin lockout guards). Review thoroughly:
   - **Key hygiene**: `api_key`/`key_hash` must never appear in views, list bodies, logs, errors, or test output. POST 201 must carry `Cache-Control: no-store`. `cw_` key entropy = 32 crypto/rand bytes.
   - **Lockout guards**: advisory xact lock `hashtext('apiclient.admin_guard')` wraps every role/is_active mutation; post-state count check `role='admin' AND is_active AND id != target`; self-demote/self-deactivate → 409; self no-op and self-rename pass; already-inactive transitions must NOT false-409.
   - **env_managed**: PATCH on bootstrap-hash row returns warning (any mutation incl. `{}`); upsert untouched.
   - **Error mapping**: validation/JSON/id/cursor/limit → 400 BAD_REQUEST (not 500); unknown id → 404; guards → 409 ADMIN_LOCKOUT.
   - **sqlc/queries**: parameterized only; keyset cursor matches provider_configs.sql convention; column-scoped updates preserve last_seen_at.
   - **Wiring**: admin gate `d.Admin != nil || d.Clients != nil`; bootstrapHashes from cfg; request_id passed into Update.
   - Dead code, DRY/YAGNI/KISS, test honesty (no weakened/skipped assertions).
3. Test Saturation & Honesty:
   - Tests cover positive + negative + boundary + concurrency (A↔B demote under -race).
   - Verify no tests were weakened, commented out, or bypassed; run the suites yourself if needed (`cd apps/api && go test -count=1 ./internal/apiclient/... ./tests/`).
4. Output Format:
   - **Verdict**: APPROVED, CHANGES_REQUESTED, or BLOCKED.
   - **Executive Summary**.
   - **Verification Evidence** (build status, test counts).
   - **Detailed Findings** classified by severity: [Blocker]/[Major]/[Minor]/[Nit].
   - **Required Actions to Approve**.

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
