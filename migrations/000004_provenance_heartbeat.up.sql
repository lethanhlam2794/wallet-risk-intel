-- Migration 000004: provenance soft-revocation + run liveness heartbeat.
--
-- References and entity-program links get the same revoked_at lifecycle as
-- entities and wallet listings: publish now soft-revokes rows absent from
-- the staged snapshot instead of hard-deleting them, so official evidence
-- for a delisted entity stays queryable (spec §6 provenance).
--
-- ingestion_runs.heartbeat_at gives the stale-run sweep a true liveness
-- signal: a live run heartbeats every heartbeatInterval; only rows whose
-- last beat (or start, if no beat yet) is older than the reap window are
-- marked crashed.

ALTER TABLE sanctions_entity_programs
    ADD COLUMN revoked_at TIMESTAMPTZ;

ALTER TABLE sanctions_references
    ADD COLUMN first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN revoked_at    TIMESTAMPTZ;

-- OFAC references carry no stable identifier; identity is the dedup tuple
-- the staging layer already enforces. A full (non-partial) index lets
-- publish upserts revive a previously-revoked row in place, preserving
-- first_seen_at across delist→relist cycles.
CREATE UNIQUE INDEX uq_sanctions_reference_identity
ON sanctions_references(
    sanctions_entity_id,
    reference_type,
    COALESCE(title, ''),
    COALESCE(reference_url, '')
);

CREATE INDEX idx_sanctions_references_entity
ON sanctions_references(sanctions_entity_id);

ALTER TABLE ingestion_runs
    ADD COLUMN heartbeat_at TIMESTAMPTZ;
