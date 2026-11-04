---
phase: 1
title: Schema + sqlc queries — provider_configs & provider_config_changes
status: pending
priority: high
dependencies: []
---

## Overview
New tables per plan.md; reversible migration 000020; sqlc query layer.

## Requirements
- `provider_configs(source_code PK→data_sources.code, api_key TEXT,
  base_url TEXT, enabled_chains JSONB NULL, status VARCHAR(16)
  CHECK in ('active','inactive') DEFAULT 'active', updated_by VARCHAR(64),
  updated_at TIMESTAMPTZ DEFAULT NOW())`
- `provider_config_changes(id UUID PK, source_code FK, actor_id,
  actor_type, field, previous_value JSONB, new_value JSONB,
  changed_at TIMESTAMPTZ)`
- Queries (`queries/provider_configs.sql`): GetProviderConfig(code),
  ListProviderConfigs, UpsertProviderConfig,
  InsertProviderConfigChange, ListProviderConfigChanges(code, cursor, limit) — keyset/cursor per repo convention
- Run sqlc; generated code compiles.

## Related Code Files
- Create: migrations/000020_provider_configs.{up,down}.sql
- Create: queries/provider_configs.sql

## Implementation Steps
1. Write migration up+down (TDD red: migrate-test fails on missing table).
2. Write sqlc queries; regenerate.
3. Green: `make migrate-test` up+down; generated queries compile.

## Success Criteria
- [ ] Up+down verified on check_wallet_test
- [ ] sqlc gen clean; `go build ./internal/db` green
- [ ] status CHECK constraint; enabled_chains JSONB NULLable

## Risk Assessment
Low — additive tables only; no change to existing rows.

## Amendments (critic R1)
- `provider_config_changes.id` ⇒ `UUID PRIMARY KEY DEFAULT gen_random_uuid()`.
- `enabled_chains` gets `CHECK (enabled_chains IS NULL OR
  jsonb_typeof(enabled_chains)='array')` — fail-closed on non-array.

- `actor_id`/`updated_by` are `VARCHAR(64)` storing the api_client Name (not the numeric id).
