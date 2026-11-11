-- Sanctions read path (phase 05). All match semantics are explicit:
--   direct    : same normalized_address AND same network_id AND not revoked
--   crosschain: same normalized_address, OTHER evidenced network (non-NULL),
--               not revoked — observation only, never a direct match
--   unresolved: same normalized_address, network_id IS NULL, not revoked —
--               surfaced so a listed address can never read as "no data"

-- name: MatchSanctionsWallets :many
SELECT sw.id AS sanctions_wallet_id, sw.listed_address, sw.currency_code,
       sw.identifier_type, sw.match_scope, sw.evidence,
       se.id AS sanctions_entity_id, se.external_entity_id,
       se.primary_name, se.entity_type,
       src.code AS source_code, src.name AS source_name
FROM sanctions_wallets sw
JOIN sanctions_entities se ON se.id = sw.sanctions_entity_id AND se.revoked_at IS NULL
JOIN data_sources src ON src.id = se.source_id
WHERE sw.normalized_address = $1
  AND sw.network_id = $2
  AND sw.revoked_at IS NULL;

-- name: CrossChainSanctionsWallets :many
SELECT sw.id AS sanctions_wallet_id, sw.listed_address, sw.currency_code,
       sw.identifier_type, sw.match_scope, sw.evidence,
       se.id AS sanctions_entity_id, se.external_entity_id,
       se.primary_name, se.entity_type,
       src.code AS source_code, src.name AS source_name,
       sw.network_id
FROM sanctions_wallets sw
JOIN sanctions_entities se ON se.id = sw.sanctions_entity_id AND se.revoked_at IS NULL
JOIN data_sources src ON src.id = se.source_id
WHERE sw.normalized_address = $1
  AND sw.network_id IS NOT NULL
  AND sw.network_id <> $2
  AND sw.revoked_at IS NULL;

-- name: UnresolvedNetworkListings :many
SELECT sw.id AS sanctions_wallet_id, sw.listed_address, sw.currency_code,
       sw.identifier_type, sw.match_scope, sw.evidence,
       se.id AS sanctions_entity_id, se.external_entity_id,
       se.primary_name, se.entity_type,
       src.code AS source_code, src.name AS source_name
FROM sanctions_wallets sw
JOIN sanctions_entities se ON se.id = sw.sanctions_entity_id AND se.revoked_at IS NULL
JOIN data_sources src ON src.id = se.source_id
WHERE sw.normalized_address = $1
  AND sw.network_id IS NULL
  AND sw.revoked_at IS NULL;

-- Bulk direct-match read (v1.1 phase-02): active sanctions listings over
-- a normalized-address set on one network — same semantics as
-- MatchSanctionsWallets (live entity + live listing, same network).
-- name: BulkMatchSanctionedAddresses :many
SELECT sw.normalized_address, se.id AS sanctions_entity_id,
       se.external_entity_id, se.primary_name, se.entity_type,
       src.code AS source_code
FROM sanctions_wallets sw
JOIN sanctions_entities se ON se.id = sw.sanctions_entity_id
    AND se.revoked_at IS NULL
JOIN data_sources src ON src.id = se.source_id
WHERE sw.network_id = sqlc.arg('network_id')::bigint
  AND sw.normalized_address = ANY(sqlc.arg('addresses')::text[])
  AND sw.revoked_at IS NULL;

-- name: GetSanctionsEntityByID :one
SELECT se.*, src.code AS source_code, src.name AS source_name, src.homepage_url AS source_homepage
FROM sanctions_entities se
JOIN data_sources src ON src.id = se.source_id
WHERE se.id = $1;

-- name: ListEntityPrograms :many
-- Returns all links including revoked ones so an entity detail shows its
-- last-known program context (revoked_at flag is surfaced to the caller).
SELECT sp.*, sep.revoked_at AS link_revoked_at FROM sanctions_programs sp
JOIN sanctions_entity_programs sep ON sep.program_id = sp.id
WHERE sep.sanctions_entity_id = $1
ORDER BY sp.code;

-- name: ListProgramsForEntities :many
-- Live associations only — a revoked link must not surface on a live lookup.
SELECT sep.sanctions_entity_id, sp.* FROM sanctions_programs sp
JOIN sanctions_entity_programs sep ON sep.program_id = sp.id
WHERE sep.sanctions_entity_id = ANY($1::bigint[])
  AND sep.revoked_at IS NULL
ORDER BY sp.code;

-- name: ListEntityWallets :many
SELECT * FROM sanctions_wallets
WHERE sanctions_entity_id = $1 AND revoked_at IS NULL
ORDER BY id;

-- name: ListEntityReferences :many
-- All rows including revoked: the entity detail is the audit surface —
-- "which official reference justified this listing" stays answerable for
-- delisted entities (spec §6 provenance).
SELECT * FROM sanctions_references
WHERE sanctions_entity_id = $1
ORDER BY published_at DESC NULLS LAST, id;

-- name: GetProgramByCode :one
SELECT sp.*, src.code AS source_code, src.name AS source_name
FROM sanctions_programs sp
JOIN data_sources src ON src.id = sp.source_id
WHERE sp.code = $1;

-- name: CountProgramEntities :one
SELECT count(*) FROM sanctions_entity_programs sep
JOIN sanctions_entities se ON se.id = sep.sanctions_entity_id AND se.revoked_at IS NULL
WHERE sep.program_id = $1 AND sep.revoked_at IS NULL;

-- name: LatestSnapshotPublished :one
SELECT * FROM sanctions_snapshots
WHERE source_id = (SELECT id FROM data_sources WHERE code = 'ofac_sdn')
ORDER BY published_at DESC, id DESC
LIMIT 1;
