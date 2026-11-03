DROP INDEX IF EXISTS uq_ingestion_runs_running;
ALTER TABLE ingestion_runs DROP COLUMN IF EXISTS triggered_by;
DROP TABLE IF EXISTS sanctions_snapshots;
