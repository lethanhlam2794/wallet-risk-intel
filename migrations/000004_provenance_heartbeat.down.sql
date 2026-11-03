ALTER TABLE ingestion_runs DROP COLUMN heartbeat_at;

DROP INDEX idx_sanctions_references_entity;
DROP INDEX uq_sanctions_reference_identity;

ALTER TABLE sanctions_references
    DROP COLUMN revoked_at,
    DROP COLUMN last_seen_at,
    DROP COLUMN first_seen_at;

ALTER TABLE sanctions_entity_programs
    DROP COLUMN revoked_at;
