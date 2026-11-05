---
phase: 1
title: "Schema + sqlc queries"
status: completed
priority: P1
dependencies: []
---

# Phase 1: Schema + sqlc queries

## Overview

Additive-only migration `000023` creating `warm_queue` + `warm_quota_ledger`,
plus `queries/warm_index.sql` and regenerated `internal/db` code.

## Requirements

- Functional: exact column set from the design contract; partial unique index
  `WHERE status IN ('pending','claimed')` for active dedupe.
- Non-functional: additive only — no existing-table changes; down migration
  drops both tables.

## Architecture

```sql
CREATE TABLE warm_queue (
    id BIGSERIAL PRIMARY KEY,
    network_id BIGINT NOT NULL REFERENCES blockchain_networks(id),
    address TEXT NOT NULL CHECK (address ~ '^0x[0-9a-f]{40}$'),
    priority INT NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    source_screening_id UUID,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','claimed','done','dead')),
    attempts INT NOT NULL DEFAULT 0,
    claimed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX uq_warm_queue_active
    ON warm_queue (network_id, address)
    WHERE status IN ('pending','claimed');
CREATE INDEX idx_warm_queue_pending_order
    ON warm_queue (priority DESC, id) WHERE status = 'pending';

CREATE TABLE warm_quota_ledger (
    provider_code TEXT NOT NULL,
    period_start DATE NOT NULL,
    calls INT NOT NULL DEFAULT 0,
    PRIMARY KEY (provider_code, period_start)
);
```

Down: `DROP TABLE warm_quota_ledger; DROP TABLE warm_queue;`

`queries/warm_index.sql` (sqlc, pgx/v5, named `:one`/`:exec`/`:many`):

- `EnqueueWarmItem` — upsert per D4 (`ON CONFLICT (network_id,address)
  WHERE status IN ('pending','claimed') DO UPDATE …`), `RETURNING id`.
- `ResetExpiredWarmClaims` — lease reset **counts the crashed attempt**
  (D10): `UPDATE … SET attempts=attempts+1, claimed_at=NULL, updated_at=now(),
  status = CASE WHEN attempts+1 >= $2 THEN 'dead' ELSE 'pending' END
  WHERE status='claimed' AND claimed_at < $1` returns count.
  (`$1`=cutoff, `$2`=MaxAttempts.)
- `ClaimNextWarmItem` — atomic claim per D2, `RETURNING *`.
- `CompleteWarmItem` — `status='done', updated_at=now()` by id.
- `FailWarmItem` — `attempts=attempts+1, claimed_at=NULL, updated_at=now(),
  status = CASE WHEN attempts+1 >= $2 THEN 'dead' ELSE 'pending' END` by id.
- `ReleaseWarmClaim` — `status='pending', claimed_at=NULL, updated_at=now()`
  by id (eligibility release — no attempt bump).
- `GetWarmQuotaCalls` — `SELECT calls … WHERE provider_code=$1 AND
  period_start=$2` (0 when no row → handle `pgx.ErrNoRows`).
- `AddWarmQuotaCalls` — `INSERT … ON CONFLICT (provider_code,period_start)
  DO UPDATE SET calls = warm_quota_ledger.calls + EXCLUDED.calls`.

## Related Code Files

- Create: `apps/api/migrations/000023_warm_index_queue.up.sql`,
  `…down.sql`, `apps/api/queries/warm_index.sql`
- Modify: `apps/api/internal/db/*` (regenerated via `make sqlc`)

## Implementation Steps (TDD)

1. RED: write a migration-smoke test later phases extend — for this phase,
   "red" = `sqlc generate` fails (queries reference missing tables) until
   migration applied; write the .sql files first.
2. Create up/down migrations exactly per contract.
3. Create `queries/warm_index.sql`; run `make sqlc`.
4. `DATABASE_URL=…/check_wallet_warmindex_test go run ./cmd/migrate` (or
   `make migrate-test`) → verify 000023 applied; rollback once and re-apply
   to prove down is clean.
5. `go build ./...` green.

## Success Criteria

- [x] `000023` up/down apply cleanly on the test DB; re-up idempotent.
- [x] `make sqlc` generates `internal/db/warm_index.sql.go` + model types —
  this is the **early grammar gate**: if pg_query chokes on the
  `ON CONFLICT … WHERE` arbiter, the decided fallback is UPDATE-then-INSERT
  inside a transaction (still race-safe via the partial index's 23505).
- [x] `go build ./...` exits 0.
- [x] No changes to existing tables (`git diff migrations/` = 2 new files).
- [x] `testutil` needs no change — `warm_*` tables are auto-truncated per test
  (`staticTables` keeps only seed tables).

## Risk Assessment

- Partial-unique-index ON CONFLICT: inference predicate must match the index
  verbatim (`WHERE status IN ('pending','claimed')`) — sqlc gate above catches
  grammar issues before any logic is written.
- sqlc `RETURNING *` on `warm_queue` generates the full row — fine.
