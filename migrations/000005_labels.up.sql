-- Migration 000005: public wallet labels (spec §7.11–7.12, phase 3).
-- Staging follows the established convention: no FK / no cascade — failed-run
-- staging is retained for diagnosis; lifecycle is managed by purge lists.

CREATE TABLE risk_categories (
    id BIGSERIAL PRIMARY KEY,
    code VARCHAR(60) NOT NULL UNIQUE,
    name VARCHAR(150) NOT NULL,
    category_type VARCHAR(30) NOT NULL,
    CHECK (category_type IN ('LEGAL','MALICIOUS_ACTIVITY','SERVICE_TYPE','ENTITY_TYPE','OTHER'))
);

-- Seed set: spec §7.11 suggested categories + every upstream-observed
-- category (verified live 2026-11-03). Unseen upstream categories still
-- land at publish time as dynamic rows with category_type='OTHER'.
INSERT INTO risk_categories (code, name, category_type) VALUES
    ('SANCTIONED', 'Sanctioned', 'LEGAL'),
    ('HACK', 'Hack', 'MALICIOUS_ACTIVITY'),
    ('SCAM', 'Scam', 'MALICIOUS_ACTIVITY'),
    ('PHISHING', 'Phishing', 'MALICIOUS_ACTIVITY'),
    ('DRAINER', 'Drainer', 'MALICIOUS_ACTIVITY'),
    ('RANSOMWARE', 'Ransomware', 'MALICIOUS_ACTIVITY'),
    ('MONEY_LAUNDERING', 'Money Laundering', 'MALICIOUS_ACTIVITY'),
    ('TERRORISM', 'Terrorism', 'MALICIOUS_ACTIVITY'),
    ('DARKNET', 'Darknet', 'MALICIOUS_ACTIVITY'),
    ('EXTREMISM', 'Extremism', 'MALICIOUS_ACTIVITY'),
    ('FROZEN', 'Frozen', 'MALICIOUS_ACTIVITY'),
    ('GAMBLING', 'Gambling', 'SERVICE_TYPE'),
    ('MIXER', 'Mixer', 'SERVICE_TYPE'),
    ('MINING', 'Mining', 'SERVICE_TYPE'),
    ('SERVICE', 'Service', 'SERVICE_TYPE'),
    ('BRIDGE', 'Bridge', 'SERVICE_TYPE'),
    ('MEV', 'MEV', 'SERVICE_TYPE'),
    ('DEFI', 'DeFi', 'SERVICE_TYPE'),
    ('EXCHANGE', 'Exchange', 'ENTITY_TYPE'),
    ('ENTITY', 'Entity', 'ENTITY_TYPE'),
    ('OTHER', 'Other', 'OTHER');

CREATE TABLE wallet_labels (
    id BIGSERIAL PRIMARY KEY,
    wallet_id BIGINT NOT NULL REFERENCES wallets(id),
    category_id BIGINT NOT NULL REFERENCES risk_categories(id),
    source_id BIGINT NOT NULL REFERENCES data_sources(id),
    entity_id BIGINT REFERENCES entities(id),
    external_record_id TEXT,
    fingerprint TEXT NOT NULL UNIQUE,
    label TEXT NOT NULL,
    confidence VARCHAR(20) NOT NULL DEFAULT 'unknown',
    verification_status VARCHAR(30) NOT NULL DEFAULT 'unverified',
    source_url TEXT,
    upstream_category TEXT,            -- original upstream category string
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    observed_at TIMESTAMPTZ,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,
    CHECK (confidence IN ('unknown','low','medium','high')),
    CHECK (verification_status IN ('unverified','reported','verified','disputed'))
);
CREATE INDEX idx_wallet_labels_wallet ON wallet_labels(wallet_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_wallet_labels_evidence ON wallet_labels USING GIN(evidence);
CREATE INDEX idx_wallet_labels_category ON wallet_labels(category_id) WHERE revoked_at IS NULL;

-- F8: NO FK / no cascade — matches OFAC staging convention (failed-run
-- staging retained for diagnosis; lifecycle via purge lists).
CREATE TABLE stg_wallet_labels (
    run_id UUID NOT NULL,
    network_code TEXT NOT NULL,
    address TEXT NOT NULL,
    entity_name TEXT,
    label TEXT NOT NULL,
    upstream_category TEXT NOT NULL,
    source_field TEXT,                 -- upstream `source` column (a+b+c)
    source_url TEXT,
    confidence TEXT NOT NULL,
    last_updated TEXT,
    fingerprint TEXT NOT NULL,
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (run_id, fingerprint)
);
CREATE INDEX idx_stg_wallet_labels_run ON stg_wallet_labels(run_id);

-- F5/F9: generic per-source snapshot registry (labels + future bulk
-- sources); sanctions_snapshots stays OFAC-shaped, NOT migrated.
-- checksum stores the artifact checksum so the checksum-identical no-op
-- check works at OFAC parity (phase-03 run behavior).
CREATE TABLE data_source_snapshots (
    id BIGSERIAL PRIMARY KEY,
    source_id BIGINT NOT NULL REFERENCES data_sources(id),
    version TEXT NOT NULL,
    checksum TEXT NOT NULL,
    published_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    row_count BIGINT NOT NULL,
    UNIQUE(source_id, version)
);

-- F6: shared entity registry dedup — canonical whitespace (inner runs
-- collapsed, ends trimmed) + case-insensitive identity, so 'Binance ' and
-- 'Binance' resolve to one row. entity_type is first-write-wins at publish.
CREATE UNIQUE INDEX uq_entities_name_ci
ON entities (LOWER(TRIM(regexp_replace(name, '\s+', ' ', 'g'))));

-- Registered as 'snapshot' not 'bulk': the artifact is a full-file snapshot
-- with a soft-revoke lifecycle; BulkSource cursor semantics don't fit a
-- git-hosted file (taxonomy rationale recorded per Critic F17).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('wallet_attribution', 'Wallet Attribution (prettydeath)', 'snapshot',
     'https://github.com/prettydeath/wallet-attribution');
