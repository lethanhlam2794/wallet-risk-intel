-- name: InsertProviderObservation :one
INSERT INTO provider_observations
    (source_id, wallet_id, requested_chain, status, http_status,
     latency_ms, payload, provider_score, error, fetched_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: ListLatestProviderObservations :many
SELECT * FROM provider_observations
WHERE source_id = $1 AND wallet_id = $2
ORDER BY fetched_at DESC, id DESC
LIMIT $3;

-- name: PruneProviderObservations :exec
-- Retention bound: keep the newest N rows per (source, wallet). Called
-- inside the insert transaction so the bound holds atomically; a burst
-- across processes can transiently keep N+1 — the next insert self-heals.
DELETE FROM provider_observations
WHERE provider_observations.source_id = $1 AND provider_observations.wallet_id = $2
  AND provider_observations.id NOT IN (
      SELECT p.id FROM provider_observations p
      WHERE p.source_id = $1 AND p.wallet_id = $2
      ORDER BY p.fetched_at DESC, p.id DESC
      LIMIT $3
  );
