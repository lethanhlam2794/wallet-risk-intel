-- name: StgInsertEntity :exec
INSERT INTO stg_sanctions_entities (run_id, external_entity_id, primary_name, entity_type, source_url, details)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT DO NOTHING;

-- name: StgInsertWallet :exec
INSERT INTO stg_sanctions_wallets
    (run_id, external_entity_id, listed_address, normalized_address,
     currency_code, network_id, identifier_type, source_identifier_id,
     match_scope, evidence)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT DO NOTHING;

-- name: StgInsertProgram :exec
INSERT INTO stg_sanctions_programs (run_id, code, name, description, source_url)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (run_id, code) DO NOTHING;

-- name: StgInsertEntityProgram :exec
INSERT INTO stg_sanctions_entity_programs (run_id, external_entity_id, program_code)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: StgInsertReference :exec
INSERT INTO stg_sanctions_references
    (run_id, external_entity_id, reference_type, title, reference_url, published_at, details)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT DO NOTHING;

-- name: StgCountByRun :many
SELECT 'entities' AS table_name, count(*) AS n FROM stg_sanctions_entities WHERE stg_sanctions_entities.run_id = $1
UNION ALL
SELECT 'wallets', count(*) FROM stg_sanctions_wallets WHERE stg_sanctions_wallets.run_id = $1
UNION ALL
SELECT 'programs', count(*) FROM stg_sanctions_programs WHERE stg_sanctions_programs.run_id = $1
UNION ALL
SELECT 'entity_programs', count(*) FROM stg_sanctions_entity_programs WHERE stg_sanctions_entity_programs.run_id = $1
UNION ALL
SELECT 'references', count(*) FROM stg_sanctions_references WHERE stg_sanctions_references.run_id = $1;

-- name: StgOrphanWallets :one
SELECT count(*) FROM stg_sanctions_wallets w
WHERE w.run_id = $1 AND NOT EXISTS (
    SELECT 1 FROM stg_sanctions_entities e
    WHERE e.run_id = w.run_id AND e.external_entity_id = w.external_entity_id
);

-- name: StgOrphanEntityPrograms :one
SELECT count(*) FROM stg_sanctions_entity_programs ep
WHERE ep.run_id = $1 AND (
    NOT EXISTS (
        SELECT 1 FROM stg_sanctions_programs p
        WHERE p.run_id = ep.run_id AND p.code = ep.program_code
    ) OR NOT EXISTS (
        SELECT 1 FROM stg_sanctions_entities e
        WHERE e.run_id = ep.run_id AND e.external_entity_id = ep.external_entity_id
    )
);

-- name: StgOrphanReferences :one
SELECT count(*) FROM stg_sanctions_references r
WHERE r.run_id = $1 AND NOT EXISTS (
    SELECT 1 FROM stg_sanctions_entities e
    WHERE e.run_id = r.run_id AND e.external_entity_id = r.external_entity_id
);

-- name: StgDeleteRunEntities :exec
DELETE FROM stg_sanctions_entities WHERE run_id = $1;

-- name: StgDeleteRunWallets :exec
DELETE FROM stg_sanctions_wallets WHERE run_id = $1;

-- name: StgDeleteRunPrograms :exec
DELETE FROM stg_sanctions_programs WHERE run_id = $1;

-- name: StgDeleteRunEntityPrograms :exec
DELETE FROM stg_sanctions_entity_programs WHERE run_id = $1;

-- name: StgDeleteRunReferences :exec
DELETE FROM stg_sanctions_references WHERE run_id = $1;

-- name: StgPurgeEntitiesExceptRuns :exec
DELETE FROM stg_sanctions_entities WHERE run_id <> ALL($1::uuid[]);

-- name: StgPurgeWalletsExceptRuns :exec
DELETE FROM stg_sanctions_wallets WHERE run_id <> ALL($1::uuid[]);

-- name: StgPurgeProgramsExceptRuns :exec
DELETE FROM stg_sanctions_programs WHERE run_id <> ALL($1::uuid[]);

-- name: StgPurgeEntityProgramsExceptRuns :exec
DELETE FROM stg_sanctions_entity_programs WHERE run_id <> ALL($1::uuid[]);

-- name: StgPurgeReferencesExceptRuns :exec
DELETE FROM stg_sanctions_references WHERE run_id <> ALL($1::uuid[]);
