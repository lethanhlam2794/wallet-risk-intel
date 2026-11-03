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
