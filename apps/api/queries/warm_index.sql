-- Warm index queue (warm-index-queue v1): SKIP LOCKED claim, lease reset,
-- idempotent enqueue upsert, and the per-provider daily call ledger.
-- All statements are named sqlc queries — no interpolated SQL anywhere.

-- Enqueue dedupe (plan D4): the partial unique index covers only
-- pending/claimed rows, so the ON CONFLICT arbiter predicate must repeat
-- it verbatim. Priority is monotone — a higher-priority signal raises the
-- row and owns the reason; equal/lower priorities keep the first reason.
-- name: EnqueueWarmItem :one
INSERT INTO warm_queue (network_id, address, priority, reason,
    source_screening_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (network_id, address) WHERE status IN ('pending','claimed')
DO UPDATE SET
    priority = GREATEST(warm_queue.priority, EXCLUDED.priority),
    reason = CASE WHEN EXCLUDED.priority > warm_queue.priority
                  THEN EXCLUDED.reason ELSE warm_queue.reason END,
    updated_at = NOW()
RETURNING id;

-- Lease sweep (plan D10): a reset claim COUNTS as a failed attempt — a
-- poison pill (crash/OOM mid-drain) reaches 'dead' in MaxAttempts strikes
-- instead of reclaiming forever. Graceful release uses ReleaseWarmClaim
-- which never bumps.
-- name: ResetExpiredWarmClaims :execrows
UPDATE warm_queue SET
    attempts = attempts + 1,
    claimed_at = NULL,
    updated_at = NOW(),
    status = CASE WHEN attempts + 1 >= $2 THEN 'dead' ELSE 'pending' END
WHERE status = 'claimed' AND claimed_at < $1;

-- Atomic single-item claim (plan D2): highest priority first, id as the
-- stable tiebreak; SKIP LOCKED makes concurrent drainers disjoint.
-- name: ClaimNextWarmItem :one
UPDATE warm_queue SET
    status = 'claimed', claimed_at = NOW(), updated_at = NOW()
WHERE id = (SELECT id FROM warm_queue
            WHERE status = 'pending'
            ORDER BY priority DESC, id
            LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING *;

-- Outcome writes are claim-gated (AND status='claimed'): if a lease ever
-- outlives an in-flight drain, the sweep's reset can never be clobbered
-- back by a stale settler.
-- name: CompleteWarmItem :exec
UPDATE warm_queue SET status = 'done', updated_at = NOW()
WHERE id = $1 AND status = 'claimed';

-- Attempted-drain failure: bumps attempts, releases the claim; the row
-- dies on the third strike. Ledger writes stay separate (attempted calls
-- are already spent regardless of the outcome write).
-- name: FailWarmItem :exec
UPDATE warm_queue SET
    attempts = attempts + 1,
    claimed_at = NULL,
    updated_at = NOW(),
    status = CASE WHEN attempts + 1 >= $2 THEN 'dead' ELSE 'pending' END
WHERE id = $1 AND status = 'claimed';

-- Eligibility release (plan D2): no lane selected / no upstream call was
-- attempted → back to pending WITHOUT an attempt bump.
-- name: ReleaseWarmClaim :exec
UPDATE warm_queue SET
    status = 'pending', claimed_at = NULL, updated_at = NOW()
WHERE id = $1 AND status = 'claimed';

-- Permanently unprocessable item (e.g. network_id no longer resolvable):
-- straight to dead, not a retryable failure.
-- name: KillWarmItem :exec
UPDATE warm_queue SET
    status = 'dead', claimed_at = NULL, updated_at = NOW()
WHERE id = $1 AND status = 'claimed';

-- name: GetWarmQuotaCalls :one
SELECT calls FROM warm_quota_ledger
WHERE provider_code = $1 AND period_start = $2;

-- name: AddWarmQuotaCalls :exec
INSERT INTO warm_quota_ledger (provider_code, period_start, calls)
VALUES ($1, $2, $3)
ON CONFLICT (provider_code, period_start)
DO UPDATE SET calls = warm_quota_ledger.calls + EXCLUDED.calls;
