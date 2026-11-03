-- Migration 000001: core + sanctions schema (spec §7 subset for phases 1-2).
-- Column definitions follow spec §7 verbatim where specified.

CREATE TABLE blockchain_networks (
    id BIGSERIAL PRIMARY KEY,
    namespace VARCHAR(30) NOT NULL,
    chain_reference VARCHAR(100) NOT NULL,
    code VARCHAR(50) NOT NULL UNIQUE,
    name VARCHAR(100) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    UNIQUE(namespace, chain_reference)
);

CREATE TABLE wallets (
    id BIGSERIAL PRIMARY KEY,
    network_id BIGINT NOT NULL
        REFERENCES blockchain_networks(id),
    address VARCHAR(255) NOT NULL,
    normalized_address VARCHAR(255) NOT NULL,
    address_type VARCHAR(30) NOT NULL DEFAULT 'unknown',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(network_id, normalized_address)
);

CREATE INDEX idx_wallets_normalized
ON wallets(normalized_address);

CREATE TABLE data_sources (
    id BIGSERIAL PRIMARY KEY,
    code VARCHAR(100) NOT NULL UNIQUE,
    name VARCHAR(150) NOT NULL,
    source_type VARCHAR(30) NOT NULL,
    homepage_url TEXT,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE entities (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    entity_type VARCHAR(60),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE sanctions_entities (
    id BIGSERIAL PRIMARY KEY,
    source_id BIGINT NOT NULL REFERENCES data_sources(id),
    external_entity_id TEXT NOT NULL,
    entity_id BIGINT REFERENCES entities(id),
    primary_name TEXT NOT NULL,
    entity_type VARCHAR(60),
    source_url TEXT,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,
    UNIQUE(source_id, external_entity_id)
);

CREATE TABLE sanctions_programs (
    id BIGSERIAL PRIMARY KEY,
    source_id BIGINT NOT NULL REFERENCES data_sources(id),
    code VARCHAR(100) NOT NULL,
    name TEXT,
    description TEXT,
    source_url TEXT,
    UNIQUE(source_id, code)
);

CREATE TABLE sanctions_entity_programs (
    sanctions_entity_id BIGINT NOT NULL
        REFERENCES sanctions_entities(id),
    program_id BIGINT NOT NULL
        REFERENCES sanctions_programs(id),
    PRIMARY KEY(sanctions_entity_id, program_id)
);

CREATE TABLE sanctions_wallets (
    id BIGSERIAL PRIMARY KEY,
    sanctions_entity_id BIGINT NOT NULL
        REFERENCES sanctions_entities(id),

    wallet_id BIGINT REFERENCES wallets(id),

    listed_address TEXT NOT NULL,
    normalized_address TEXT NOT NULL,

    currency_code VARCHAR(40),
    network_id BIGINT REFERENCES blockchain_networks(id),

    identifier_type TEXT NOT NULL,
    source_identifier_id TEXT,
    match_scope VARCHAR(30) NOT NULL DEFAULT 'CHAIN_SPECIFIC',

    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,

    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

CREATE INDEX idx_sanctions_wallets_address
ON sanctions_wallets(normalized_address);

CREATE INDEX idx_sanctions_wallets_entity
ON sanctions_wallets(sanctions_entity_id);

CREATE UNIQUE INDEX uq_sanctions_wallet_identifier
ON sanctions_wallets(
    sanctions_entity_id,
    normalized_address,
    identifier_type,
    COALESCE(currency_code, ''),
    COALESCE(source_identifier_id, '')
);

CREATE TABLE sanctions_references (
    id BIGSERIAL PRIMARY KEY,
    sanctions_entity_id BIGINT NOT NULL
        REFERENCES sanctions_entities(id),

    reference_type VARCHAR(60) NOT NULL,
    title TEXT,
    reference_url TEXT,
    published_at TIMESTAMPTZ,
    details JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE ingestion_runs (
    id UUID PRIMARY KEY,

    source_id BIGINT NOT NULL REFERENCES data_sources(id),

    status VARCHAR(30) NOT NULL,

    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,

    records_fetched BIGINT NOT NULL DEFAULT 0,
    records_inserted BIGINT NOT NULL DEFAULT 0,
    records_updated BIGINT NOT NULL DEFAULT 0,
    records_rejected BIGINT NOT NULL DEFAULT 0,

    snapshot_checksum TEXT,
    snapshot_version TEXT,
    cursor_value TEXT,
    error_message TEXT
);

CREATE INDEX idx_ingestion_runs_source
ON ingestion_runs(source_id, started_at DESC);

-- API authentication clients (spec §16: API key auth, role-based admin).
-- key_hash is a SHA-256 hex digest of the API key; raw keys are never stored.
CREATE TABLE api_clients (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(150) NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,
    role VARCHAR(20) NOT NULL CHECK (role IN ('reader', 'admin')),
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ
);

-- Seeds: supported networks (eip155:1, eip155:56) and the OFAC source.
INSERT INTO blockchain_networks (namespace, chain_reference, code, name) VALUES
    ('eip155', '1', 'ethereum', 'Ethereum Mainnet'),
    ('eip155', '56', 'bsc', 'BNB Smart Chain');

INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('ofac_sdn', 'OFAC SDN Advanced XML', 'snapshot',
     'https://sanctionslistservice.ofac.treas.gov/');
