-- Migration 000024 down: drop the freeze-intel schema and its rows.
-- FK order (000005 precedent): runs → snapshots → labels → tables →
-- source row. wallet_labels itself is shared — never dropped.

DELETE FROM ingestion_runs
WHERE source_id = (SELECT id FROM data_sources WHERE code = 'stablecoin_freeze');

DELETE FROM data_source_snapshots
WHERE source_id = (SELECT id FROM data_sources WHERE code = 'stablecoin_freeze');

DELETE FROM wallet_labels
WHERE source_id = (SELECT id FROM data_sources WHERE code = 'stablecoin_freeze');

DROP TABLE IF EXISTS stg_freeze_events;
DROP TABLE IF EXISTS freeze_scan_state;

DELETE FROM data_sources WHERE code = 'stablecoin_freeze';
