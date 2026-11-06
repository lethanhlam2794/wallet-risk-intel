-- Warm seed (warm-seed-worker): keyset-paginated scans feeding the
-- one-shot `warmseed` worker mode. Each source streams bounded batches
-- (ORDER BY + cursor + LIMIT) — never a whole-table read. The LEFT JOIN
-- to blockchain_networks is deliberate: rows whose network_id is NULL,
-- inactive, or non-EVM must stay visible so the seeder can count them
-- under skipped_inactive_net instead of silently dropping them.

-- Source 1 (priority 20, seed:ofac_list): live OFAC-listed wallets.
-- revoked_at rows are dead evidence and excluded in SQL.
-- name: ListWarmSeedSanctionsBatch :many
SELECT sw.id, sw.network_id, sw.normalized_address,
       bn.is_active AS net_active, bn.namespace AS net_namespace
FROM sanctions_wallets sw
LEFT JOIN blockchain_networks bn ON bn.id = sw.network_id
WHERE sw.revoked_at IS NULL
  AND sw.id > sqlc.narg('cursor_id')::bigint
ORDER BY sw.id
LIMIT sqlc.arg('lim')::bigint;

-- Source 2 (priority 10, seed:risky_label): wallets carrying a risky
-- label. The category allowlist mirrors tracing.riskyWarmCategories —
-- GAMBLING/EXCHANGE are classifications, not crimes, and never scan.
-- name: ListWarmSeedLabelBatch :many
SELECT wl.id, w.network_id, w.normalized_address,
       bn.is_active AS net_active, bn.namespace AS net_namespace
FROM wallet_labels wl
JOIN wallets w ON w.id = wl.wallet_id
JOIN risk_categories rc ON rc.id = wl.category_id
LEFT JOIN blockchain_networks bn ON bn.id = w.network_id
WHERE wl.revoked_at IS NULL
  AND rc.code IN ('SCAM','PHISHING','HACK','RANSOMWARE','MIXER')
  AND wl.id > sqlc.narg('cursor_id')::bigint
ORDER BY wl.id
LIMIT sqlc.arg('lim')::bigint;

-- Source 3 (priority 5, seed:refresh_stale): per-source index coverage.
-- ALL coverage rows are returned (no status predicate): the classifier
-- distinguishes stale (partial/failed/history_truncated) from adequate
-- coverage (skipped_fresh) — an honest skipped_fresh counter requires
-- seeing the adequate rows too. Composite keyset over the
-- (network_id, wallet_id, source_id) PK matches the row-value cursor
-- convention from queries/transactions.sql.
-- name: ListWarmSeedIndexStateBatch :many
SELECT wis.network_id, wis.wallet_id, wis.source_id, wis.status,
       wis.history_truncated, w.normalized_address,
       bn.is_active AS net_active, bn.namespace AS net_namespace
FROM wallet_index_state wis
JOIN wallets w ON w.id = wis.wallet_id
LEFT JOIN blockchain_networks bn ON bn.id = wis.network_id
WHERE (wis.network_id, wis.wallet_id, wis.source_id)
      > (sqlc.narg('cursor_net')::bigint,
         sqlc.narg('cursor_wallet')::bigint,
         sqlc.narg('cursor_source')::bigint)
ORDER BY wis.network_id, wis.wallet_id, wis.source_id
LIMIT sqlc.arg('lim')::bigint;
