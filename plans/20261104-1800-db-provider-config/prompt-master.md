# Prompt-Master — DB-managed Provider Config (port of greatify "partner/provider" pattern)

**For:** Codex review → implementation plan
**Repo:** `/home/lethanhlam/Desktop/Profile/check-wallet` (Go/Gin/pgx/sqlc)
**Reference implementation (pattern source):**
`/home/lethanhlam/Desktop/GSW-2/gsw-nx-monorepo/apps/greatify-api` (NestJS/TypeORM — read-only reference)

## Goal

Move per-provider operational config (API key, base URL, enabled
chains, active status) out of env vars into a DB-managed, admin-editable
surface — the way greatify-api manages providers/partners — **without**
weakening any honesty invariant of the tx-provider expansion.

## Pattern found in greatify-api (verified)

- `providers` table: platform catalog — `code` PK, `name`, `category`,
  `status` (active/inactive = kill switch), `capabilities` (editable-field
  whitelist), `route_priority`, `api_url`, `webhook_url`, fee fields.
  (`src/modules/provider/provider.model.ts`)
- `provider_changes` table: append-only audit — one row per changed
  field, carries `actor_id/actor_type/actor_source`, `previous_value`,
  `new_value`. (`provider.model.ts:86-115`)
- `ProviderService`: `updateStatus`/`updateConfig` run in a transaction,
  every field mutation writes a change row; `assertProviderActive` is the
  kill-switch consulted at flow entry points; boot guard
  `onApplicationBootstrap` **fails loudly** if any code-routable provider
  lacks a catalog row. (`provider.service.ts:88-102`)
- `partners` table: credentials per partner — `auth_type` +
  `auth_info` JSONB (`token`/`access_id`/`access_secret`/`api_key` per
  type), `base_url`, `private_key`, `webhook_url`, `domains`, `metadata`.
  (`src/modules/partner/partner.model.ts:21-54`)
- Admin CRUD via `internal-provider.controller.ts` (role-gated);
  metrics (30d success/error rates) decorated onto list/detail reads
  with a 60s advisory cache.

## What check-wallet already has (verified)

- `data_sources` table: `id`, `code` UNIQUE, `name`, `source_type`,
  `homepage_url`, `is_active` — the provenance catalog every adapter
  already must resolve at boot (`cmd/api/main.go` boot check:
  `GetSourceByCode(p.Name())` + `budget > 0`).
- Admin role exists in middleware (`api_clients.role`).
- Config: `internal/config/config.go` reads env → adapters constructed
  once at boot in `cmd/api/main.go`.
- Invariants to preserve: missing key ⇒ `Enabled()=false` (honest
  DISABLED, never failure); unknown selector ⇒ boot error; failover
  never treats disabled as failure; per-source watermarks.

## Proposed design (for review)

### 1. Schema — migration `000020_provider_configs`

New table (separate from `data_sources` — keeps provenance catalog clean,
relational-first):

```sql
provider_configs (
  source_code      VARCHAR(100) PRIMARY KEY REFERENCES data_sources(code),
  api_key          TEXT,            -- see D2 encryption decision
  base_url         TEXT,
  enabled_chains   JSONB,           -- e.g. ["ethereum","bsc"], NULL = no override
  status           VARCHAR(16) NOT NULL DEFAULT 'active',  -- active|inactive
  updated_by       VARCHAR(64),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
)
provider_config_changes (           -- port of provider_changes
  id               UUID PRIMARY KEY,
  source_code      VARCHAR(100) NOT NULL REFERENCES data_sources(code),
  actor_id         VARCHAR(64),
  actor_type       VARCHAR(32),
  field            VARCHAR(64) NOT NULL,
  previous_value   JSONB,           -- api_key values stored MASKED, never plaintext
  new_value        JSONB,
  changed_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
)
```

### 2. Resolution layer — internal/providerconfig/

Boot-time load; precedence **DB-wins-if-set, else env** (env = seed/fallback):

```
key(source)      := provider_configs.api_key  if non-empty  else <SRC>_API_KEY
base_url(source) := provider_configs.base_url if non-empty  else <SRC>_BASE_URL/default
chains(source)   := provider_configs.enabled_chains if set  else <SRC>_ENABLED_CHAINS
```

`Enabled()` derivation unchanged: key empty ⇒ disabled. `status=inactive`
⇒ treated as disabled (honest skip, zero traffic) — see D3.

### 3. Admin API (role-gated, existing admin role)

- `GET  /api/v1/admin/provider-configs` — list (api_key masked: `{"set":true,"last4":"…wxyz"}`)
- `GET  /api/v1/admin/provider-configs/{code}`
- `PUT  /api/v1/admin/provider-configs/{code}` — set api_key/base_url/enabled_chains/status;
  field-whitelist enforced; every mutation writes `provider_config_changes`
- `GET  /api/v1/admin/provider-configs/{code}/changes` — audit history
- Never returns plaintext keys in any response.

### 4. Runtime kill-switch (D3 — decide scope)

Two options:
- **(a) Boot-only (minimal):** config resolved once at boot; `status=inactive`
  applies on next restart. Simplest, zero per-request cost.
- **(b) TTL-cached runtime check (greatify-faithful):** service checks
  `provider_configs.status` with a ~30s in-process cache inside the
  failover chain walk — inactive ⇒ skipped as disabled. Kill switch works
  without redeploy. Small complexity cost.

## Open decisions to confirm

- **D1 — precedence:** DB-wins-if-set + env fallback (proposed) vs env-override-wins vs env-as-seed-only-once.
- **D2 — key at rest:** plaintext in DB (greatify does this in `auth_info` jsonb) vs AES-256-GCM with master key env `PROVIDER_CONFIG_KEK` (one env to rule them all). Proposed: encrypt-if-KEK-set, plaintext otherwise, documented honestly.
- **D3 — kill-switch timing:** boot-only vs TTL-cached runtime.
- **D4 — chain ordering:** keep `TX_PROVIDER_*` env selectors (proposed, v1) vs move chain order into DB (`route_priority`-style) later.
- **D5 — scope:** tx-history providers only (proposed v1) vs all providers incl. goplus/publicaml/chainabuse/ofac.

## Constraints (non-negotiable)

- No secrets in logs/API responses/commits; audit rows mask keys.
- Boot invariants preserved: registered provider must resolve
  `data_sources` row + `budget > 0`; unknown `TX_PROVIDER_*` name fails boot.
- `provider_configs` row optional — absent ⇒ current env-only behavior
  (backwards compatible).
- Migrations reversible, tested up/down on `_test` DB.
- Read-only reference: do not modify the greatify repo.

## Acceptance criteria

- Boot with only DB-stored key (env empty) ⇒ adapter enabled, indexing works.
- `status=inactive` via admin API ⇒ provider skipped as disabled (never failure).
- Key update via API ⇒ visible in `provider_config_changes` with masked values.
- `GET` on configs never exposes plaintext key.
- `go build ./...`, `go vet`, `gofmt`, `go test ./...` green; migration up/down verified.
