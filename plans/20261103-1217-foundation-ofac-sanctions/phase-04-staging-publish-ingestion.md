---
phase: 4
title: "Staging Validation, Atomic Publish & Admin Ingestion API"
status: pending
priority: P1
dependencies: [3]
---

# Phase 4: Staging Validation, Atomic Publish & Admin Ingestion API

## Overview

Orchestrate `download → stage → validate → publish` inside `internal/ingestion`
with Postgres advisory-lock single-flight, `ingestion_runs` accounting,
atomic snapshot publication (single tx, revoke-absent semantics), snapshot
versioning for the cache, and the admin trigger/status endpoints.

## Requirements

- Functional: `ingestion.Service.RunOFAC(ctx)` — **pin a dedicated
  `pool.Acquire()` connection for the entire run** (download→publish) and
  hold `pg_try_advisory_lock` on it, keyed per `source_id` (session-scoped
  locks silently drop if the conn returns to the pool); **plus** a DB-layer
  gate: partial unique index
  `UNIQUE(source_id) WHERE status='RUNNING'` on `ingestion_runs`
  (defense-in-depth, G-005). Busy → `ErrIngestionRunning`→409. Create
  `ingestion_runs` row (RUNNING, `triggered_by` = api client id or
  'worker'); download; stage into `stg_*` (run_id); validate: staging row
  counts >0, every `stg_sanctions_wallets` → `stg_sanctions_entities`
  FK-resolvable, every `stg_sanctions_entity_programs` →
  `stg_sanctions_programs` resolvable, no orphan keys, `DateOfIssue` not
  older than current published snapshot (reject stale upstream mirrors),
  entity-count sanity band vs previous snapshot; publish in **one
  transaction** (with `lock_timeout`/`statement_timeout`): upsert
  entities/programs/wallets/references (stable keys, `last_seen_at=now`,
  clear `revoked_at`), link `wallet_id` via get-or-create for resolvable
  networks, `revoked_at=now` on live rows absent from staging **scoped to
  this source** (`sanctions_entity_id IN (SELECT id FROM
  sanctions_entities WHERE source_id = ofac)`), write
  `sanctions_snapshots` (version=checksum-prefix+ts or monotonic, checksum,
  counts) and bump `sanctions:current_snapshot` in Redis; mark run
  COMPLETED with counters; on any earlier failure mark FAILED +
  `error_message`, live tables untouched. **Checksum-identical snapshot →
  skip publish entirely** (mark run COMPLETED with counters, no new
  snapshot version, no cache-epoch churn).
- Non-functional: publish tx bounded; concurrent ingestion never produces
  partial publish; endpoint admin-only; run status queryable.

## Architecture

- `sanctions_snapshots(id bigserial, source_id, version unique, checksum,
  entity_count, wallet_count, program_count, published_at)` — migration
  `000003`; current version cached `sanctions:current_snapshot`.
- Publish SQL lives in `queries/ofac_publish.sql` (sqlc) + tx orchestration in
  `internal/ingestion/publish.go` (pgx `Begin`, not string SQL).
- Revocation rule: `revoked_at IS NULL → now()` only for identifiers absent
  from the **validated** staging set; failed validation → zero revocations
  (spec: never delete/revoke after incomplete sync).
- Admin API: `POST /api/v1/admin/ingestion-runs {source:"ofac"}` → 202
  `{run_id,status:"RUNNING"}` — **async**: run in a goroutine on a
  **detached `context.Background()`-derived ctx** (never
  `c.Request.Context()` — it cancels when the 202 flush completes) with a
  `recover()` wrapper marking the run FAILED on panic; concurrent →
  409 `INGESTION_RUNNING`. `GET /api/v1/admin/ingestion-runs/{id}` →
  status/counters/checksum/error/triggered_by.
- **Staging retention**: delete `stg_*` rows for a run after successful
  publish (evidence persists in live `evidence` jsonb); keep failed-run
  staging for diagnosis (last N=5 runs), purge orphaned staging from
  crashed runs on next run start.
- `cmd/worker`: `-source ofac -file <path>` runs the same service path
  (file-based snapshot for dev/tests, no HTTP download).

## Related Code Files

- Create: `internal/ingestion/service.go`, `publish.go`, `validate.go`,
  `locks.go`; `internal/ingestion/handler.go` (admin endpoints)
- Create: `migrations/000003_sanctions_snapshots.up.sql` / `.down.sql` —
  `sanctions_snapshots` table **plus** `ALTER TABLE ingestion_runs ADD
  COLUMN triggered_by TEXT` and the partial unique index
  `UNIQUE(source_id) WHERE status='RUNNING'` (both live in 000003,
  file+manifest pairs per P-007)
- Create: `queries/ofac_publish.sql`, `queries/ingestion_runs.sql`
- Modify: `cmd/worker/main.go` (real entrypoint), `internal/httpserver` wiring

## Implementation Steps (TDD)

1. Red (`tests/ingestion_publish_test.go`, `_test` db): seed staging →
   publish → live rows + snapshot row; re-run → `last_seen_at` bumps, zero
   duplicates (assert by unique-key counts, not just no-error).
2. Red: fixture B missing one wallet identifier that was in fixture A →
   publish B → that row `revoked_at` set, others untouched, row NOT deleted.
3. Red: staging failing validation (orphan program ref / zero rows) →
   publish aborted, live data identical pre/post (hash/dump compare), run
   FAILED with message.
4. Red: concurrent `RunOFAC` → second returns `ErrIngestionRunning`;
   two parallel admin POSTs → one 202, one 409.
5. Green: implement service/validate/publish/handlers + worker.
6. Integration: full pipeline fixture → `POST /admin/ingestion-runs` → poll
   GET → COMPLETED with counters; live tables queryable (feeds phase 05).
7. Refactor: extract publish tx steps into readable helpers; keep single tx.

## Success Criteria

- [ ] Atomic publish verified: kill-safety — validation failure leaves live
      tables byte-identical.
- [ ] Revocation only post-validation, absent-only, never deletes.
- [ ] Advisory lock proven: concurrent run rejected, no double publish.
- [ ] `ingestion_runs` counters accurate per stage outcome.
- [ ] `sanctions:current_snapshot` updated exactly once per publish; version
      embedded in snapshot row.
- [ ] Admin endpoints enforce role; non-admin → 403.
- [ ] Tests green on `_test` db; build/vet/fmt clean.

## Risk Assessment

- Long publish tx locking readers → publish uses upserts not table locks;
  snapshot row + version flip at commit keeps readers consistent.
- Publish partial-failure mid-tx → single transaction = all-or-nothing; test
  induced error → rollback verified.
- Staging cross-run contamination → staging rows scoped by `run_id`,
  truncated for that run before stage.
- Version drift between cache and DB → version sourced from
  `sanctions_snapshots` row written inside the same tx; cache bump after
  commit (post-commit hook), lookup re-reads version on miss.
