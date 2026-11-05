---
phase: 2
title: "HTTP surface + OpenAPI contract"
status: completed
priority: P1
dependencies: [1]
---

# Phase 2: HTTP surface + OpenAPI contract

## Overview

`httpserver.ClientHandler` on the admin group, Deps wiring in
`cmd/api`, OpenAPI additions with examples, and integration tests.

## Requirements

- Functional:
  - `GET  /api/v1/admin/clients?limit&cursor` → 200 `{data:{items:[…],next_cursor?}}`; `items:[]` on empty
  - `POST /api/v1/admin/clients` `{name,role}` → 201 `{data:{…,api_key,note}}` + `Cache-Control: no-store`
  - `PATCH /api/v1/admin/clients/{id}` `{name?,role?,is_active?}` → 200 `{data:{…},warnings?}`
  - Errors: 400 `BAD_REQUEST` (bad JSON/unknown field/bad role/bad
    id/`limit`≤0/non-int/invalid cursor), 401 `UNAUTHORIZED`,
    403 `FORBIDDEN`, 404 `NOT_FOUND`, 409 `ADMIN_LOCKOUT`
  - `DisallowUnknownFields` on POST **and** PATCH bodies
  - `PATCH {}` → no-op returning current view
  - Actor from `middleware.ClientFrom(c)` feeds guards + domain log;
    `request_id` comes from `middleware.GetRequestID(c)` passed into the
    Update call (actor struct or explicit arg) — never re-derived in
    the service
- Non-functional:
  - View = `{id,name,role,is_active,created_at,last_seen_at}` — never
    `key_hash`/`api_key` (asserted)
  - `limit` >200 clamps to 200

## Architecture

Shape mirrors `httpserver/provider_configs.go`: thin handler → `h.err`
switch → `middleware.RespondError`. New code
`CodeAdminLockout = "ADMIN_LOCKOUT"` in middleware/errors.go.
Deps gains `Clients *apiclient.Service`; admin group gate becomes
`d.Admin != nil || d.Clients != nil`. `cmd/api/main.go` wires
`apiclient.New(pool, queries, bootstrapHashes(cfg), log)` into
`Deps.Clients` — bootstrap hashes derive from `cfg.BootstrapAdminKey` /
`cfg.BootstrapReaderKey` via `middleware.HashAPIKey`.

## Related Code Files

- Create: `apps/api/internal/httpserver/clients.go`
- Modify: `apps/api/internal/httpserver/server.go` (Deps + routes)
- Modify: `apps/api/internal/middleware/errors.go` (CodeAdminLockout)
- Modify: `apps/api/cmd/api/main.go` (Deps.Clients wiring)
- Modify: `apps/api/api/openapi.yaml` (3 paths, `ApiClient` /
  `ApiClientCreated` schemas, examples, 409 response, env_managed
  warnings example)
- Create: `apps/api/tests/clients_api_test.go`

## Implementation Steps

1. Write `tests/clients_api_test.go` first (Red): role gate
   (401/403/200), POST → key round-trips auth → absent from GET, list
   fields + `items:[]`, patch rename/revoke/reactivate,
   400/404/409 matrix, `limit`/cursor edges, duplicate names coexist,
   `no-store` header on 201.
2. Add `CodeAdminLockout`; implement `clients.go`.
3. Wire Deps + main.go.
4. Extend `api/openapi.yaml` (match admin section style exactly).
5. `go test ./tests/... ./internal/...` green; `go build ./...`.

## Success Criteria

- [x] `tests/clients_api_test.go` green
- [x] No response body exposes `key_hash`/raw key (asserted)
- [x] `Cache-Control: no-store` on POST 201 (asserted)
- [x] openapi.yaml documents all 3 endpoints with real examples
- [x] `go build ./...` clean, `gofmt -l .` empty

## Risk Assessment

- Admin group gating: `Admin`-only deployments still work when
  `Clients` nil (per-dep route guard, same as ProviderConfigs).
- `{id}` parse: `abc`→400; `0`/`-1`/unknown→404.
