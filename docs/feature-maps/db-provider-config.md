# Feature Map & Operational Guidebook — DB-managed Provider Config

Delivered via the 6-stage Codex Vibe pipeline (plan:
`plans/20261104-1800-db-provider-config/`, approved spec in
`prompt-master.md`, task brief
`docs/prompts/20261104-2258-codex-db-provider-config.md`).
Backend-only feature — no UI surface; evidence is live API JSON + SQL
captures (`plans/.../reports/stage5/`).

## 1. What ships

Per-provider operational config for the **12 tx-history adapters**
moved from env-only into a DB-managed, admin-editable surface —
patterned on greatify-api's `providers`/`provider_changes`/`authInfo`:

| Capability | Where |
|---|---|
| `provider_configs` table — api_key / base_url / enabled_chains / status per `data_sources.code` | migration `000020` |
| `provider_config_changes` — masked, per-field audit trail (actor, before/after, timestamp) | migration `000020` |
| Boot resolver — **DB non-null wins, env falls back only on absent** | `internal/providerconfig/resolver.go` |
| AES-256-GCM keystore (`enc:v1:` envelope) active iff `PROVIDER_CONFIG_KEK` set | `internal/providerconfig/keystore.go` |
| Runtime kill-switch — ~30s TTL-cached status check inside the failover chain walk | `internal/providerconfig/status_source.go` + `internal/transaction/service.go` |
| Admin REST API — masked reads, whitelisted writes, audit history | `internal/httpserver/provider_configs.go` + `internal/providerconfig/service.go` |
| `provider.AllowedHostsFor(code)` — adapters publish their SSRF allowlists | `internal/provider/allowlist.go` + per-adapter `init()` |

## 2. Source map

| Concern | Path |
|---|---|
| Schema (both tables) | `migrations/000020_provider_configs.{up,down}.sql` |
| sqlc queries | `queries/provider_configs.sql` → `internal/db/provider_configs.sql.go` |
| Resolver + descriptor map (12 providers, `chainscan`→`ETHERSCAN_API_KEY`) | `internal/providerconfig/{resolver,descriptor}.go` |
| Keystore (enc:v1 marker) | `internal/providerconfig/keystore.go` |
| Status cache (goroutine-safe TTL, last-known/cold-start) | `internal/providerconfig/status_source.go` |
| Admin service (masked views, column-scoped writes, audit, warnings) | `internal/providerconfig/service.go` |
| HTTP handler + routes | `internal/httpserver/provider_configs.go`, `server.go` |
| Boot wiring (resolved adapters, kill-switch, admin svc) | `cmd/api/main.go` |
| Kill-switch consult point | `internal/transaction/service.go` (`ensure` disabled-skip case) |
| Tests | `internal/providerconfig/*_test.go`, `internal/transaction/killswitch_test.go`, `tests/provider_configs_api_test.go` |
| OpenAPI | `api/openapi.yaml` (`/api/v1/admin/provider-configs*`) |

## 3. Pinned semantics (the contract)

- **Resolution**: DB non-NULL wins; env fallback **only** on absent/NULL.
  `enabled_chains=[]` and `base_url=""` are *explicit overrides* — never
  treated as "use env". Row absent ⇒ byte-identical env behavior.
- **Errors**: any resolution error (DB, decrypt, allowlist, malformed
  JSON) is **boot-fatal naming provider+field** — never env fallback.
- **Keystore**: `enc:v1:<b64 nonce>:<b64 ct>`; full-shape check before
  decrypt; `enc:`-prefixed plaintext + KEK ⇒ boot-fatal; client `enc:`
  input ⇒ 400; encrypt-on-write iff KEK; plaintext mode documented.
- **Kill-switch**: `effective = data_sources.is_active AND
  (status='active' OR row absent)`; TTL ~30s; DB error ⇒ last-known
  (cold ⇒ active + log); consulted **before** upstream spend via the
  exact disabled-skip path; all-inactive ⇒ `disabled` under chain[0]'s
  source_id.
- **Boot vs runtime**: `status` is the only runtime-effective field;
  api_key/base_url/enabled_chains need restart — responses disclose
  `requires_restart`, PUT adds `warnings[]` when stored≠in-effect.
- **Admin writes**: whitelist {api_key, base_url, enabled_chains,
  status}; masked `{"set":bool,"last4":string|null}` (last4 suppressed
  <8 chars); base_url via `AllowedHostsFor` + https; unknown code ⇒
  404; column-scoped UPDATE inside one tx; masked audit row per field
  same tx; `actor_id`/`updated_by` = `ClientFrom(c).Name`,
  `actor_type="api_client"`; `PUT {}` is a no-op read.

## 4. API contract

```
GET /api/v1/admin/provider-configs            → 200 list (12 providers, PUT-discoverable)
GET /api/v1/admin/provider-configs/{code}     → 200 view | 404
PUT /api/v1/admin/provider-configs/{code}     → 200 {data, warnings[]} | 400/403/404
GET /api/v1/admin/provider-configs/{code}/changes?cursor&limit → 200 {items, next_cursor}
```

Auth: `X-API-Key`, admin role required (reader ⇒ 403, absent ⇒ 401).
View fields: `source_code, configured, api_key{set,last4}, base_url,
enabled_chains, status, catalog_active, effective_active,
requires_restart, updated_by, updated_at`. Warning codes:
`requires_restart` (per-field drift), `no_enabled_chains`,
`last_active_member` (per-network).

## 5. Step-by-step operations guidebook

### 5.1 Boot and inspect resolution origins

```bash
HTTP_ADDR=:8090 TX_PROVIDER_ETH=chainscan,blockscout go run ./cmd/api
# log: "provider config resolved" provider=chainscan api_key="env" ...
#      origins only — values are never logged
```
*(capture: `reports/stage5/00-boot-resolution.log`)*

### 5.2 List + inspect the masked surface

```bash
curl -H "X-API-Key: $ADMIN" :8090/api/v1/admin/provider-configs
curl -H "X-API-Key: $ADMIN" :8090/api/v1/admin/provider-configs/chainscan
```
→ 200; every provider `configured:false` with `{"set":false,"last4":null}`
*(captures: `01-list.json`, `02-get-chainscan-before.json`; 2.5–7.5ms)*

### 5.3 Write config — masked response + warnings

```bash
curl -X PUT -H "X-API-Key: $ADMIN" -H "Content-Type: application/json" \
  -d '{"api_key":"…","base_url":"https://api.covalenthq.com","enabled_chains":["ethereum"]}' \
  :8090/api/v1/admin/provider-configs/covalent
```
→ `api_key{"set":true,"last4":"7777"}`; `warnings[]` fires
`requires_restart` only for fields differing from the boot snapshot
*(capture: `03-put-covalent.json`)*

### 5.4 Verify the audit trail

```bash
curl -H "X-API-Key: $ADMIN" ":8090/api/v1/admin/provider-configs/covalent/changes?limit=10"
```
→ per-field masked rows, actor=`bootstrap-admin`, `next_cursor` keyset
pagination *(capture: `04-changes.json`)*

### 5.5 Runtime kill-switch (live proof)

```bash
curl -X PUT -H "X-API-Key: $ADMIN" -d '{"status":"inactive"}' \
  :8090/api/v1/admin/provider-configs/chainscan
# effective_active:false, warnings:null (blockscout still active)

sleep 33   # outlive the ~30s TTL
curl -H "X-API-Key: $ADMIN" \
  ":8090/api/v1/wallets/ethereum/0x…12345/transactions?refresh=1"
# → blockscout serves fresh/ok; SQL shows chainscan's
#   indexed_until_block FROZEN while blockscout's advances —
#   zero upstream spend on the flipped provider
```
*(captures: `07-killswitch-flip.json`, `08-tx-after-flip.json`,
`06/09-sql-*.txt`)*

### 5.6 Flip back — runtime-effective, no restart

```bash
curl -X PUT -H "X-API-Key: $ADMIN" -d '{"status":"active"}' \
  :8090/api/v1/admin/provider-configs/chainscan
```
*(capture: `10-flip-back.json`)*

## 6. Database verification queries (executed live)

```sql
-- What is stored (key shown by length only):
SELECT source_code, api_key IS NOT NULL AS key_set, base_url,
       enabled_chains, status, updated_by, updated_at
FROM provider_configs ORDER BY source_code;

-- Masked audit trail (plaintext key never persisted):
SELECT field, previous_value, new_value, actor_id, actor_type, changed_at
FROM provider_config_changes ORDER BY changed_at DESC, id DESC;

-- Kill-switch effect on index watermarks:
SELECT s.code, wis.status, wis.indexed_until_block, wis.indexed_at
FROM wallet_index_state wis
JOIN wallets w ON w.id=wis.wallet_id
JOIN data_sources s ON s.id=wis.source_id
JOIN blockchain_networks n ON n.id=wis.network_id
WHERE w.address='<wallet>' AND n.code='ethereum';
```
Outputs captured verbatim: `reports/stage5/11-sql-dump.txt`,
`06/09-sql-*.txt`.

## 7. Env/config contract

| Env | Effect |
|---|---|
| `PROVIDER_CONFIG_KEK` | 32-byte base64/hex ⇒ api_key encrypted at rest; unset ⇒ documented plaintext |
| `<ADAPTER>_API_KEY` / `_BASE_URL` / `_ENABLED_CHAINS` | env fallback layer — consulted only for NULL fields |
| `TX_PROVIDER_<NET>` | chain ordering unchanged (env-only in v1) |
| `data_sources.is_active` | catalog switch; `provider_configs.status` = operator switch |

## 8. Test coverage map

- Resolver: 8-case truth table, env parity, named boot-fatal errors
- Keystore: round-trip, tamper, marker shapes, no-KEK plaintext
- Status cache: formula, TTL, last-known/cold, `-race`
- Kill-switch: zero upstream, all-inactive disabled under chain[0]
- Admin: masking, whitelist, `enc:` 400, SSRF echo, 404, FOR UPDATE,
  concurrent two-field PUT, concurrent first-write (ON CONFLICT),
  explicit-empty-from-NULL, drift warnings, empty patch, pagination
- Saturation: 10/10 randomized blind iterations, 20 pkgs
  (`reports/saturation/`)

## 9. Known limits

- `status` is the only runtime field — key/URL/chain edits need restart
  (disclosed via `requires_restart`).
- KEK rotation is out of v1 scope (manual re-encrypt path documented).
- Scope is the 12 tx-history providers; non-tx providers are a data-add.
- `warnings` serializes `null` when empty (OpenAPI `type: array` —
  consistent with existing behavior; reviewer observation, non-blocking).
