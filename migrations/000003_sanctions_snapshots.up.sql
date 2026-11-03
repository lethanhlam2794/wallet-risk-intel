-- Migration 000003: published snapshot registry + ingestion audit trail and
-- single-flight gate (phase 04 contract).

CREATE TABLE sanctions_snapshots (
    id BIGSERIAL PRIMARY KEY,
    source_id BIGINT NOT NULL REFERENCES data_sources(id),
    version TEXT NOT NULL UNIQUE,
    checksum TEXT NOT NULL,
    entity_count BIGINT NOT NULL,
    wallet_count BIGINT NOT NULL,
    program_count BIGINT NOT NULL,
    date_of_issue DATE,
    published_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Audit trail: which api client / 'worker' triggered the run.
ALTER TABLE ingestion_runs ADD COLUMN triggered_by TEXT;

-- DB-layer single-flight gate (G-005): at most one RUNNING row per source.
CREATE UNIQUE INDEX uq_ingestion_runs_running
ON ingestion_runs(source_id) WHERE status = 'RUNNING';
