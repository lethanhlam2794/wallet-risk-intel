-- Down: collapse back to one row per (network_id, wallet_id) — keep the
-- freshest row per pair (updated_at, then source_id tiebreak), then
-- restore the pre-failover key. Per-source history is lost on downgrade.
ALTER TABLE wallet_index_state DROP CONSTRAINT wallet_index_state_pkey;
DELETE FROM wallet_index_state a
    USING wallet_index_state b
    WHERE a.network_id = b.network_id AND a.wallet_id = b.wallet_id
      AND (a.updated_at < b.updated_at
           OR (a.updated_at = b.updated_at AND a.source_id < b.source_id));
ALTER TABLE wallet_index_state ALTER COLUMN source_id DROP NOT NULL;
ALTER TABLE wallet_index_state
    ADD PRIMARY KEY (network_id, wallet_id);
