-- name: GetAPIClientByKeyHash :one
SELECT * FROM api_clients WHERE key_hash = $1;

-- name: TouchAPIClient :exec
UPDATE api_clients SET last_seen_at = NOW() WHERE id = $1;

-- name: UpsertAPIClient :one
INSERT INTO api_clients (name, key_hash, role)
VALUES ($1, $2, $3)
ON CONFLICT (key_hash)
DO UPDATE SET name = EXCLUDED.name, role = EXCLUDED.role, is_active = TRUE
RETURNING *;

-- Admin client-management surface (admin-api-clients plan). key_hash and
-- raw keys never leave the service layer — views are built in apiclient.

-- name: GetAPIClientByID :one
SELECT * FROM api_clients WHERE id = $1;

-- Newest first, keyset cursor on (created_at, id) — repo convention.
-- name: ListAPIClients :many
SELECT * FROM api_clients
WHERE (sqlc.narg('cursor_ts')::timestamptz IS NULL
       OR (created_at, id)
          < (sqlc.narg('cursor_ts'), sqlc.narg('cursor_id')::bigint))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('lim')::bigint;

-- name: InsertAPIClient :one
INSERT INTO api_clients (name, key_hash, role)
VALUES ($1, $2, $3)
RETURNING *;

-- Row lock for the read-modify-write admin patch; taken after the
-- namespaced advisory lock so A↔B demotes serialize without deadlock.
-- name: GetAPIClientForUpdate :one
SELECT * FROM api_clients WHERE id = $1 FOR UPDATE;

-- Serializes every admin-lockout-guarded mutation inside its tx.
-- Namespaced via hashtext — ingestion's session advisory lock space is
-- raw source_id. Invariant: every role/is_active writer holds it
-- (UpsertAPIClient is exempt — it never removes admins).
-- name: LockClientAdminTransitions :exec
SELECT pg_advisory_xact_lock(hashtext('apiclient.admin_guard'));

-- Column-scoped writes (provider_configs convention): a concurrent PATCH
-- on another field never clobbers it, and last_seen_at is never reset.
-- name: UpdateAPIClientName :exec
UPDATE api_clients SET name = $2 WHERE id = $1;

-- name: UpdateAPIClientRole :exec
UPDATE api_clients SET role = $2 WHERE id = $1;

-- name: UpdateAPIClientActive :exec
UPDATE api_clients SET is_active = $2 WHERE id = $1;

-- Post-state lockout guard: active admins other than the target row.
-- name: CountOtherActiveAdmins :one
SELECT count(*) FROM api_clients
WHERE role = 'admin' AND is_active AND id != $1;
