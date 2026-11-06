You are Devin Review. You must execute an evidence-based code review (`ck-code-review --pending`) on the pending changes in `/home/lethanhlam/Desktop/Profile/check-wallet`.

Working directory: `/home/lethanhlam/Desktop/Profile/check-wallet`
Go module root (all go commands): `/home/lethanhlam/Desktop/Profile/check-wallet/apps/api`

The change under review is **TRON enablement prep** (plan: `plans/20261106-1946-tron-enablement-prep/` — read `plan.md` and `reports/final-report.md` first): TronGrid provider decision doc, TRON indexer semantics doc, `wallet.Service.GetOrCreate` address_type generalization (new signature `(ctx, net network.Network, address, normalized)` + `ErrUnknownAddressFormat`), dormant `internal/provider/trongrid/` TxProvider adapter, `'evm'` SQL literal comments, audit/discovery doc updates. TRON stays `is_active=FALSE`; the adapter is intentionally unwired (§15 gate).

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md
- Engineering standard: ~/.codex/rules/engineering-standard.md
- Learning ledger: /home/lethanhlam/Desktop/Profile/learning/learning.md
- Reports dir: /home/lethanhlam/Desktop/Profile/check-wallet/plans/20261106-1946-tron-enablement-prep/reports
If AGENTS.md or learning.md is unreadable: STOP and return `Status: NEEDS_CONTEXT` — do not guess paths or proceed.

### Review Scope & Principles
1. Examine `git status` and `git diff` (tracked changes) plus the untracked new files (`internal/provider/trongrid/`, `internal/wallet/service_test.go`, `docs/tron-*.md`).
2. Review thoroughly against engineering standards — technical correctness over social comfort; be honest, brutal, concise:
   - Domain invariants (AGENTS.md §5): no silent coverage lies — check INV-1 (cursor never advances past unresolved rows) and INV-2 (every emitted `Transfer.Stream` ∈ `page.Streams`) are real invariants, not comments; check no block-less TRC-20 row can be emitted; check `Enabled()=false` truly precedes all HTTP.
   - The adapter is a §15-gated dormant artifact — its correctness claims must be test-backed, not aspirational. Audit the 23 tests for honesty (no weakened assertions, httptest-only, zero real-network calls).
   - `address_type` generalization: all callers updated? `ErrUnknownAddressFormat` fail-closed correct? EVM stamped values byte-identical? Any caller passing a mismatched network?
   - Docs accuracy: `docs/tron-indexer-semantics.md` and `docs/tron-provider-decision.md` claims vs actual code/probe evidence; audit doc must not claim more than code does.
   - `'evm'` SQL literal comments: leading `--` blocks only, no semantic change; sqlc generated code untouched.
   - Dead code, raw SQL, secrets, new dependencies (must be stdlib-only in trongrid + wallet changes).
   - Correct error kinds per `provider.go` ErrKind vocabulary; `UpstreamCalls` accounting must count bridge + fan-out calls exactly.
   - Migration safety: confirm zero migration files touched; `blockchain_networks` untouched.
3. Test Saturation & Honesty: positive/negative/boundary coverage; no skipped/weakened tests; replay determinism and ascending-order assertions are real.
4. Output Format:
   - **Verdict**: APPROVED, CHANGES_REQUESTED, or BLOCKED.
   - **Executive Summary**.
   - **Verification Evidence** (build status, test counts — run the gates yourself).
   - **Detailed Findings** by severity: [Blocker] / [Major] / [Minor] / [Nit].
   - **Required Actions to Approve**.

Write your report to `plans/20261106-1946-tron-enablement-prep/reports/review-round-1.md` AND return it in your reply.

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
