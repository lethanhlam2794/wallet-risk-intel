# Plan — DB-managed Provider Config (port of greatify partner/provider pattern)

status: pending
feature: db-provider-config
spec: plans/20261104-1800-db-provider-config/prompt-master.md
reference: /home/lethanhlam/Desktop/GSW-2/gsw-nx-monorepo/apps/greatify-api (read-only)

## Overview

Move per-provider operational config (API key, base URL, enabled
chains, active status) from env-only into a DB-managed, admin-editable
surface — patterned on greatify-api's `providers`/`provider_changes`/
`partners.authInfo` — preserving every tx-provider-expansion invariant.

## Decisions (user-approved)

| # | Decision |
|---|---|
| D1 | Precedence: **DB-wins-if-set, env = fallback** |
| D2 | Key at rest: **AES-256-GCM when `PROVIDER_CONFIG_KEK` set, plaintext otherwise** (documented honestly) |
| D3 | Kill-switch: **runtime TTL-cached status check (~30s)** inside chain walk — inactive ⇒ skipped as disabled |
| D4 | Chain ordering: **`TX_PROVIDER_*` env stays** in v1 |
| D5 | Scope: **tx-history providers only** in v1 (alchemy, blockscout, ankr, covalent, moralis, tatum, bitquery, oklink, tokenview, chainbase, chainscan, nodereal) |

## Phase roadmap

| Phase | Title | Depends |
|---|---|---|
| 01 | Schema + queries (provider_configs, provider_config_changes, migration 000020, sqlc) | — |
| 02 | Resolution layer + boot wiring (config merge, encryption, adapter construction) | 01 |
| 03 | Runtime kill-switch (TTL-cached status in chain walk) | 02 |
| 04 | Admin API (masked reads, whitelisted writes, audit rows, OpenAPI) | 02 |

## Invariants (non-negotiable)

- Missing/empty key ⇒ `Enabled()=false` — honest DISABLED, never failure.
- `status=inactive` ⇒ skipped exactly like disabled (zero traffic,
  never a failover trigger, never a failure state).
- Unknown `TX_PROVIDER_*` name ⇒ boot error (unchanged).
- Every registered provider must resolve `data_sources` row +
  `budget > 0` (unchanged boot checks).
- `provider_configs` row absent ⇒ byte-identical current env behavior.
- No plaintext key in any API response, log line, audit row, or test.
- Parameterized SQL only; migrations reversible + verified up/down on
  `_test` DB.

## Risk matrix

- **Encryption key loss** → rows unreadable ⇒ document KEK rotation as
  re-encrypt migration path; plaintext fallback stays honest.
- **Status flapping mid-request** ⇒ TTL snapshot per EnsureIndexed call;
  a status flip only affects *next* refresh — acceptable.
- **Adapter construction time** ⇒ adapters build once at boot; runtime
  key changes need restart (kill-switch is runtime; keys are boot-time —
  documented).
- **Config drift env↔DB** ⇒ boot log prints effective source
  (db|env|default) per provider without values.
- **Audit masking** ⇒ api_key changes recorded as `{"set":bool,"last4":string|null}`.

## Amendments (critic R1)

- **Risk: R3** (schema + credential-handling surface). Rollback: feature
  is inert without rows — absent `provider_configs` rows = env parity is
  the built-in kill switch; down-migration drops both tables.
- **Effective-active formula** (invariant): `active(source) =
  data_sources.is_active AND (provider_configs.status='active' OR row
  absent)`. `status` is the operator-facing switch; `is_active` is the
  catalog-level switch.
- **Restart semantics (invariant):** `status` is runtime-effective
  (~30s TTL); `api_key`/`base_url`/`enabled_chains` are boot-effective
  only — admin API responses must disclose `requires_restart` per field.
- **Resolution errors are boot-fatal** (DB failure, decrypt failure,
  allowlist rejection, malformed JSON) and name the provider+field;
  env fallback happens only on *absent* values, never on errors.
- Stale `provider_configs` rows for unregistered source codes are
  ignored with a boot warn log; `enabled_chains=[]` and `base_url=""`
  are explicit overrides — only NULL falls back to env.
