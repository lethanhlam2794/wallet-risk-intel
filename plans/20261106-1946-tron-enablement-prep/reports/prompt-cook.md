You are Devin Cook, responsible for executing the implementation plan in:

`/home/lethanhlam/Desktop/Profile/check-wallet/plans/20261106-1946-tron-enablement-prep/plan.md`

Working directory: `/home/lethanhlam/Desktop/Profile/check-wallet`
Go module root (all go commands run here): `/home/lethanhlam/Desktop/Profile/check-wallet/apps/api`

The task is **TRON enablement prep** (R3): pick a verified free TRON TxProvider, write the indexer semantics + provider decision docs, generalize the `'evm'` address_type in `wallet.Service.GetOrCreate`, and implement a dormant (unwired) `internal/provider/<chosen>/` adapter. The source prompt is at `docs/prompts/20261106-1740-codex-tron-enablement-prep.md` — its acceptance criteria and stop conditions are binding. The plan was adversarially hardened by an independent critic — read the critique in `plans/20261106-1946-tron-enablement-prep/reports/critique-round-1.md` before coding; the binding invariants (INV-1/INV-2) and the TRC-20 tri-problem framing are NOT optional.

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md
- Engineering standard: ~/.codex/rules/engineering-standard.md
- Learning ledger: /home/lethanhlam/Desktop/Profile/learning/learning.md
- Reports dir: /home/lethanhlam/Desktop/Profile/check-wallet/plans/20261106-1946-tron-enablement-prep/reports
If AGENTS.md or learning.md is unreadable: STOP and return `Status: NEEDS_CONTEXT` — do not guess paths or proceed.

### Execution Rules & Engineering Standard
1. Strictly follow Test-Driven Development (TDD): Red → Green → Refactor.
2. Zero Hallucination: run tests against real environments. Dev DB: `postgres://wallet:devonly@127.0.0.1:5433/check_wallet` (already running). Integration tests MUST use dedicated `*_test` databases ONLY — `internal/testutil` enforces the suffix and auto-migrates; use per-package DSNs like `check_wallet_wallet_test`.
3. Definition of Done per Phase: phase-specific tests pass; update the phase file frontmatter `status: completed` and check off success criteria `[x]`; `cd apps/api && go build ./...` compiles.
4. Checkpoint after each phase: output `Phase N done: <evidence>` in your reply.
5. Report at the end: comprehensive summary, test pass counts, build status, modified files.

### Phase order and hard constraints
Execute phases 1→6 in `plans/20261106-1946-tron-enablement-prep/`. The phase files contain the authoritative requirements — including the critic amendments (RA-1..RA-10 baked in). Key reminders:
- Phase 2: use a subagent or focused fetches for vendor docs; live probes keyless only; if NO candidate meets all 6 requirements on a free no-card tier → `Status: BLOCKED`, do not pick a paid fallback.
- Phase 3: document the tri-problem (b1/b2/b3) exactly as structured in the phase file; every decision needs cited evidence (file:line or live probe output).
- Phase 4: signature `GetOrCreate(ctx, net network.Network, address, normalized string)`; `""` format → `ErrUnknownAddressFormat`; update ALL call sites (4 production + 9 test + `WalletStore` interface + `httpserver/server.go:270`); 4 `'evm'` SQL literals get leading `--` comment blocks, no sqlc regen needed.
- Phase 5: dormant adapter — NEVER wire into `cmd/api`, `cmd/worker`, `providerconfig`, `config`, or `data_sources`. `var _ provider.TxProvider = (*Adapter)(nil)`. httptest only — unit tests must NEVER hit the real network.
- Stop conditions (from source prompt — pause and `Status: BLOCKED` before): flipping is_active or touching blockchain_networks; transactions/wallet_index_state DDL; wiring the adapter into any selector; tron.csv/wattrFiles; OpenAPI Address pattern; deleting files or changing EVM adapter behavior; paying for/embedding any API key.
- No new external Go dependency without asking. No secrets in code/commits. Do NOT create commits — leave changes uncommitted in the working tree.
- Keep everything conventional: gofmt (tabs), package names short/lowercase, comments in the codebase's existing voice.

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
