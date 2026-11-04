-- Migration 000020: DB-managed provider config (db-provider-config plan).
-- provider_configs carries per-source operational overrides (api key, base
-- url, enabled chains, operator status); provider_config_changes is the
-- admin audit trail — api_key values are recorded masked, never plaintext.
CREATE TABLE provider_configs (
    source_code VARCHAR(100) PRIMARY KEY
        REFERENCES data_sources(code) ON UPDATE CASCADE,
    api_key TEXT,
    base_url TEXT,
    enabled_chains JSONB,
    status VARCHAR(16) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'inactive')),
    updated_by VARCHAR(64),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (enabled_chains IS NULL OR jsonb_typeof(enabled_chains) = 'array')
);

CREATE TABLE provider_config_changes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_code VARCHAR(100) NOT NULL
        REFERENCES data_sources(code) ON UPDATE CASCADE,
    actor_id VARCHAR(64),
    actor_type VARCHAR(32) NOT NULL,
    field VARCHAR(64) NOT NULL,
    previous_value JSONB,
    new_value JSONB,
    changed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Keyset pagination (changed_at DESC, id DESC) scoped per source.
CREATE INDEX idx_provider_config_changes_source
    ON provider_config_changes (source_code, changed_at DESC, id DESC);
