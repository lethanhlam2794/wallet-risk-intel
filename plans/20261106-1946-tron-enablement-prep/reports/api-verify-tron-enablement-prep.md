# Stage 5 API Verification — TRON enablement prep

**Surface classification**: `API` — `GET /api/v1/wallets/:network/:address` exposes
`address_type` in its response (`internal/httpserver/server.go:299`, openapi
`address_type: { type: string }`, example `address_type: evm`). The wallet
handler is one of the four `GetOrCreate` call sites migrated to the
`net.AddressFormat()` signature, so the stamping change is live-verifiable
through this endpoint. No new/modified route — same endpoint, same contract;
EVM stamping must remain byte-identical and inactive TRON must remain
unreachable.

**Service under test**: `go run ./cmd/api` on `:8010` against dev stack
(`postgres://…:5433/check_wallet`, `redis://…:6380`), auth via dev bootstrap
reader key (`API_BOOTSTRAP_READER_KEY` env — the documented dev path).
Captured live 2026-11-06 ~20:55 (+07:00).

## Request/response matrix (raw captures)

### 1. `GET /api/v1/networks` — active networks only (TRON absent)

```
curl -sS 'http://127.0.0.1:8010/api/v1/networks' -H 'X-API-Key: <reader-key>'
```

```json
{"data":[{"code":"bsc","name":"BNB Smart Chain","chain_id":"eip155:56","namespace":"eip155"},{"code":"ethereum","name":"Ethereum Mainnet","chain_id":"eip155:1","namespace":"eip155"}]}
HTTP 200 · 0.061562s
```

**Asserted**: `tron` and `solana` absent — inactive rows never surface.

### 2. `GET /api/v1/wallets/ethereum/0x…fee1` — happy path (live `GetOrCreate`)

```
curl -sS 'http://127.0.0.1:8010/api/v1/wallets/ethereum/0x000000000000000000000000000000000000fee1' -H 'X-API-Key: <reader-key>'
```

```json
{"data":{"address":"0x000000000000000000000000000000000000fee1","address_type":"evm","data_status":"AVAILABLE","first_seen_at":"2026-11-06T13:54:42.828181Z","labels":[],"labels_count":0,"last_synced_at":"2026-11-06T00:06:21.600905Z","network":"ethereum","normalized_address":"0x000000000000000000000000000000000000fee1"}}
HTTP 200 · 0.258412s
```

**Asserted**: `address_type:"evm"` — byte-identical EVM stamping through the
new `GetOrCreate(ctx, net, address, normalized)` signature.

### 3. `GET /api/v1/wallets/bsc/0x…fee1` — second eip155 network

```json
{"data":{"address":"0x000000000000000000000000000000000000fee1","address_type":"evm","data_status":"AVAILABLE","first_seen_at":"2026-11-06T13:54:43.072593Z","labels":[],"labels_count":0,"last_synced_at":"2026-11-06T00:06:21.600905Z","network":"bsc","normalized_address":"0x000000000000000000000000000000000000fee1"}}
HTTP 200 · 0.073531s
```

**Asserted**: BSC also stamps `evm` via the namespace→format derivation.

### 4. `GET /api/v1/wallets/tron/T…` — inactive network fail-closed

```
curl -sS 'http://127.0.0.1:8010/api/v1/wallets/tron/TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t' -H 'X-API-Key: <reader-key>'
```

```json
{"error":{"code":"INVALID_NETWORK","message":"unsupported network","request_id":"b4056be3-bcb4-4dae-9578-26322f93530b"}}
HTTP 400 · 0.035484s
```

**Asserted**: resolver only loads `is_active` networks — a valid TRON base58
address on `network=tron` is rejected before `GetOrCreate` runs. TRON stays
code-complete but INACTIVE.

### 5. `GET /api/v1/wallets/ethereum/notanaddress` — invalid address

```json
{"error":{"code":"INVALID_ADDRESS","message":"invalid address","request_id":"7ab3ad67-bd52-4d24-b39c-574726c20021"}}
HTTP 400 · 0.036543s
```

### 6. `GET /api/v1/wallets/ethereum/0x…fee1` — no credentials

```json
{"error":{"code":"UNAUTHORIZED","message":"authentication required","request_id":"3170d3d4-c702-4b4a-b9ed-62e15f3c7e4f"}}
HTTP 401 · 0.000564s
```

## DB verification + cleanup

```sql
SELECT w.id, n.code AS network, w.address, w.address_type
FROM wallets w JOIN blockchain_networks n ON n.id=w.network_id
WHERE w.normalized_address='0x000000000000000000000000000000000000fee1';
-- 117935 | ethereum | …fee1 | evm
-- 117936 | bsc      | …fee1 | evm

SELECT code, namespace, is_active FROM blockchain_networks
WHERE code IN ('tron','solana','ethereum','bsc');
-- ethereum|eip155|t  bsc|eip155|t  solana|solana|f  tron|tron|f  ← TRON INACTIVE

DELETE FROM wallets WHERE normalized_address='0x…fee1' AND id IN (117935,117936);
-- DELETE 2; remaining = 0 — dev DB restored to pre-verification state.
```

## Verdict

6/6 request cases asserted (happy ×2, inactive-network 400, invalid-address
400, auth 401, networks list). Latency ≤ 0.26s. EVM `address_type` stamping
verified end-to-end through the live endpoint; TRON remains dormant and
unreachable.
