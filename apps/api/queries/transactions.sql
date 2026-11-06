-- Transaction indexing + read path (phase 6, spec §7.13, plan §3.2/§3.4).
-- Upsert identity (network_id, tx_hash, transfer_index); the DO UPDATE set
-- covers only mutable columns so tail-window refetches actually revalidate.

-- name: UpsertTransactions :exec
-- Single jsonb batch param (sqlc cannot type variadic unnest); keys map to
-- columns by name via populate_recordset — amount_raw stays a raw-integer
-- string so NUMERIC fidelity is never at risk.
INSERT INTO transactions (network_id, tx_hash, transfer_index, transfer_type,
    from_wallet_id, to_wallet_id, asset_identifier, amount_raw,
    block_number, block_timestamp, confirmation_status, is_error)
SELECT sqlc.arg(network_id)::bigint, r.tx_hash, r.transfer_index,
       r.transfer_type, r.from_wallet_id, r.to_wallet_id,
       r.asset_identifier, r.amount_raw, r.block_number,
       r.block_timestamp, r.confirmation_status, r.is_error
FROM jsonb_populate_recordset(NULL::transactions, sqlc.arg(rows)::jsonb) AS r
ON CONFLICT (network_id, tx_hash, transfer_index) DO UPDATE SET
    confirmation_status = EXCLUDED.confirmation_status,
    block_number        = EXCLUDED.block_number,
    block_timestamp     = EXCLUDED.block_timestamp,
    is_error            = EXCLUDED.is_error;

-- Bulk counterparty resolve (wallets.sql convention: the no-op DO UPDATE
-- forces RETURNING for pre-existing rows — none are omitted).
-- 'evm' literal: upstream feeds are EVM-only (buildRows NormalizeEVMs both
-- endpoints); non-EVM stamping is an enablement task
-- (docs/tron-indexer-semantics.md §f).
-- name: BulkResolveWallets :many
INSERT INTO wallets (network_id, address, normalized_address, address_type)
SELECT $1::bigint, a, a, 'evm' FROM unnest($2::text[]) AS a
ON CONFLICT (network_id, normalized_address)
DO UPDATE SET updated_at = wallets.updated_at
RETURNING id, normalized_address;

-- Indexed-transfer listing: the OR predicate over from/to wallet lets
-- Postgres bitmap both indexes; keyset pagination over
-- (block_timestamp DESC, id DESC). A W→W row is emitted exactly once with
-- direction='self'; direction=in/out filters still match it per plan §3.5.
-- name: ListTransactions :many
SELECT t.id, t.tx_hash, t.transfer_index, t.transfer_type, t.asset_identifier,
       t.amount_raw::text AS amount_raw, t.block_number, t.block_timestamp,
       t.confirmation_status, t.is_error,
       COALESCE(fw.normalized_address, '') AS from_address,
       COALESCE(tw.normalized_address, '') AS to_address,
       CASE WHEN t.from_wallet_id = $2 AND t.to_wallet_id = $2 THEN 'self'
            WHEN t.from_wallet_id = $2 THEN 'out' ELSE 'in' END AS direction
FROM transactions t
LEFT JOIN wallets fw ON fw.id = t.from_wallet_id
LEFT JOIN wallets tw ON tw.id = t.to_wallet_id
WHERE t.network_id = $1
  AND (t.from_wallet_id = $2 OR t.to_wallet_id = $2)
  AND (sqlc.narg('direction')::text IS NULL
       OR (sqlc.narg('direction') = 'self' AND t.from_wallet_id = $2
                                           AND t.to_wallet_id = $2)
       OR (sqlc.narg('direction') = 'in'  AND t.to_wallet_id = $2)
       OR (sqlc.narg('direction') = 'out' AND t.from_wallet_id = $2))
  AND (sqlc.narg('asset')::text IS NULL
       OR (sqlc.narg('asset') = 'native' AND t.transfer_type = 'NATIVE')
       OR (sqlc.narg('asset') <> 'native' AND t.asset_identifier = sqlc.narg('asset')))
  AND (sqlc.narg('from_ts')::timestamptz IS NULL
       OR t.block_timestamp >= sqlc.narg('from_ts'))
  AND (sqlc.narg('to_ts')::timestamptz IS NULL
       OR t.block_timestamp <= sqlc.narg('to_ts'))
  AND (sqlc.narg('cursor_ts')::timestamptz IS NULL
       OR (t.block_timestamp, t.id)
          < (sqlc.narg('cursor_ts'), sqlc.narg('cursor_id')::bigint))
ORDER BY t.block_timestamp DESC, t.id DESC
LIMIT sqlc.arg('lim')::bigint;

-- BFS edge scan (plan §3.3): is_error rows move nothing — no edge, no
-- counterparty. Declared scan order (block_timestamp, tx_hash,
-- transfer_index) makes mid-scan max_edges truncation deterministic.
-- name: ListWalletEdges :many
SELECT t.id, t.tx_hash, t.transfer_index, t.transfer_type, t.asset_identifier,
       t.amount_raw::text AS amount_raw, t.block_number, t.block_timestamp,
       t.confirmation_status,
       COALESCE(fw.normalized_address, '') AS from_address,
       COALESCE(tw.normalized_address, '') AS to_address
FROM transactions t
LEFT JOIN wallets fw ON fw.id = t.from_wallet_id
LEFT JOIN wallets tw ON tw.id = t.to_wallet_id
WHERE t.network_id = $1 AND NOT t.is_error
  AND (t.from_wallet_id = $2 OR t.to_wallet_id = $2)
ORDER BY t.block_timestamp, t.tx_hash, t.transfer_index
LIMIT $3;

-- name: GetWalletIndexState :one
SELECT * FROM wallet_index_state
WHERE network_id = $1 AND wallet_id = $2 AND source_id = $3;

-- Claim marker: status='indexing' preserves watermark/counters; updated_at
-- is the claim timestamp the stale-claim horizon (IndexingStaleAfter)
-- reads; error is cleared so a live claim never echoes a stale ErrKind.
-- name: MarkWalletIndexing :exec
INSERT INTO wallet_index_state (network_id, wallet_id, source_id, status)
VALUES ($1, $2, $3, 'indexing')
ON CONFLICT (network_id, wallet_id, source_id) DO UPDATE SET
    status = 'indexing', error = NULL,
    updated_at = NOW();

-- Terminal write: status/error/indexed_at written in one upsert —
-- indexed_at is simultaneously the freshness, FAILED-cooldown, and stale
-- clock. rows_* deltas accumulate per source (phase-12 re-key: each
-- adapter owns an independent coverage row).
-- name: UpsertWalletIndexState :one
INSERT INTO wallet_index_state (network_id, wallet_id, source_id,
    indexed_until_block, indexed_at, status, error,
    rows_indexed, rows_skipped_missing_contract, rows_skipped_invalid_address,
    history_truncated)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (network_id, wallet_id, source_id) DO UPDATE SET
    indexed_until_block = EXCLUDED.indexed_until_block,
    indexed_at = EXCLUDED.indexed_at,
    status = EXCLUDED.status,
    error = EXCLUDED.error,
    rows_indexed =
        wallet_index_state.rows_indexed + EXCLUDED.rows_indexed,
    rows_skipped_missing_contract =
        wallet_index_state.rows_skipped_missing_contract
        + EXCLUDED.rows_skipped_missing_contract,
    rows_skipped_invalid_address =
        wallet_index_state.rows_skipped_invalid_address
        + EXCLUDED.rows_skipped_invalid_address,
    history_truncated = EXCLUDED.history_truncated,
    updated_at = NOW()
RETURNING *;

-- Bulk label match for counterparties (plan §3.4): one join over the full
-- counterparty set — the only existing reads were per-address/LIMIT 5.
-- name: BulkMatchLabels :many
SELECT w.normalized_address, wl.label, wl.verification_status, wl.confidence,
       rc.code AS category_code, rc.category_type,
       e.id AS entity_id, e.name AS entity_name,
       ds.code AS source_code
FROM wallet_labels wl
JOIN wallets w ON w.id = wl.wallet_id
JOIN risk_categories rc ON rc.id = wl.category_id
JOIN data_sources ds ON ds.id = wl.source_id
LEFT JOIN entities e ON e.id = wl.entity_id
WHERE w.network_id = $1 AND w.normalized_address = ANY($2::text[])
  AND wl.revoked_at IS NULL;

-- Cross-network label match (phase 7): foreign-network wallet_labels rows
-- for the same normalized address. listed_address carries the foreign
-- wallets.address ORIGINAL representation (never the normalized form);
-- an empty network-id set matches zero rows.
-- name: BulkMatchLabelsCrossNet :many
SELECT w.normalized_address, wl.label, wl.verification_status, wl.confidence,
       rc.code AS category_code, rc.category_type,
       e.id AS entity_id, e.name AS entity_name,
       ds.code AS source_code,
       n.code AS network_code, w.address AS listed_address
FROM wallet_labels wl
JOIN wallets w ON w.id = wl.wallet_id
JOIN blockchain_networks n ON n.id = w.network_id
JOIN risk_categories rc ON rc.id = wl.category_id
JOIN data_sources ds ON ds.id = wl.source_id
LEFT JOIN entities e ON e.id = wl.entity_id
WHERE w.normalized_address = ANY(sqlc.arg('addrs')::text[])
  AND w.network_id = ANY(sqlc.arg('network_ids')::bigint[])
  AND wl.revoked_at IS NULL;
