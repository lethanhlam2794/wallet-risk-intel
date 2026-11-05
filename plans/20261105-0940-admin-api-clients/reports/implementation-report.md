# Implementation Report — Admin API Clients Management

Date: 2026-11-05 · Executed by: Devin Cook · Risk: R4 (auth boundary —
credential issuance + privilege grant)

## Outcome

Both phases complete. Three admin endpoints are live behind the admin
role gate on `/api/v1/admin`:

- `GET  /api/v1/admin/clients` — keyset pagination `(created_at,id)` DESC,
  `limit` default 50 / clamp 200 / ≤0 or non-int → 400, `items:[]` on
  empty pages, invalid cursor → 400.
- `POST /api/v1/admin/clients` — mints `cw_` + base64url(32 crypto/rand
  bytes), stores SHA-256 only via `middleware.HashAPIKey` (single digest
  impl), returns plaintext exactly once under `Cache-Control: no-store`.
  Not idempotent by design (documented in OpenAPI).
- `PATCH /api/v1/admin/clients/{id}` — whitelisted `{name,role,is_active}`
  via `DisallowUnknownFields` (POST and PATCH); `{}` no-op returns the
  current view; column-scoped writes preserve `last_seen_at`.

Lockout guards per pinned spec: namespaced
`pg_advisory_xact_lock(hashtext('apiclient.admin_guard'))` → row
`FOR UPDATE` → (a) unconditional self-demote/self-deactivate reject
(self no-op `{role:"admin"}` and self-rename pass), (b) post-state
zero-remaining-active-admin reject — both 409 `ADMIN_LOCKOUT`.
`env_managed` warning on any PATCH of a bootstrap-hash row, including
empty patches. One structured `slog.Info` per mutation with
`actor_id`/`actor_name`/`target_id`/`changed_fields`/`request_id`
(request id passed from handler, never re-derived).

## Files

Created:
- `apps/api/internal/apiclient/{keys,model,service}.go`
- `apps/api/internal/apiclient/service_test.go` (12 tests)
- `apps/api/internal/httpserver/clients.go`
- `apps/api/tests/clients_api_test.go` (9 tests)

Modified:
- `apps/api/queries/api_clients.sql` — 9 new queries (GetAPIClientByID,
  ListAPIClients narg cursor, InsertAPIClient, GetAPIClientForUpdate,
  LockClientAdminTransitions, UpdateAPIClientName/Role/Active,
  CountOtherActiveAdmins)
- `apps/api/internal/db/api_clients.sql.go` — regenerated via sqlc
- `apps/api/internal/httpserver/server.go` — Deps.Clients, admin group
  gate `d.Admin != nil || d.Clients != nil`, per-dep route guards
- `apps/api/internal/middleware/errors.go` — `CodeAdminLockout`
- `apps/api/cmd/api/main.go` — `bootstrapKeyHashes` helper + wiring
- `apps/api/api/openapi.yaml` — 3 paths, `ApiClient`/`ApiClientCreated`/
  `ApiClientCreate`/`ApiClientPatch`/`ApiClientWarning` schemas, examples
  incl. env_managed warning and 409 ADMIN_LOCKOUT
- `plans/.../phase-01-domain-persistence.md`, `phase-02-http-contract.md`
  — status: completed

## Verification (real runners)

| Command | Result |
|---|---|
| `go test -race -v ./internal/apiclient/...` | 12/12 PASS (5.5s) on `check_wallet_apiclient_test` |
| `go test ./tests/ -run TestClients -v` | 9/9 PASS on `check_wallet_test` |
| `go test ./tests/... ./internal/...` | ALL PASS — tests 62.1s + every internal package |
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `gofmt -l .` | empty |
| `git diff --check` | clean |

Environment: docker compose stack postgres:5433 + redis:6380 (healthy);
test DBs auto-created/migrated by `testutil.RequireTestPoolDSN`.

## Evidence highlights

- Key round-trip: POST-minted `cw_` key authenticates via
  `GetAPIClientByKeyHash` + full middleware chain (reader→200 on
  /networks, 403 on admin; admin→200 on admin). Arbitrary-format legacy
  keys unaffected.
- Concurrent A↔B demote (service, -race): exactly one racer succeeds,
  one gets `ErrAdminLockout`, ≥1 active admin remains — serialized by
  the advisory lock.
- Inactive-admin demote/deactivate: no false 409 (post-state guard).
- No response body exposes `key_hash` or raw key (asserted on list,
  patch, post-list GETs); view field set enumerated and asserted.
- `last_seen_at` preserved across PATCH (column-scoped writes).

## Deviations / decisions inside pinned spec

- `Create` also takes `actor` (phase-01 abbreviated signature) — the
  pinned "structured log per mutation" needs actor fields; request_id
  still comes from the handler.
- `PATCH {}` on an env-managed row returns `env_managed` too —
  "warning on ANY patch of a bootstrap-hash row" read literally.
- Non-numeric `{id}` → 400; `0`/`-1`/unknown → 404 via service
  `ErrNotFound` (per phase-02 risk note).
- Guard (b) sequential case exercised with a non-admin actor (only
  reachable concurrency path via HTTP is the A↔B test, also covered).

## Rollback

No schema migration. `Deps.Clients = nil` removes the routes entirely;
existing `api_clients` rows (incl. minted keys) keep working — auth
path unchanged. Reverting the diff restores prior behavior with no data
impact.

## Residual notes

- `apps/api/docs/` is untracked — it is this pipeline's own stage log
  (`pipeline-log.md`, app-scoped per the codex-vibe docs strategy), not
  part of the feature diff.
- POST intentionally non-idempotent (plan decision, documented in
  OpenAPI): retried create mints a second live key.
