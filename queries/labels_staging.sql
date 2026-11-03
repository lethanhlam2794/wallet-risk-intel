-- Label staging (phase 03): run-scoped rows; PK (run_id, fingerprint) makes
-- staging inserts idempotent; execrows exposes fingerprint collisions.

-- name: StgInsertWalletLabel :execrows
INSERT INTO stg_wallet_labels
    (run_id, network_code, address, entity_name, label, upstream_category,
     source_field, source_url, confidence, last_updated, fingerprint, evidence)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT DO NOTHING;

-- name: StgLabelCountsByNetwork :many
SELECT s.network_code, count(*) AS n FROM stg_wallet_labels s
WHERE s.run_id = $1
GROUP BY s.network_code;

-- name: StgLabelOrphanNetworks :one
SELECT count(*) FROM stg_wallet_labels s
WHERE s.run_id = $1
  AND NOT EXISTS (
      SELECT 1 FROM blockchain_networks bn WHERE bn.code = s.network_code
  );

-- name: StgLabelNewCategories :many
SELECT DISTINCT UPPER(s.upstream_category) AS code
FROM stg_wallet_labels s
WHERE s.run_id = $1
  AND NOT EXISTS (
      SELECT 1 FROM risk_categories rc
      WHERE rc.code = UPPER(s.upstream_category)
  );

-- name: StgLabelMaxLastUpdated :one
SELECT MAX(s.last_updated)::text AS mx FROM stg_wallet_labels s
WHERE s.run_id = $1;

-- name: CountLiveLabelsForStagedNetworks :many
SELECT bn.code AS network_code, count(*) AS n
FROM wallet_labels wl
JOIN wallets w ON w.id = wl.wallet_id
JOIN blockchain_networks bn ON bn.id = w.network_id
WHERE wl.source_id = $1
  AND wl.revoked_at IS NULL
  AND w.network_id IN (
      SELECT bn2.id FROM blockchain_networks bn2
      WHERE bn2.code IN (
          SELECT DISTINCT s.network_code FROM stg_wallet_labels s
          WHERE s.run_id = $2))
GROUP BY bn.code;

-- name: StgDeleteRunWalletLabels :exec
DELETE FROM stg_wallet_labels WHERE run_id = $1;

-- name: StgPurgeWalletLabelsExceptRuns :exec
DELETE FROM stg_wallet_labels WHERE run_id <> ALL($1::uuid[]);
