-- DB-managed provider config (db-provider-config plan): operational
-- overrides over env per tx-history provider, plus the admin audit trail.
-- api_key is written through the keystore (enc:v1: envelope when KEK is
-- set, plaintext otherwise) and recorded masked in change rows.

-- name: GetProviderConfig :one
SELECT * FROM provider_configs WHERE source_code = $1;

-- name: ListProviderConfigs :many
SELECT * FROM provider_configs ORDER BY source_code;

-- Row lock for the read-modify-write admin update; no row ⇒ caller inserts.
-- name: GetProviderConfigForUpdate :one
SELECT * FROM provider_configs WHERE source_code = $1 FOR UPDATE;

-- First-write materialization; DO NOTHING keeps a concurrent first PUT
-- serialized — the loser re-selects FOR UPDATE instead of eating 23505.
-- name: InsertProviderConfigRow :exec
INSERT INTO provider_configs (source_code, updated_by)
VALUES ($1, $2)
ON CONFLICT (source_code) DO NOTHING;

-- name: UpsertProviderConfig :one
INSERT INTO provider_configs (source_code, api_key, base_url, enabled_chains, status, updated_by)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (source_code) DO UPDATE SET
    api_key = EXCLUDED.api_key,
    base_url = EXCLUDED.base_url,
    enabled_chains = EXCLUDED.enabled_chains,
    status = EXCLUDED.status,
    updated_by = EXCLUDED.updated_by,
    updated_at = NOW()
RETURNING *;

-- Column-scoped writes: a concurrent PUT patching another field never
-- clobbers a value it didn't read (lost-update guard, critic R1).
-- name: UpdateProviderConfigAPIKey :exec
UPDATE provider_configs
SET api_key = $2, updated_by = $3, updated_at = NOW()
WHERE source_code = $1;

-- name: UpdateProviderConfigBaseURL :exec
UPDATE provider_configs
SET base_url = $2, updated_by = $3, updated_at = NOW()
WHERE source_code = $1;

-- name: UpdateProviderConfigEnabledChains :exec
UPDATE provider_configs
SET enabled_chains = $2, updated_by = $3, updated_at = NOW()
WHERE source_code = $1;

-- name: UpdateProviderConfigStatus :exec
UPDATE provider_configs
SET status = $2, updated_by = $3, updated_at = NOW()
WHERE source_code = $1;

-- name: InsertProviderConfigChange :one
INSERT INTO provider_config_changes
    (source_code, actor_id, actor_type, field, previous_value, new_value)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- Newest first, keyset cursor on (changed_at, id) — repo convention.
-- name: ListProviderConfigChanges :many
SELECT * FROM provider_config_changes
WHERE source_code = $1
  AND (sqlc.narg('cursor_ts')::timestamptz IS NULL
       OR (changed_at, id)
          < (sqlc.narg('cursor_ts'), sqlc.narg('cursor_id')::uuid))
ORDER BY changed_at DESC, id DESC
LIMIT sqlc.arg('lim')::bigint;

-- Effective-active (critic R1 invariant): catalog switch ANDs with the
-- operator switch — is_active AND (status='active' OR row absent).
-- name: GetProviderEffectiveStatus :one
SELECT ds.is_active,
       COALESCE(pc.status, 'active') AS config_status
FROM data_sources ds
LEFT JOIN provider_configs pc ON pc.source_code = ds.code
WHERE ds.code = $1;
