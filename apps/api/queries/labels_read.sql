-- Label read path (spec §9.8). The full per-wallet set is fetched once and
-- filtered/paginated in the service layer so one cache entry serves every
-- filter combination (revoked rows included — active_only is a read filter).

-- name: ListAllWalletLabels :many
SELECT wl.id AS wallet_label_id, wl.label, wl.confidence, wl.verification_status,
       wl.source_url, wl.upstream_category, wl.evidence, wl.observed_at,
       wl.first_seen_at, wl.last_seen_at, wl.revoked_at,
       rc.code AS category_code, rc.category_type,
       e.id AS entity_id, e.name AS entity_name,
       ds.code AS source_code, ds.name AS source_name
FROM wallet_labels wl
JOIN wallets w ON w.id = wl.wallet_id
JOIN risk_categories rc ON rc.id = wl.category_id
JOIN data_sources ds ON ds.id = wl.source_id
LEFT JOIN entities e ON e.id = wl.entity_id
WHERE w.normalized_address = $1 AND w.network_id = $2
ORDER BY wl.id;

-- name: LatestLabelSnapshotPublished :one
-- Latest publish across EVERY label-producing source — wallet-label and
-- screening caches embed this version, so a publish from either
-- wallet_attribution (full replace) or stablecoin_freeze (delta) must
-- invalidate them.
SELECT * FROM data_source_snapshots
WHERE source_id IN (
    SELECT id FROM data_sources
    WHERE code IN ('wallet_attribution', 'stablecoin_freeze'))
ORDER BY published_at DESC, id DESC
LIMIT 1;

-- name: ListWalletLabelsSummary :many
SELECT wl.id AS wallet_label_id, wl.label, wl.confidence, wl.verification_status,
       rc.code AS category_code, rc.category_type,
       e.id AS entity_id, e.name AS entity_name
FROM wallet_labels wl
JOIN risk_categories rc ON rc.id = wl.category_id
LEFT JOIN entities e ON e.id = wl.entity_id
WHERE wl.wallet_id = $1 AND wl.revoked_at IS NULL
ORDER BY wl.id
LIMIT 5;

-- name: CountActiveWalletLabels :one
SELECT count(*) FROM wallet_labels
WHERE wallet_id = $1 AND revoked_at IS NULL;

-- name: CountActiveLabels :one
SELECT count(*) FROM wallet_labels WHERE revoked_at IS NULL;

-- name: MaxLabelObservedAt :one
SELECT MAX(observed_at)::timestamptz AS mx FROM wallet_labels WHERE source_id = $1;
