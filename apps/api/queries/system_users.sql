-- name: GetSystemUserByUsername :one
SELECT * FROM system_users WHERE username = $1;

-- name: CreateSystemUser :one
INSERT INTO system_users (username, password_hash, role)
VALUES ($1, $2, $3)
RETURNING *;

-- name: TouchSystemUserLogin :exec
UPDATE system_users SET last_login_at = NOW() WHERE id = $1;

-- name: CountSuperAdmins :one
SELECT COUNT(*) FROM system_users WHERE role = 'superadmin' AND is_active;

-- name: ListSystemUsers :many
SELECT id, username, role, is_active, created_at, last_login_at
FROM system_users ORDER BY username;
