-- Migration 000002: OFAC staging tables (spec §8.1 stage → validate → publish).
-- All staging rows are scoped by run_id so concurrent/crashed runs cannot
-- contaminate each other; failed-run staging is retained for diagnosis.

CREATE TABLE stg_sanctions_entities (
    run_id UUID NOT NULL,
    external_entity_id TEXT NOT NULL,
    primary_name TEXT NOT NULL,
    entity_type VARCHAR(60),
    source_url TEXT,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE(run_id, external_entity_id)
);
CREATE INDEX idx_stg_entities_run ON stg_sanctions_entities(run_id);

CREATE TABLE stg_sanctions_programs (
    run_id UUID NOT NULL,
    code VARCHAR(100) NOT NULL,
    name TEXT,
    description TEXT,
    source_url TEXT,
    UNIQUE(run_id, code)
);

CREATE TABLE stg_sanctions_entity_programs (
    run_id UUID NOT NULL,
    external_entity_id TEXT NOT NULL,
    program_code VARCHAR(100) NOT NULL,
    PRIMARY KEY(run_id, external_entity_id, program_code)
);
CREATE INDEX idx_stg_entity_programs_run ON stg_sanctions_entity_programs(run_id);

CREATE TABLE stg_sanctions_wallets (
    run_id UUID NOT NULL,
    external_entity_id TEXT NOT NULL,
    listed_address TEXT NOT NULL,
    normalized_address TEXT NOT NULL,
    currency_code VARCHAR(40),
    network_id BIGINT,
    identifier_type TEXT NOT NULL,
    source_identifier_id TEXT,
    match_scope VARCHAR(30) NOT NULL DEFAULT 'CHAIN_SPECIFIC',
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb
);
-- Staging dedup mirrors the live unique-key tuple (phase 03 contract):
-- same tuple collapses once; different Feature@IDs stay distinct rows.
CREATE UNIQUE INDEX uq_stg_wallets_identifier ON stg_sanctions_wallets(
    run_id,
    external_entity_id,
    normalized_address,
    identifier_type,
    COALESCE(currency_code, ''),
    COALESCE(source_identifier_id, '')
);
CREATE INDEX idx_stg_wallets_run ON stg_sanctions_wallets(run_id);

CREATE TABLE stg_sanctions_references (
    run_id UUID NOT NULL,
    external_entity_id TEXT NOT NULL,
    reference_type VARCHAR(60) NOT NULL,
    title TEXT,
    reference_url TEXT,
    published_at TIMESTAMPTZ,
    details JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE UNIQUE INDEX uq_stg_references ON stg_sanctions_references(
    run_id,
    external_entity_id,
    reference_type,
    COALESCE(title, ''),
    COALESCE(reference_url, '')
);
CREATE INDEX idx_stg_references_run ON stg_sanctions_references(run_id);
