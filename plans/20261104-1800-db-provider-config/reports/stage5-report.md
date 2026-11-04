# Stage 5 Report — SKIPPED-NO-UI with live-API substitution

## Skip justification

This change has **no UI surface** — the repository is a Go REST API +
worker (Gin + pgx + Redis); the feature delivers admin REST endpoints
only. Per pipeline rules, Stage 5 is `SKIPPED-NO-UI`; substitute
evidence = live API traffic + latency + masked-secret verification +
kill-switch behavior against a real failover chain + DB state.

## Environment

- API: `go build ./cmd/api` → `/tmp/cw-api`, `HTTP_ADDR=:8090` (8080 is
  host-bound by an unrelated `gateway` process), dev DB
  `check_wallet@127.0.0.1:5433` migrated through 000020 (+ ALTER width
  convergence), Redis `6380/0`
- Chain under test: `TX_PROVIDER_ETH=chainscan,blockscout` (boot log
  `tx provider failover chain resolved`)
- Auth: `X-API-Key` dev bootstrap admin/reader keys (documented local
  seeds, never real credentials)
- Wallet fixture: `0x0000000000000000000000000000000000012345`
  (pre-indexed under BOTH chainscan|fresh|26340489 and
  blockscout|fresh|26340492)

## Captures (`reports/stage5/`)

| File | Evidence |
|---|---|
| `00-boot-resolution.log` | `provider config resolved` per provider — origins only (`env`/`default`), never values; `TX_PROVIDER_ETH` chain resolved |
| `01-list.json` | `GET /api/v1/admin/provider-configs` → 200, 7.5ms, 12 providers all `configured:false`, masked `{"set":false,"last4":null}`, `requires_restart` disclosure |
| `02-get-chainscan-before.json` | `GET /chainscan` → 200, 2.5ms, unconfigured view |
| `03-put-covalent.json` | `PUT /covalent {api_key, base_url, enabled_chains}` → 200, 8.4ms — `{"set":true,"last4":"7777"}` masked; `warnings[]` = `requires_restart` on api_key+enabled_chains (stored≠boot snapshot); **no warning for base_url — stored == boot-resolved env default** (conditional warning proven) |
| `04-changes.json` | `GET /covalent/changes` → 200, 4.3ms — 3 masked audit rows, `actor_id:"bootstrap-admin"`, `actor_type:"api_client"`, `previous_value:null`/`{"set":false}` |
| `05-validation.txt` | reader→403, no-key→401, unknown code→404, `https://evil.example.com`→400 echoing `"allowed_hosts":["api.covalenthq.com"]` |
| `06-sql-before.txt` | fixture wallet state: `blockscout|fresh|26340492`, `chainscan|fresh|26340489` |
| `07-killswitch-flip.json` | `PUT /chainscan {status:inactive}` → 200, 16.6ms — `effective_active:false`; `warnings:null` (**no last_active_member warning — blockscout still active**, conditional logic proven) |
| `08-tx-after-flip.json` | after ~33s TTL: `GET /wallets/ethereum/0x…12345/transactions?refresh=1` → 200, **2.30s**, `fresh/ok`, `indexed_until_block:26342823`, `rows_indexed:7` — served by blockscout |
| `09-sql-after.txt` | **kill-switch proof**: `blockscout 26340492→26342823` (fresh fetch); `chainscan` frozen `26340489`/`indexed_at` unchanged — zero upstream spend on the flipped provider, exact disabled-skip semantics |
| `10-flip-back.json` | `PUT {status:active}` → 200 — `effective_active:true`, no restart needed (runtime field) |
| `11-sql-dump.txt` | live `\x` dump — `provider_configs` rows (api_key shown only as length), `provider_config_changes` masked `{set,last4}` — plaintext key never persisted in audit |
| `12-cleanup.txt` | `DELETE 2` — dev rows removed, env parity restored (`remaining_rows=0`) |

## Latency

- `/networks`: **6.0ms** · list: **7.5ms** · GET one: **2.5ms**
- PUT writes: **8.4–16.6ms** · changes page: **4.3ms**
- Transactions (indexed, warm): **2.30s** — upstream-bound (blockscout),
  within envelope. Zero 5xx, zero error responses beyond asserted
  400/401/403/404.

## Secret hygiene

- api_key never leaves the surface: wire `{"set","last4"}` only; audit
  rows masked identically; SQL dump shows length placeholder only.
- Boot log prints origins (`env`/`default`), never values; headers are
  never logged (`access_log` verified in prior audits).
- Stage-5 key `fake-stage5-key-7777` is an obvious fixture; both dev rows
  deleted post-capture (env parity restored).

## Console/runtime error audit

Boot log clean: expected `disabled` notices for unkeyed adapters +
resolution/chain lines only. Zero panics, zero unhandled errors during
all calls. No CORS surface (no browser client).
