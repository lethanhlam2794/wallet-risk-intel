-- Migration 000005 down: drop the labels schema and its seeds.

DELETE FROM ingestion_runs
WHERE source_id = (SELECT id FROM data_sources WHERE code = 'wallet_attribution');

DROP TABLE IF EXISTS stg_wallet_labels;
DROP TABLE IF EXISTS wallet_labels;
DROP TABLE IF EXISTS data_source_snapshots;

DELETE FROM data_sources WHERE code = 'wallet_attribution';

DROP INDEX IF EXISTS uq_entities_name_ci;

DELETE FROM risk_categories;
DROP TABLE IF EXISTS risk_categories;
