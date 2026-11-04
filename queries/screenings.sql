-- Screening persistence (phase 5). Terminal result rows plus the
-- claim-first idempotency state machine (plan §3.3): INSERT claim →
-- pipeline → one transaction attaching the result to the claim;
-- stale claims (claimed_at ≥ 5 min, no result) are takeover-eligible
-- via the guarded delete. The 5-minute staleness bound deliberately
-- exceeds the 30 s pipeline deadline (service.Deps.Deadline) with a
-- wide margin — a live owner attaches within seconds; a 5-minute-old
-- open claim can only be a dead owner.

-- name: InsertScreeningResult :one
INSERT INTO screening_results (id, wallet_id, risk_score, risk_level,
    screening_status, scoring_policy_version, findings, provider_status,
    screened_at, request, result)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id;

-- name: GetScreeningResultByID :one
SELECT * FROM screening_results WHERE id = $1;

-- name: ListScreeningResultsByWallet :many
SELECT * FROM screening_results WHERE wallet_id = $1
ORDER BY screened_at DESC, id
LIMIT $2;

-- name: InsertScreeningRequest :one
INSERT INTO screening_requests (client_id, idempotency_key, request_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetScreeningRequestByKey :one
-- claim_stale is computed in DB time so app/DB clock skew can never
-- pick the wrong poll-vs-takeover branch.
SELECT *, claimed_at < NOW() - INTERVAL '5 minutes' AS claim_stale
FROM screening_requests
WHERE client_id = $1 AND idempotency_key = $2;

-- name: AttachScreeningResultToRequest :exec
-- Runs inside the result-insert transaction so a result row and its
-- claim pointer commit (or roll back) atomically.
UPDATE screening_requests
SET screening_result_id = $2
WHERE id = $1 AND screening_result_id IS NULL;

-- name: DeleteScreeningRequest :exec
-- Claim release on pipeline failure: an open claim whose pipeline
-- errored is deleted so a retry is not held hostage by a row that can
-- never complete. Only open (unattached) claims are deletable here.
DELETE FROM screening_requests
WHERE id = $1 AND screening_result_id IS NULL;

-- name: DeleteStaleScreeningRequest :execrows
-- Takeover path: only a still-open, still-stale claim may be deleted.
-- The claimed_at predicate re-checks staleness inside the delete so a
-- claim that completed (or was re-claimed) in the meantime is untouched.
DELETE FROM screening_requests
WHERE id = $1
  AND screening_result_id IS NULL
  AND claimed_at < NOW() - INTERVAL '5 minutes';

-- name: ListRiskCategories :many
-- Category → category_type resolution for the screening mapper; dynamic
-- upstream categories are simply absent here and land at OTHER.
SELECT code, category_type FROM risk_categories;
