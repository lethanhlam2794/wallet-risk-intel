# Prompt: codex — db-provider-config

> Created: 2026-11-04 22:58 | Target: codex | Template: M (agentic task brief)

## Prompt

````
### Objective
Implement a DB-managed, admin-editable provider-config surface for the
tx-history providers — `provider_configs` table + audit trail + boot
resolver (DB-wins, env fallback) + runtime kill-switch + admin REST API —
patterned on greatify-api's providers/provider_changes/partners.authInfo
design, in this Go/Gin/pgx/sqlc codebase.

### Context (carry forward — verified anchors, do not re-derive)
- `data_sources(id, code UNIQUE, name, source_type, is_active)` already
  exists; boot already fails if a registered provider lacks a row or has
  budget<=0 (`cmd/api/main.go`). Next free migration: 000020.
- 12 tx adapters take `{apiKey, baseURL, enabledChains}` ONCE at
  construction; each validates baseURL against a package-private
  `defaultAllowedHosts`. `chainscan`'s key env is `ETHERSCAN_API_KEY`
  (not CHAINSCAN_API_KEY).
- Failover chain walk in `internal/transaction/service.go` already has a
  disabled-skip path before any upstream spend; reuse it exactly —
  inactive must look identical to disabled, never a failure/failover.
- Admin identity: `middleware.ClientFrom(c)` → `ClientIdentity`.
- OpenAPI uses cursor pagination (`cursor`/`next_cursor`).

### Files & Scope
- Primary (create): `migrations/000020_provider_configs.{up,down}.sql`,
  `queries/provider_configs.sql`, `internal/providerconfig/` (resolver,
  descriptor map, keystore, status source, admin service),
  `internal/httpserver/provider_configs.go`; wire `cmd/api/main.go`;
  update `api/openapi.yaml`.
- Primary (modify): `cmd/api/main.go`, `internal/transaction/service.go`
  (kill-switch consult), each `internal/provider/<name>/adapter.go`
  (export allowlist via `provider.AllowedHostsFor(code)`).
- Reference (read-only): approved plan + pinned semantics at
  `plans/20261104-1800-db-provider-config/plan.md` and phase-01..04 —
  the "Amendments (critic R1)" sections WIN over earlier text;
  greatify pattern at
  `/home/lethanhlam/Desktop/GSW-2/gsw-nx-monorepo/apps/greatify-api/src/modules/{provider,partner}/`.
- Do NOT touch: `data_sources` semantics, `TX_PROVIDER_*` env selector
  chain format, goplus/publicaml/chainabuse adapters, failover error-kind
  whitelist, existing migrations < 000020.

### Constraints
- TDD, per-phase commits, conventional commits, no AI references.
- Migrations reversible; verify up+down on the dedicated `_test` DB only.
- Parameterized SQL (sqlc/pgx) only. All external calls bounded.
- Only make changes directly requested. No extra files, abstractions,
  or features beyond the spec below.

**Pinned semantics — implement verbatim:**
1. Resolution: DB non-null wins; env fallback ONLY on absent (NULL).
   `enabled_chains=[]` / `base_url=""` are explicit overrides, not
   fallbacks. Row absent ⇒ byte-identical env behavior (parity test).
   ANY resolution error (DB fail, decrypt fail, allowlist reject,
   malformed JSON) is boot-fatal naming provider+field — never fallback.
2. Keystore: AES-256-GCM iff `PROVIDER_CONFIG_KEK` set, else plaintext
   (documented). Wire `enc:v1:<b64 nonce>:<b64 ct>` — validate full shape
   before decrypt; plaintext input bearing `enc:` + KEK set ⇒ boot-fatal;
   reject client `enc:` input at the API (400). KEK rotation out of scope.
3. Kill-switch: goroutine-safe ~30s TTL cache of `provider_configs.status`;
   effective-active = `data_sources.is_active AND (status='active' OR row
   absent)`; DB error ⇒ last-known (cold ⇒ active + log); consult BEFORE
   upstream spend via the existing disabled-skip; all-inactive ⇒ disabled
   under chain[0]'s source_id.
4. Boot-effective vs runtime: `status` is the only runtime field; api_key/
   base_url/enabled_chains need restart — responses MUST disclose
   `requires_restart` and PUT MUST add `warnings[]` when stored ≠
   in-effect (compare boot-time resolved snapshot); disabling the last
   active member of a chain adds a warning.
5. Admin API: `GET/PUT /api/v1/admin/provider-configs[/{code}]`,
   `GET .../{code}/changes?cursor&limit`. api_key serialized
   `{"set":bool,"last4":string|null}` (last4 suppressed <8 chars);
   whitelist {api_key, base_url, enabled_chains, status}; base_url via
   `provider.AllowedHostsFor(code)`; enabled_chains against registered
   network codes; unknown code ⇒ 404 (never FK-500); column-scoped
   UPDATE or `FOR UPDATE` in one tx; every change ⇒ audit row same tx
   (key values masked; actor_id = client Name, actor_type="api_client",
   updated_by same). Non-admin ⇒ 403. `data_sources.is_active` stays the
   catalog switch; `provider_configs.status` the operator switch.
6. Schema: `provider_configs(source_code VARCHAR(100) PK FK→
   data_sources.code, api_key TEXT, base_url TEXT, enabled_chains JSONB
   CHECK(NULL OR jsonb_typeof='array'), status VARCHAR(16) NOT NULL
   DEFAULT 'active' CHECK('active'|'inactive'), updated_by VARCHAR(64),
   updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())` +
   `provider_config_changes(id UUID PK DEFAULT gen_random_uuid(),
   source_code VARCHAR(100) NOT NULL FK, actor_id VARCHAR(64),
   actor_type VARCHAR(32), field VARCHAR(64) NOT NULL,
   previous_value JSONB, new_value JSONB,
   changed_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`.

### Acceptance Criteria
- [ ] 8-case resolution truth table tested (row-absent/db-null/db-empty/
  db-set × env-set/env-unset) incl. `[]`/`""` overrides + env parity when
  row absent.
- [ ] Boot-fatal on each resolution error class, naming provider+field.
- [ ] `enc:` marker tests: encrypt-on-write iff KEK; full-shape check;
  `enc:` plaintext + KEK ⇒ fatal; client `enc:` input ⇒ 400.
- [ ] Kill-switch tests: flip ⇒ skipped zero upstream calls; flip back ⇒
  serves; all-inactive ⇒ honest `disabled`; DB-error last-known.
- [ ] Admin tests: masking, whitelist rejection, invalid status/chain/
  host (error echoes allowed hosts), 403 non-admin, audit rows masked,
  concurrent two-field PUT no lost update, unknown code 404, cursor
  pagination, `requires_restart`+`warnings[]` disclosure, last-active
  warning, no plaintext key in response/log/audit/test.
- [ ] Missing/empty key ⇒ `Enabled()=false` honest disabled — invariant.
- [ ] `gofmt -l .` empty; `go build ./...`, `go vet ./...`,
  `go test -count=1 ./...` all green; migration up+down verified.

### Stop Conditions
- Pause and ask before: touching `data_sources`/`TX_PROVIDER_*`
  semantics, adding dependencies, changing migrations < 000020, KEK
  rotation scope, or any deviation from pinned semantics.
- After each phase: report commit hash + tests added + deviations.
- Missing AGENTS.md / learning.md / unreadable plan ⇒ stop:
  `Status: NEEDS_CONTEXT`.

### Session Strategy
- Read plan.md + phase files + AGENTS.md + service.go chain walk +
  one adapter New() before writing anything — the semantics are dense
  and literal.
- Work phase-by-phase 01→04; keep each phase's Red→Green commits small.
- End with: `Status: DONE | DONE_WITH_CONCERNS | BLOCKED |
  NEEDS_CONTEXT` + summary + concerns.
````

## Strategy

Template M for a literal-reading agentic model: verified anchors front-loaded,
dense pinned-semantics block replaces prose spec, binary acceptance checklist
encodes the scenario-gate findings (secrets, migration, concurrency, failover
compatibility), hard scope locks prevent drift.

## Notes

Paste into Codex at repo root. Dev DB: postgres://wallet:devonly@127.0.0.1:5433/check_wallet (tests use the `_test` DB via `make migrate-test`).
This prompt is for an agentic tool with real system access — review the
scope locks, forbidden actions, and stop conditions before pasting.
