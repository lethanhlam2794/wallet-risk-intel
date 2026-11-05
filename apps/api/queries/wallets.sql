-- name: GetOrCreateWallet :one
INSERT INTO wallets (network_id, address, normalized_address, address_type)
VALUES ($1, $2, $3, $4)
ON CONFLICT (network_id, normalized_address)
DO UPDATE SET updated_at = wallets.updated_at
RETURNING *;
