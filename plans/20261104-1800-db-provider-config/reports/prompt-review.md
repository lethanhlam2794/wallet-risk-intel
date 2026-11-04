You are Devin Review — an INDEPENDENT code reviewer with zero author
bias. Execute an evidence-based code review (ck-code-review --pending
equivalent) on the committed work `e2af931..HEAD` in
`/home/lethanhlam/Desktop/Profile/check-wallet`.

Working directory: `/home/lethanhlam/Desktop/Profile/check-wallet`

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md
- Engineering standard: plans/20261104-1800-db-provider-config/reports/compliance/engineering-standard.md
- Learning ledger: plans/20261104-1800-db-provider-config/reports/compliance/learning.md
- Reports dir: plans/20261104-1800-db-provider-config/reports/
- Plan under review: plans/20261104-1800-db-provider-config/plan.md +
  phase-01..04 — **"Amendments (critic R1)" sections WIN over earlier
  text**; task brief `docs/prompts/20261104-2258-codex-db-provider-config.md`
  (pinned semantics = contract)
If AGENTS.md or learning.md is unreadable: STOP → `Status: NEEDS_CONTEXT`.

### READ-ONLY MANDATE
You are READ-ONLY. Inspect, run tests/builds, query the test DB — but
write ZERO files and make ZERO code edits. If you accidentally modify
the tree, revert it. Your evidence is `git diff`, file reads, and
command output. (`plans/.../reports/cook-raw.log` shows as modified —
log artifact, not review scope.)

### Scope
6 commits, 42 files, +3,583/−79 implementing DB-managed provider config:
`b633f12` schema (migration 000020 + sqlc) · `b32cba2` resolution layer
(descriptor map, resolver, AES-256-GCM keystore,
`provider.AllowedHostsFor`, boot wiring) · `11bc1a7` TTL-cached
kill-switch in the chain walk · `bc7f533` admin API (masked reads,
whitelisted writes, audit rows, OpenAPI) · `462c2b3`,`f0a61e2` docs.

### Audit dimensions (be brutal, cite file:line)
1. **Resolution semantics (pinned)**: DB non-null wins; env fallback
   ONLY on absent/NULL — `enabled_chains=[]` and `base_url=""` are
   explicit overrides, never fallbacks. Row absent ⇒ byte-identical env
   behavior. ANY resolution error (DB fail, decrypt fail, allowlist
   reject, malformed JSON) is boot-fatal naming provider+field — verify
   no error path silently falls back to env. Check the 8-case truth
   table is actually implemented and tested.
2. **Keystore (pinned)**: AES-256-GCM iff `PROVIDER_CONFIG_KEK` set,
   else documented plaintext. Wire shape `enc:v1:<b64 nonce>:<b64 ct>`
   — full-shape validation BEFORE decrypt; plaintext input bearing
   `enc:` + KEK set ⇒ boot-fatal; client `enc:` input ⇒ 400 at API.
   Tamper/wrong-KEK ⇒ error never plaintext. No key material in logs.
3. **Kill-switch (pinned)**: goroutine-safe ~30s TTL cache of
   `provider_configs.status`; effective-active =
   `data_sources.is_active AND (status='active' OR row absent)`;
   DB error ⇒ last-known (cold ⇒ active + log); consulted BEFORE
   upstream spend via the EXACT existing disabled-skip path in
   `internal/transaction/service.go` — verify zero upstream calls, no
   failover trigger, no failure write; all-inactive ⇒ disabled under
   chain[0]'s source_id. Check `-race` safety of the cache.
4. **Admin API (pinned)**: `GET/PUT /api/v1/admin/provider-configs[/{code}]`,
   `GET .../{code}/changes?cursor&limit`. api_key serialized
   `{"set":bool,"last4":string|null}` — last4 suppressed <8 chars, never
   plaintext anywhere (response/log/audit/test). Whitelist
   {api_key, base_url, enabled_chains, status}; base_url via
   `provider.AllowedHostsFor(code)` echoing allowed hosts in 400;
   enabled_chains vs registered network codes; unknown code ⇒ 404
   (never FK-500); column-scoped UPDATE or FOR UPDATE in ONE tx — check
   concurrent two-field PUT for lost update; every change ⇒ audit row
   same tx (masked key values; actor_id=client Name,
   actor_type="api_client", updated_by same); non-admin ⇒ 403;
   `requires_restart` disclosure + `warnings[]` when stored≠in-effect
   (boot snapshot compare); last-active-member warning on
   status=inactive.
5. **Schema (pinned)**: migration 000020 matches the pinned DDL
   (source_code PK FK→data_sources.code, enabled_chains JSONB
   CHECK array-or-null, status CHECK active|inactive DEFAULT 'active',
   changes.id UUID DEFAULT gen_random_uuid()); up+down honest; sqlc
   queries parameterized only.
6. **Domain invariants (AGENTS §5)**: missing/empty key ⇒
   `Enabled()=false` honest disabled; `data_sources` semantics,
   `TX_PROVIDER_*` selector format, failover error-kind whitelist,
   goplus/publicaml/chainabuse adapters, migrations <000020 — all
   untouched. Provider failure never becomes a clean result.
7. **Test honesty**: real DB fixtures (`_test` DBs only), real wire
   shapes; no weakened/skipped tests; the flaky-assertion fix cook
   reported (uuid-tiebreak on same-`changed_at` audit rows) — verify it
   tests the real contract, not a relaxed one.
8. **Cook-declared deviations — adjudicate**:
   - PUT base_url additionally requires `https` scheme
   - api_key audit rows store masked object for BOTH previous+new value
   - Same-timestamp audit ordering assertion relaxed to full-page assert
9. **Hygiene**: dead code, raw SQL outside sqlc, secrets/keys in any
   committed file, `.env` untouched, error mapping (400 not 500 on
   client input), gofmt/vet/build clean.
10. Run: `git diff e2af931..HEAD`, `git log`, `go build ./...`,
    `go vet ./...`, `go test -count=1 ./internal/providerconfig/
    ./internal/transaction/ ./tests/ ./internal/provider/` and any
    additional spot checks you judge necessary (e.g. re-run migration
    up+down on `check_wallet_test` via `make migrate-test`,
    TEST_DATABASE_URL in .env).

### Output format
- **Verdict**: APPROVED / CHANGES_REQUESTED / BLOCKED
- **Executive Summary**
- **Verification Evidence** (commands run + results)
- **Detailed Findings** by severity: [Blocker] / [Major] / [Minor] /
  [Nit] — each with file:line evidence
- **Required Actions to Approve**

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
