-- name: GetSourceByCode :one
SELECT * FROM data_sources WHERE code = $1;

-- name: InsertIngestionRun :one
INSERT INTO ingestion_runs (id, source_id, status, triggered_by)
VALUES ($1, $2, 'RUNNING', $3)
RETURNING *;

-- name: FinishIngestionRun :exec
UPDATE ingestion_runs SET
    status = $2,
    completed_at = NOW(),
    records_fetched = $3,
    records_inserted = $4,
    records_updated = $5,
    records_rejected = $6,
    snapshot_checksum = $7,
    snapshot_version = $8,
    error_message = $9
WHERE id = $1;

-- name: GetIngestionRun :one
SELECT * FROM ingestion_runs WHERE id = $1;

-- name: ListStaleRunningRuns :many
SELECT * FROM ingestion_runs
WHERE status = 'RUNNING' AND started_at < NOW() - INTERVAL '1 hour';

-- name: MarkStaleRunsFailed :exec
UPDATE ingestion_runs SET status = 'FAILED', completed_at = NOW(),
    error_message = 'worker restarted'
WHERE status = 'RUNNING' AND started_at < NOW() - INTERVAL '1 hour';

-- name: ListRecentRunIDs :many
SELECT id FROM ingestion_runs ORDER BY started_at DESC LIMIT $1;

-- name: GetLatestSnapshot :one
SELECT * FROM sanctions_snapshots
WHERE source_id = $1
ORDER BY published_at DESC, id DESC
LIMIT 1;
