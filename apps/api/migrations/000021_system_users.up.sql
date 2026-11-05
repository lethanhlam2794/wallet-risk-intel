-- Migration 000021: system_users — human admin identities for the admin
-- console. Passwords are bcrypt hashes; raw passwords are never stored.
-- api_clients (API-key principals for programmatic access) is unchanged.
CREATE TABLE system_users (
    id BIGSERIAL PRIMARY KEY,
    username VARCHAR(80) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role VARCHAR(20) NOT NULL CHECK (role IN ('superadmin', 'admin')),
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at TIMESTAMPTZ
);
