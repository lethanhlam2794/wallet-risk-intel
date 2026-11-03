-- name: ListActiveNetworks :many
SELECT * FROM blockchain_networks WHERE is_active = TRUE ORDER BY code;
