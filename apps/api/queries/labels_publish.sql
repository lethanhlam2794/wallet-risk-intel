-- Atomic labels publish: staged run → live tables in ONE transaction.
-- $1 = source_id, $2 = run_id. Every step is set-based; no interpolated SQL.

-- 'evm' literal: all staged rows are pre-validated EVM addresses — see
-- normalized-address comment below; non-EVM stamping is an enablement task
-- (docs/tron-indexer-semantics.md §f).
-- name: PublishEnsureLabelWallets :execrows
-- wallets.address keeps the CSV spelling verbatim (spec §7.3); the
-- normalized form is derived identically to wallet.NormalizeEVM (all staged
-- rows are pre-validated EVM addresses).
INSERT INTO wallets (network_id, address, normalized_address, address_type)
SELECT DISTINCT ON (net_id, norm) net_id, addr, norm, 'evm'
FROM (
    SELECT bn.id AS net_id, s.address AS addr,
           CASE WHEN s.address ILIKE '0x%' THEN lower(s.address)
                ELSE '0x' || lower(s.address) END AS norm
    FROM stg_wallet_labels s
    JOIN blockchain_networks bn ON bn.code = s.network_code AND bn.is_active
    WHERE s.run_id = $1
) t
ORDER BY net_id, norm, addr
ON CONFLICT (network_id, normalized_address) DO NOTHING;

-- name: PublishEnsureRiskCategories :execrows
-- Unseen upstream categories land as dynamic rows with category_type='OTHER'
-- (count + warn upstream so a human can promote them). Seeded codes conflict.
INSERT INTO risk_categories (code, name, category_type)
SELECT DISTINCT UPPER(s.upstream_category), UPPER(s.upstream_category), 'OTHER'
FROM stg_wallet_labels s
WHERE s.run_id = $1
ON CONFLICT (code) DO NOTHING;

-- name: PublishEnsureLabelEntities :execrows
-- Shared registry dedup on canonical whitespace + case (uq_entities_name_ci);
-- entity_type is first-write-wins — a label import never downgrades an
-- existing non-NULL type.
INSERT INTO entities (name)
SELECT MIN(TRIM(regexp_replace(s.entity_name, '\s+', ' ', 'g')))
FROM stg_wallet_labels s
WHERE s.run_id = $1
  AND s.entity_name IS NOT NULL
  AND TRIM(s.entity_name) <> ''
GROUP BY LOWER(TRIM(regexp_replace(s.entity_name, '\s+', ' ', 'g')))
ON CONFLICT (LOWER(TRIM(regexp_replace(name, '\s+', ' ', 'g'))))
DO UPDATE SET entity_type = COALESCE(entities.entity_type, EXCLUDED.entity_type);

-- name: PublishUpsertWalletLabels :execrows
-- verification_status is always 'reported' — community/provider labels are
-- never auto-verified. observed_at comes from the mapper-parsed evidence
-- field (raw last_updated stays in stg.last_updated for forensics); absent
-- or unparseable dates stay NULL — never invented.
INSERT INTO wallet_labels
    (wallet_id, category_id, source_id, entity_id, fingerprint, label,
     confidence, verification_status, source_url, upstream_category,
     evidence, observed_at)
SELECT w.id, rc.id, $1, e.id, s.fingerprint, s.label,
       CASE WHEN s.confidence IN ('high','medium','low') THEN s.confidence
            ELSE 'unknown' END,
       'reported',
       s.source_url, s.upstream_category, s.evidence,
       NULLIF(s.evidence->>'observed_at', '')::timestamptz
FROM stg_wallet_labels s
JOIN blockchain_networks bn ON bn.code = s.network_code AND bn.is_active
JOIN wallets w
  ON w.network_id = bn.id
 AND w.normalized_address = CASE WHEN s.address ILIKE '0x%'
                                  THEN lower(s.address)
                                  ELSE '0x' || lower(s.address) END
JOIN risk_categories rc ON rc.code = UPPER(s.upstream_category)
LEFT JOIN entities e
  ON s.entity_name IS NOT NULL
 AND TRIM(s.entity_name) <> ''
 AND LOWER(TRIM(regexp_replace(e.name, '\s+', ' ', 'g')))
     = LOWER(TRIM(regexp_replace(s.entity_name, '\s+', ' ', 'g')))
WHERE s.run_id = $2
ON CONFLICT (fingerprint) DO UPDATE SET
    entity_id = EXCLUDED.entity_id,
    confidence = EXCLUDED.confidence,
    source_url = EXCLUDED.source_url,
    evidence = EXCLUDED.evidence,
    observed_at = EXCLUDED.observed_at,
    last_seen_at = NOW(),
    revoked_at = NULL;

-- name: PublishRevokeAbsentWalletLabels :execrows
-- F3: soft-revoke is NETWORK-SCOPED — the staged-network set bounds
-- revocation, so a staged set covering only eth can never revoke bsc labels,
-- and a single-CSV file run revokes only within its own network.
--
-- "Absent from the staged set" is expressed as last_seen_at < NOW() rather
-- than a fingerprint anti-join: NOW() is transaction-stable and the upsert
-- above just stamped every staged label last_seen_at = NOW(), so any live
-- row still carrying an older stamp was not refreshed by this publish.
-- (Staging tables carry no planner statistics — a correlated anti-join on
-- them degenerates into a per-row rescan at 100k+ rows.)
UPDATE wallet_labels wl SET revoked_at = NOW()
WHERE wl.source_id = $1
  AND wl.revoked_at IS NULL
  AND wl.last_seen_at < NOW()
  AND wl.wallet_id IN (
      SELECT w.id FROM wallets w
      WHERE w.network_id IN (
          SELECT bn.id FROM blockchain_networks bn
          WHERE bn.is_active AND bn.code IN (
              SELECT DISTINCT s2.network_code FROM stg_wallet_labels s2
              WHERE s2.run_id = $2)));

-- name: InsertDataSourceSnapshot :one
INSERT INTO data_source_snapshots (source_id, version, checksum, published_at, row_count)
VALUES ($1, $2, $3, NOW(), $4)
RETURNING *;

-- name: GetLatestDataSourceSnapshot :one
SELECT * FROM data_source_snapshots
WHERE source_id = $1
ORDER BY published_at DESC, id DESC
LIMIT 1;
