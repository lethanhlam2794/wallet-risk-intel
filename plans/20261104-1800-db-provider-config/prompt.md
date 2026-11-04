# Prompt — DB-managed Provider Config (port of greatify partner/provider pattern)

Implement a DB-managed, admin-editable provider-config surface for the
tx-history providers in `/home/lethanhlam/Desktop/Profile/check-wallet`
(Go/Gin/pgx/sqlc), patterned on greatify-api's
`providers`/`provider_changes`/`partners.authInfo` design. All semantics
below are pinned — they resolve every finding from 2 adversarial review
rounds; implement them verbatim.

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md
- Engineering standard: plans/20261104-1800-db-provider-config/reports/compliance/engineering-standard.md
- Learning ledger: plans/20261104-1800-db-provider-config/reports/compliance/learning.md
- Reference impl (READ-ONLY, do not modify):
  /home/lethanhlam/Desktop/GSW-2/gsw-nx-monorepo/apps/greatify-api/src/modules/provider/
  + src/modules/partner/partner.model.ts
If AGENTS.md or learning.md is unreadable: STOP → `Status: NEEDS_CONTEXT`.

## Context (verified anchors)

- `data_sources(id, code UNIQUE, name, source_type, homepage_url,
  is_active)` — provenance catalog; every registered provider already
  must resolve a row + `budget>0` at boot (`cmd/api/main.go`).
- Adapters take `{apiKey, baseURL, enabledChains}` ONCE at construction
  (`New()` validates baseURL against package-private `defaultAllowedHosts`).
- `chainscan`'s key env is `ETHERSCAN_API_KEY` (not `CHAINSCAN_API_KEY`).
- Failover chain walk: `internal/transaction/service.go` — disabled-skip
  runs BEFORE claim/prepay (zero quota burn), `writeDisabled` writes
  honest `disabled` under chain[0]'s source_id; per-source state rows
  keyed `(network_id, wallet_id, source_id)` (migration 000019).
- Admin identity: `middleware.ClientFrom(c)` → `ClientIdentity{ID,Name,Role}`.
- OpenAPI uses cursor pagination (`cursor`/`next_cursor`).
- Next free migration number: 000020.

## Approved decisions

D1 DB-wins-if-set precedence, env = fallback · D2 AES-256-GCM if
`PROVIDER_CONFIG_KEK` set, else plaintext (documented) · D3 runtime
kill-switch, TTL-cached ~30s · D4 `TX_PROVIDER_*` env selectors stay ·
D5 tx-history providers only (12 codes). Risk: R3. Rollback: feature is
inert without rows (env parity = built-in flag); down-migration drops
both tables.

## Implementation (4 phases, TDD, conventional commits, no AI refs)

### Phase 1 — Schema + sqlc
Migration `000020_provider_configs.{up,down}.sql`:
```sql
provider_configs (
  source_code    VARCHAR(100) PRIMARY KEY REFERENCES data_sources(code),
  api_key        TEXT,
  base_url       TEXT,
  enabled_chains JSONB CHECK (enabled_chains IS NULL OR jsonb_typeof(enabled_chains)='array'),
  status         VARCHAR(16) NOT NULL DEFAULT 'active'
                 CHECK (status IN ('active','inactive')),
  updated_by     VARCHAR(64),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
)
provider_config_changes (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_code    VARCHAR(100) NOT NULL REFERENCES data_sources(code),
  actor_id       VARCHAR(64),          -- api_client Name (not numeric id)
  actor_type     VARCHAR(32),
  field          VARCHAR(64) NOT NULL,
  previous_value JSONB,
  new_value      JSONB,
  changed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
)
```
Queries (`queries/provider_configs.sql`): GetProviderConfig,
ListProviderConfigs, column-scoped updates (per-field UPDATE or
`FOR UPDATE`), InsertProviderConfigChange,
ListProviderConfigChanges(code, cursor, limit) — keyset cursor, newest
first. Regenerate sqlc; verify up+down on `check_wallet_*_test`.

### Phase 2 — Resolution layer + keystore (`internal/providerconfig/`)
- `resolver.go`: `Resolve(ctx,q,code)` →
  `{APIKey,BaseURL,EnabledChains,Status,Origin(db|env|default)}`.
  Per field: DB non-null wins; `enabled_chains=[]` and `base_url=""`
  are **explicit overrides** — only NULL falls back to env.
  Row absent ⇒ byte-identical env behavior (parity test: identical
  resolved config). **8-case truth table** (row-absent/db-null/
  db-empty/db-set × env-set/env-unset) all tested.
- `descriptor.go`: explicit `map[code]{keyEnv,baseURLEnv,chainsEnv}`
  for all 12 tx providers (`chainscan`→`ETHERSCAN_API_KEY`,
  `nodereal`→`NODEREAL_API_KEY`, others `<NAME>_*`); non-tx providers
  must be a data-add later, not rework.
- `keystore.go`: AES-256-GCM; wire shape `enc:v1:<b64 nonce>:<b64 ct>`
  validated by FULL marker shape before decrypting; plaintext value
  bearing `enc:` prefix + KEK set ⇒ boot-fatal misconfiguration;
  KEK unset ⇒ plaintext path (documented; KEK rotation = out-of-scope
  manual job).
- **Error routing:** ANY resolution error (DB failure, decrypt failure,
  allowlist rejection, malformed JSON) is **boot-fatal** naming
  provider+field — env fallback only on absent, never on error.
- `AllowedHostsFor`: export `provider.AllowedHostsFor(code) []string`
  (each adapter package publishes its allowlist) — phase-4 consumes it.
- Retain the boot-time resolved-config snapshot per provider (phase-4
  `warnings[]` compares stored vs in-effect).
- `cmd/api/main.go`: construct each tx adapter from resolved config;
  boot log prints `origin=db|env|default` per provider (never values);
  unknown `TX_PROVIDER_*` selector still fails boot; `data_sources`
  row + `budget>0` checks unchanged.

### Phase 3 — Runtime kill-switch (`internal/providerconfig/status_source.go`)
- Goroutine-safe `StatusCache` (RWMutex), ~30s TTL, process-local.
- `Active(ctx,code)`: `data_sources.is_active AND
  (provider_configs.status='active' OR row absent)`; missing row ⇒
  active (env parity); DB error ⇒ last-known value (cold-start ⇒
  active + error log).
- Chain walk consults it BEFORE any upstream spend, reusing the exact
  disabled-skip path ⇒ `status='disabled'` wire state via
  `writeDisabled`; all-inactive ⇒ disabled under chain[0]'s source_id.
- Stale `indexing` rows under a flipped source stay until
  StaleAfter/reactivation — chosen behavior (never served, self-heals).
- Tests: flip inactive ⇒ next refresh skips with zero upstream calls;
  flip back ⇒ serves; all-inactive ⇒ honest disabled; DB-error
  last-known.

### Phase 4 — Admin API (`internal/httpserver/provider_configs.go` +
`internal/providerconfig/admin_service.go`)

- `GET /api/v1/admin/provider-configs` + `GET .../{code}` — api_key
  serialized `{"set":bool,"last4":string|null}` (omit last4 when
  key length < 8); registered-but-unconfigured codes discoverable.
- `PUT .../{code}` — whitelist {api_key, base_url, enabled_chains,
  status}; status enum-validated; enabled_chains validated against
  registered network codes; base_url validated via
  `provider.AllowedHostsFor(code)` and error echoes allowed hosts;
  reject client `api_key` bearing `enc:` prefix (400); api_key passes
  through keystore (encrypt iff KEK set); column-scoped UPDATE or
  `FOR UPDATE` in one tx; unknown code ⇒ 404 never FK-500.
- Every changed field ⇒ `provider_config_changes` row in same tx;
  key values recorded MASKED `{"set","last4"}`; `actor_id`=client Name,
  `actor_type`="api_client", `updated_by` same source.
- `GET .../{code}/changes?cursor&limit` — `next_cursor` envelope.
- Responses carry `requires_restart:["api_key","base_url",
  "enabled_chains"]` (status is the only runtime-effective field) and
  PUT adds `warnings[]` when stored≠in-effect — compare against the
  boot-time resolved-config snapshot; `status=inactive` on the last
  active member of a chain adds a warning (greatify parity).
- Admin role via existing middleware; 400/404 never 500; OpenAPI
  updated (schemas + examples + restart disclosure).
- Tests: masking, whitelist rejection, invalid status/chain/host,
  non-admin 403, audit rows, concurrent two-field PUT, no plaintext
  key in response/log/audit.

## Non-negotiable invariants

- Missing/empty key ⇒ `Enabled()=false` — honest DISABLED, never failure.
- inactive ⇒ skipped exactly like disabled (zero traffic, never a
  failover trigger, never a failure state).
- No plaintext key in API responses, logs, audit rows, tests, commits.
- Parameterized SQL only; migrations reversible + verified on `_test` DB.
- `gofmt -l .` empty, `go build ./...`, `go vet ./...`,
  `go test -count=1 ./...` all green before reporting done.

## Report
Per phase: commit hash, tests added, deviations. End with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
