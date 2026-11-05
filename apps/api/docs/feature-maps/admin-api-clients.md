# Feature Map — Admin API Clients Management

Operational guidebook + technical map for the partner API-key lifecycle
surface. Verified end-to-end on 2026-11-05 against the real compose
stack (postgres :5433, redis :6380) with the outputs below captured live.

Plan: `plans/20261105-0940-admin-api-clients/` (R4 — auth boundary).

## What it does

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/admin/clients?limit&cursor` | List clients — never returns key material |
| `POST /api/v1/admin/clients` | Issue `cw_<rand>` key; plaintext returned **once**, `Cache-Control: no-store` |
| `PATCH /api/v1/admin/clients/{id}` | Rename / change role / deactivate+reactivate; guarded |

Auth: `X-API-Key` header; admin role required (401 unauthenticated, 403 reader).
Errors: stable envelope `{error:{code,message,request_id}}` —
`BAD_REQUEST`/`UNAUTHORIZED`/`FORBIDDEN`/`NOT_FOUND`/`ADMIN_LOCKOUT`/`INTERNAL`.

## Step-by-step (real outputs)

Start: `cd apps/api && docker compose up -d && go run ./cmd/api` (uses
`.env`; bootstrap admin = `API_BOOTSTRAP_ADMIN_KEY`).

### 1. Issue a partner key

```bash
curl -i -X POST -H "X-API-Key: $ADMIN" -H 'Content-Type: application/json' \
  -d '{"name":"partner-acme","role":"reader"}' \
  http://localhost:8010/api/v1/admin/clients
```

```http
HTTP/1.1 201 Created
Cache-Control: no-store
{"data":{"api_key":"cw_HL-aOyt8O0cFmW-…(masked in docs — plaintext shown once)",
 "created_at":"2026-11-05T03:21:37.856023Z","id":5,"is_active":true,
 "last_seen_at":null,"name":"partner-acme",
 "note":"api_key is shown exactly once — store it securely; it cannot be retrieved again",
 "role":"reader"}}
```

`api_key` appears in this response **only**. Copy it now — it is stored
server-side as SHA-256 hex and is never retrievable again.

### 2. Partner key authenticates immediately

```bash
curl -H "X-API-Key: cw_HL-aOyt8O0cFmW-…" http://localhost:8010/api/v1/networks
# {"data":[{"code":"bsc",…},{"code":"ethereum",…}]}   (200)
```

### 3. List clients (key material absent)

```bash
curl -H "X-API-Key: $ADMIN" 'http://localhost:8010/api/v1/admin/clients?limit=3'
```

```json
{"data":{"items":[
 {"id":5,"name":"partner-acme","role":"reader","is_active":true,
  "created_at":"2026-11-05T03:21:37.856023Z","last_seen_at":"2026-11-05T03:21:41.99032Z"},
 {"id":2,"name":"bootstrap-reader","role":"reader","is_active":true,…},
 {"id":1,"name":"bootstrap-admin","role":"admin","is_active":true,…}]}}
```

Fields enumerated: `id,name,role,is_active,created_at,last_seen_at` —
no `key_hash`, no `api_key`. `?cursor=<next_cursor>` pages keyset
`(created_at,id)` DESC; `limit` default 50, clamp 200, ≤0/non-int → 400.

### 4. Revoke a partner

```bash
curl -X PATCH -H "X-API-Key: $ADMIN" -H 'Content-Type: application/json' \
  -d '{"is_active":false}' http://localhost:8010/api/v1/admin/clients/5
# {"data":{…,"is_active":false}, "warnings":null}          (200)

curl -o /dev/null -w '%{http_code}' -H "X-API-Key: cw_HL-…" http://localhost:8010/api/v1/networks
# 401 — revoked key is rejected on its very next request
```

### 5. Lockout guards

```bash
# Admin deactivating itself → 409 (self-guard)
curl -X PATCH -H "X-API-Key: $ADMIN" -d '{"is_active":false}' \
  http://localhost:8010/api/v1/admin/clients/1
# {"error":{"code":"ADMIN_LOCKOUT",
#  "message":"self-demotion or self-deactivation is not permitted",…}}

# Removing the LAST remaining active admin → 409 (last-admin guard)
# {"error":{"code":"ADMIN_LOCKOUT",
#  "message":"operation would remove the last active admin",…}}
```

### 6. Env-managed rows disclose resurrection

```bash
curl -X PATCH -H "X-API-Key: $ADMIN" -d '{"name":"bootstrap-admin-renamed"}' \
  http://localhost:8010/api/v1/admin/clients/1
# {"data":{…,"name":"bootstrap-admin-renamed"},
#  "warnings":[{"code":"env_managed",
#   "message":"this client is managed by API_BOOTSTRAP_* env vars — the change
#    is reverted at restart while the env var is set; unset API_BOOTSTRAP_*
#    to revoke permanently"}]}
```

PATCH succeeds but warns: `UpsertAPIClient` re-sets `name/role/is_active`
at every boot while the env var stays set — the env bootstrap remains the
disaster-recovery path.

### 7. Validation matrix (verified)

| Request | Result |
|---|---|
| reader key on `/admin/*` | 403 FORBIDDEN |
| `{"role":"superuser"}` | 400 `role must be reader|admin` |
| unknown JSON field | 400 (DisallowUnknownFields) |
| `PATCH /clients/abc` | 400 invalid id |
| `PATCH /clients/999` | 404 not found |
| `PATCH {}` | 200 no-op (env rows still warn) |

## Database verification

```bash
docker exec api-postgres-1 psql -U wallet -d check_wallet \
  -c "SELECT id,name,role,is_active,left(key_hash,12) AS hash_prefix,created_at,last_seen_at FROM api_clients ORDER BY id;"
```

Real output captured 2026-11-05:

```
 id |          name           |  role  | is_active | hash_prefix  |          created_at           |         last_seen_at
----+-------------------------+--------+-----------+--------------+-------------------------------+-------------------------------
  1 | bootstrap-admin         | admin  | t         | 29b401073038 | 2026-11-05 03:21:10.504655+00 | 2026-11-05 03:21:47.763626+00
  2 | bootstrap-reader        | reader | t         | 3a1823d859d7 | 2026-11-05 03:21:10.507726+00 | 2026-11-05 03:21:47.624137+00
  5 | partner-acme            | reader | f         | 296263e497f5 | 2026-11-05 03:21:37.856023+00 | 2026-11-05 03:21:41.99032+00
```

Only `key_hash` (SHA-256 hex) persists — plaintext never touches the DB.

## Technical feature map

### Source paths

| Layer | File |
|---|---|
| Queries (sqlc) | `apps/api/queries/api_clients.sql` → generated `internal/db/api_clients.sql.go` |
| Domain service | `apps/api/internal/apiclient/{service,model,keys}.go` |
| HTTP handler | `apps/api/internal/httpserver/clients.go` |
| Route wiring | `apps/api/internal/httpserver/server.go` (admin group, `d.Admin \|\| d.Clients` gate) |
| Error code | `apps/api/internal/middleware/errors.go` → `CodeAdminLockout` |
| Boot wiring | `apps/api/cmd/api/main.go` → `bootstrapClientHashes(cfg)` into `apiclient.New` |
| Contract | `apps/api/api/openapi.yaml` (`/admin/clients`, `ApiClient`, `ApiClientCreated`) |

### Key mechanics

- `GenerateKey` (`internal/apiclient/keys.go`): `cw_` +
  `base64.RawURLEncoding(32 crypto/rand bytes)` ≈ 47 chars.
- Digest: `middleware.HashAPIKey` (SHA-256 hex) — single impl; inbound
  `X-API-Key` hashing unchanged, so arbitrary-format keys stay valid.
- `Update` tx: `pg_advisory_xact_lock(hashtext('apiclient.admin_guard'))`
  → row `FOR UPDATE` → post-state guards → column-scoped writes
  (`UpdateAPIClient{Name,Role,Active}`) → commit. Serializes A↔B
  concurrent demotes; `last_seen_at` never clobbered.
- Guard (a): self-demote/self-deactivate → `ErrSelfLockout` → 409
  `ADMIN_LOCKOUT` ("self-demotion or self-deactivation is not permitted").
  Self no-op `{role:"admin"}` and self-rename pass.
- Guard (b): removing an *active admin* with zero other
  `role='admin' AND is_active AND id != target` → `ErrAdminLockout` →
  409 `ADMIN_LOCKOUT`. Touching an already-inactive admin succeeds.
- `env_managed` warnings: service holds `bootstrapHashes` (digests of
  `API_BOOTSTRAP_*` keys); any PATCH on a matching row appends the warning.
- Audit: `slog.Info("api client mutation", actor_id, actor_name,
  target_id, changed_fields, request_id)` per mutation — `request_id`
  passed from handler (`middleware.GetRequestID`).

### Roles & bootstrap

`reader` = partners (all `/api/v1/*` reads + screenings);
`admin` = internal (+`/admin/*`). Env bootstrap
(`API_BOOTSTRAP_ADMIN_KEY`/`API_BOOTSTRAP_READER_KEY`) still upserts at
boot and is the documented recovery path; named admin keys minted via
POST are the recommended way to issue per-person admin credentials.

### Tests

- `apps/api/internal/apiclient/service_test.go` — 12 tests on
  `check_wallet_apiclient_test` (package-private DSN): guards,
  concurrency (A↔B under -race), env warnings, pagination.
- `apps/api/tests/clients_api_test.go` — 9 tests on
  `check_wallet_test`: role gate, create→auth round-trip, field
  enumeration (no key material), patch flows, 400/404/409 matrix,
  `no-store` header, duplicate names.
