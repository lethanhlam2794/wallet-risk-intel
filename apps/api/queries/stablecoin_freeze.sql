-- Stablecoin freeze intelligence (chain-scan delta source).
-- Delta semantics: absence from a window is NOT a signal — only explicit
-- unfreeze events revoke; destroy never revokes (destroyBlackFunds keeps
-- isBlackListed set). Publish order matters: ensure wallets → freeze/
-- destroy upsert → destroy evidence append → unfreeze revoke → orphan
-- unfreeze labels → cursor advance → snapshot row. One tx, service-managed.

-- ---- scan state ----

-- name: GetFreezeScanStates :many
SELECT * FROM freeze_scan_state ORDER BY contract_address;

-- name: AdvanceFreezeScanState :exec
-- GREATEST: the cursor never regresses on degenerate windows.
UPDATE freeze_scan_state
SET last_scanned_block = GREATEST(last_scanned_block, $3),
    last_head_block = $4,
    updated_at = NOW()
WHERE network_id = $1 AND contract_address = $2;

-- ---- staging ----

-- name: StgInsertFreezeEvent :execrows
-- PK (run_id, contract_address, tx_hash, log_index) makes re-staging a
-- window idempotent; execrows exposes collisions.
INSERT INTO stg_freeze_events
    (run_id, contract_address, event_type, wallet_address, tx_hash, log_index,
     block_number, block_ts, fingerprint, label, evidence)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT DO NOTHING;

-- name: StgFreezeEventCounts :many
SELECT event_type, count(*) AS n FROM stg_freeze_events
WHERE run_id = $1 GROUP BY event_type;

-- name: StgFreezeEventTotal :one
SELECT count(*) FROM stg_freeze_events WHERE run_id = $1;

-- name: StgFreezeEventMaxBlocks :many
-- Validate gate: no staged event may sit above its contract's scanned head.
SELECT contract_address, MAX(block_number)::bigint AS mx FROM stg_freeze_events
WHERE run_id = $1 GROUP BY contract_address;

-- name: StgDeleteRunFreezeEvents :exec
DELETE FROM stg_freeze_events WHERE run_id = $1;

-- name: StgPurgeFreezeEventsExceptRuns :exec
DELETE FROM stg_freeze_events WHERE run_id <> ALL($1::uuid[]);

-- ---- publish ----

-- name: PublishFreezeEnsureWallets :execrows
-- wallet_address is the normalized lowercase form the scanner extracted
-- from the log — the chain provides exactly one representation, so
-- address and normalized_address coincide. network_id resolves per
-- contract through freeze_scan_state — same join the label upsert uses.
INSERT INTO wallets (network_id, address, normalized_address, address_type)
SELECT DISTINCT fs.network_id, s.wallet_address, s.wallet_address, 'evm'
FROM stg_freeze_events s
JOIN freeze_scan_state fs ON fs.contract_address = s.contract_address
WHERE s.run_id = $1
ON CONFLICT (network_id, normalized_address) DO NOTHING;

-- name: PublishFreezeUpsertLabels :execrows
-- The latest freeze|destroy event per fingerprint wins top-level
-- evidence. DO UPDATE merges (wl.evidence || EXCLUDED.evidence) so
-- unfreeze/destroy markers survive; revoked_at=NULL revives a refrozen
-- address while first_seen_at is preserved. verification_status is
-- 'verified' — the issuer's own contract event IS the primary evidence;
-- entity stays NULL (never invent a Tether/Circle entity). $1 = source_id,
-- $2 = run_id.
INSERT INTO wallet_labels
    (wallet_id, category_id, source_id, entity_id, fingerprint, label,
     confidence, verification_status, source_url, upstream_category,
     evidence, observed_at)
SELECT w.id, rc.id, $1, NULL, s.fingerprint, s.label,
       'high', 'verified', 'https://etherscan.io/tx/' || s.tx_hash,
       'FROZEN', s.evidence, s.block_ts
FROM (
    SELECT DISTINCT ON (fingerprint) *
    FROM stg_freeze_events
    WHERE run_id = $2 AND event_type IN ('freeze', 'destroy')
    ORDER BY fingerprint, block_number DESC, log_index DESC
) s
JOIN freeze_scan_state fs ON fs.contract_address = s.contract_address
JOIN wallets w ON w.network_id = fs.network_id
            AND w.normalized_address = s.wallet_address
JOIN risk_categories rc ON rc.code = 'FROZEN'
ON CONFLICT (fingerprint) DO UPDATE SET
    evidence = wallet_labels.evidence || EXCLUDED.evidence,
    observed_at = EXCLUDED.observed_at,
    source_url = EXCLUDED.source_url,
    last_seen_at = NOW(),
    revoked_at = NULL;

-- name: PublishFreezeAppendDestroys :execrows
-- destroyBlackFunds keeps isBlackListed — the event is a terminal
-- lifecycle marker, NEVER a revocation. Appends {tx:idx → marker} under
-- destroy_events on the matching label; the keyed-object form dedupes by
-- key so a re-scanned event cannot double-append. $1 = source_id,
-- $2 = run_id.
UPDATE wallet_labels wl
SET evidence = wl.evidence || jsonb_build_object(
        'destroy_events',
        COALESCE(wl.evidence->'destroy_events', '{}'::jsonb) || s.evs)
FROM (
    SELECT fingerprint, jsonb_object_agg(k, marker) AS evs
    FROM (
        SELECT fingerprint,
               tx_hash || ':' || log_index::text AS k,
               jsonb_build_object(
                   'event', 'DestroyedBlackFunds',
                   'tx_hash', tx_hash,
                   'block_number', block_number,
                   'block_ts', block_ts) AS marker
        FROM stg_freeze_events
        WHERE run_id = $2 AND event_type = 'destroy'
    ) m
    GROUP BY fingerprint
) s
WHERE wl.fingerprint = s.fingerprint
  AND wl.source_id = $1;

-- name: PublishFreezeRevoke :execrows
-- Revocation is chronological per fingerprint: only when the LATEST
-- staged event of any type is 'unfreeze' does the matching label revoke
-- (freeze@b1+unfreeze@b2 → revoked; unfreeze@b1+freeze@b2 → stays active).
-- The revoking tx merges into evidence as unfreeze_event — the auditor
-- needs it. Absence from a window is never a revoke signal: only staged
-- unfreeze rows touch labels. $1 = source_id, $2 = run_id.
UPDATE wallet_labels wl
SET revoked_at = NOW(),
    evidence = wl.evidence || jsonb_build_object('unfreeze_event',
        jsonb_build_object('tx_hash', s.tx_hash,
                           'block_number', s.block_number,
                           'block_ts', s.block_ts))
FROM (
    SELECT DISTINCT ON (fingerprint)
           fingerprint, event_type, tx_hash, block_number, block_ts
    FROM stg_freeze_events
    WHERE run_id = $2
    ORDER BY fingerprint, block_number DESC, log_index DESC
) s
WHERE wl.fingerprint = s.fingerprint
  AND s.event_type = 'unfreeze'
  AND wl.source_id = $1;

-- name: PublishFreezeOrphanUnfreeze :execrows
-- An unfreeze for a fingerprint with no label row still proves the
-- address WAS blacklisted at that block (RemovedBlackList requires
-- _blackListed[user]); materialize an already-revoked label so the
-- evidence is preserved. Runs after the upsert/revoke steps — only
-- fingerprints with no label at all land here. $1 = source_id,
-- $2 = run_id.
INSERT INTO wallet_labels
    (wallet_id, category_id, source_id, entity_id, fingerprint, label,
     confidence, verification_status, source_url, upstream_category,
     evidence, observed_at, revoked_at)
SELECT w.id, rc.id, $1, NULL, s.fingerprint, s.label,
       'high', 'verified', 'https://etherscan.io/tx/' || s.tx_hash,
       'FROZEN', s.evidence, s.block_ts, NOW()
FROM (
    SELECT DISTINCT ON (fingerprint) *
    FROM stg_freeze_events
    WHERE run_id = $2 AND event_type = 'unfreeze'
    ORDER BY fingerprint, block_number DESC, log_index DESC
) s
JOIN freeze_scan_state fs ON fs.contract_address = s.contract_address
JOIN wallets w ON w.network_id = fs.network_id
            AND w.normalized_address = s.wallet_address
JOIN risk_categories rc ON rc.code = 'FROZEN'
WHERE NOT EXISTS (
    SELECT 1 FROM wallet_labels wl WHERE wl.fingerprint = s.fingerprint)
ON CONFLICT (fingerprint) DO NOTHING;

-- name: GetWalletLabelByFingerprint :one
-- Point lookup used by tests/audit — provenance reads stay on the read path.
SELECT * FROM wallet_labels WHERE fingerprint = $1;

-- name: UpdateRunCursorValue :exec
-- cursor_value = "contract:last_scanned" summary — observability only;
-- written inside the publish tx so the marker reflects what committed.
UPDATE ingestion_runs SET cursor_value = $2 WHERE id = $1;
