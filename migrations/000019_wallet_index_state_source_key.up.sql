-- Migration 000019: wallet_index_state re-key to (network_id, wallet_id,
-- source_id) — per-source watermarks for the ordered failover chain
-- (phase-12 design D1). Rows without provenance are already distrusted
-- by the old reset guard and are dropped; source_id becomes NOT NULL.
DELETE FROM wallet_index_state WHERE source_id IS NULL;
ALTER TABLE wallet_index_state DROP CONSTRAINT wallet_index_state_pkey;
ALTER TABLE wallet_index_state ALTER COLUMN source_id SET NOT NULL;
ALTER TABLE wallet_index_state
    ADD PRIMARY KEY (network_id, wallet_id, source_id);
