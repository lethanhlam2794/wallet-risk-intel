---
phase: 1
title: "Domain service + persistence"
status: completed
priority: P1
dependencies: []
---

# Phase 1: Domain service + persistence

## Overview

sqlc queries for client CRUD + `internal/apiclient` package: key
generation, list/create/patch with admin-lockout guards, env-managed
warnings, and a structured mutation log — verified by service tests on a
package-private test database.

## Requirements

- Functional:
  - `GenerateKey()` → `cw_` + base64url(32 crypto/rand bytes); hashing
    delegates to `middleware.HashAPIKey` (single digest impl — no dup)
  - `List(ctx, cursor, limit)` → keyset page `(created_at,id)` DESC;
    `limit` default 50, >200 clamps, ≤0/non-int handled at handler;
    `next_cursor` only when truncated; empty page → `Items: []`
  - `Create(ctx, name, role)` → validate name (trim, 1–150 **runes**),
    role ∈ {reader,admin}; insert hash; return view + plaintext key
  - `Update(ctx, id, patch{name?,role?,is_active?}, actor)` → tx:
    `pg_advisory_xact_lock(hashtext('apiclient.admin_guard'))` → row
    `FOR UPDATE` → validate → **post-state** lockout guards →
    column-scoped writes → return view + optional `env_managed` warning
  - Lockout guards (409 `ADMIN_LOCKOUT`):
    (a) actor demoting (admin→reader) or deactivating **own** row —
        unconditional reject (self `{role:"admin"}` no-op and
        self-rename pass through);
    (b) mutation removing an *active admin* while zero other
        `role='admin' AND is_active AND id != target` rows remain.
        Guard (b) evaluates post-state: demoting/deactivating an
        already-inactive admin succeeds.
  - `env_managed` warning applies to **any** PATCH mutation on an
    env-managed row (name/role/is_active all revert at boot, incl. the
    upsert's name+role reset — no separate handling needed)
  - Env-managed rows: `Service` receives the configured bootstrap
    key-hashes; `Update` on a matching row appends
    `warnings:[{code:"env_managed",…}]` (restart resurrects the env
    state — revocation persists only after unsetting `API_BOOTSTRAP_*`)
  - One structured `slog.Info` per mutation (actor, target id, changed
    fields, request_id) — AccessLog's route pattern lacks the target
- Non-functional:
  - Raw key material only in `Create` return — never in views/logs/errors
  - Guard+mutate in one tx; advisory lock invariant: every role/
    is_active writer holds it (`UpsertAPIClient` exempt — never removes
    admins)

## Architecture

New package `internal/apiclient` (singular): `keys.go` (GenerateKey),
`model.go` (View DTO, Page, Warning), `service.go`
(`Service{pool, queries, bootstrapHashes map[string]bool, log}`; errors
ErrNotFound, ErrValidation, ErrAdminLockout). Tx via `q.WithTx(tx)`
(verified on `db.Queries`, db.go:28).

## Related Code Files

- Modify: `apps/api/queries/api_clients.sql` — add GetAPIClientByID,
  ListAPIClients (narg cursor), InsertAPIClient, GetAPIClientForUpdate,
  UpdateAPIClientName/Role/Active, CountOtherActiveAdmins,
  LockClientAdminTransitions
- Regen: `apps/api/internal/db/api_clients.sql.go` via `make sqlc`
- Create: `apps/api/internal/apiclient/{keys,model,service}.go`
- Create: `apps/api/internal/apiclient/service_test.go`
  (`packageDSN = …/check_wallet_apiclient_test`, RequireTestPoolDSN)

## Implementation Steps

1. Write `service_test.go` first (Red): create/auth round-trip, list
   pagination + empty page, patch validations, self-guard, last-admin
   guard incl. already-inactive no-op, env_managed warning,
   concurrent A↔B demote (one must 409; ≥1 admin remains).
2. Extend `queries/api_clients.sql`; `make sqlc`; `go build ./internal/db`.
3. Implement keys/model/service (Green).
4. `go test ./internal/apiclient/...` (compose stack required).

## Success Criteria

- [x] `go test ./internal/apiclient/...` green on `check_wallet_apiclient_test`
- [x] Generated `cw_` key authenticates through `IdentifyClient`;
      arbitrary-format keys still valid
- [x] Concurrent A↔B demote: at least one active admin remains
- [x] Inactive-admin demote/deactivate succeeds (no false 409)
- [x] `go build ./...` clean

## Risk Assessment

- sqlc `narg` cursor typing — copy provider_configs.sql pattern verbatim.
- `pg_advisory_xact_lock` tx scope kept tiny (no network I/O inside).
