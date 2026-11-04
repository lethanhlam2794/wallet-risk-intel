# Feature Map & Operational Guidebook — Transaction-History Provider Expansion

Delivered via the 6-stage Codex Vibe pipeline (plan:
`plans/20261104-1100-tx-provider-expansion/`). Backend-only feature —
no UI surface; evidence is live API JSON + DB queries.

## 1. What ships

Eleven candidate transaction-history providers were evaluated; **ten
adapters** landed behind the existing `provider.TxProvider` contract,
plus an **ordered per-network quota-sharing failover chain** with
per-source index watermarks.

| Adapter | Chains | Key | Status |
|---|---|---|---|
| chainscan (Etherscan family) | ethereum, bsc | `ETHERSCAN_API_KEY` | pre-existing, ETH default |
| nodereal | bsc | `NODEREAL_API_KEY` | pre-existing, BSC default |
| blockscout | ethereum | keyless (`BLOCKSCOUT_BASE_URL`) | **live-verified** |
| alchemy | ethereum, bsc | `ALCHEMY_API_KEY` | implemented, key-gated |
| ankr | ethereum, bsc | `ANKR_API_KEY` | implemented, key-gated |
| covalent | bsc | `COVALENT_API_KEY` | implemented, key-gated |
| moralis | bsc | `MORALIS_API_KEY` | implemented, key-gated |
| tatum | bsc | `TATUM_API_KEY` | implemented, key-gated |
| bitquery | bsc | `BITQUERY_API_KEY` | probe 401 → implemented unverified |
| oklink | bsc | `OKLINK_API_KEY` | probe 401 → implemented unverified |
| tokenview | ethereum, bsc | `TOKENVIEW_API_KEY` | probe 401 → implemented unverified |
| chainbase | ethereum, bsc | `CHAINBASE_API_KEY` | probe 401 → implemented unverified |
| blockchair | — | — | **NOT-VIABLE** (no BSC coverage; `reports/blockchair-viability.md`) |

Unverified keyed adapters are excluded from every default selector —
explicit operator override still accepted.

## 2. Source map

| Concern | Path |
|---|---|
| Provider contract | `internal/provider/provider.go` (`TxProvider`, `Transfer`, `TransferLookupOpts`, `TransferPage`) |
| Adapters | `internal/provider/{alchemy,ankr,bitquery,blockscout,chainbase,covalent,moralis,oklink,tatum,tokenview}/` |
| Failover chain walk | `internal/transaction/service.go` (`ensure`, `attemptFetch`, `releaseClaim`, `failoverKinds`) |
| Ordered selector resolution | `cmd/api/main.go` (`selectTxProviderChain`) |
| Per-source index state | `migrations/000019_wallet_index_state_source_key.up.sql`, `queries/transactions.sql` |
| Config/env | `internal/config/config.go`, `.env.example` |
| HTTP surface | `internal/httpserver/transactions.go` |
| Failover design doc | `plans/.../reports/phase-12-design.md` (D1–D5 decisions) |

## 3. Configuration contract

| Env | Effect |
|---|---|
| `TX_PROVIDER_ETH` | comma-separated ordered chain; unset → `chainscan` |
| `TX_PROVIDER_BSC` | comma-separated ordered chain; unset → `nodereal` |
| `<ADAPTER>_API_KEY` / `_BASE_URL` / `_ENABLED_CHAINS` | per-adapter enablement & scoping |
| `TXINDEX_BUDGET_<ADAPTER>` | per-adapter per-request upstream-call cap (default 8; ETH/BSC defaults 32/8) |

Boot invariants: unknown selector names fail boot; every registered
provider must resolve a `data_sources` row and a positive budget.

## 4. Failover semantics (phase 12)

- **Fail over on**: `rate_limited`, `upstream_5xx`, `degraded`,
  `transport`, `circuit_open`.
- **Never on**: empty successful page, `client_4xx`, `malformed`,
  `timeout`, `internal`, `disabled`.
- Partial batches from a failed primary are **discarded** before the
  secondary retries with its own head/window/token/source row.
- Empty successful page = **terminal success** (never a failure).
- Disabled adapters produce **zero** upstream traffic.
- All-disabled → honest `disabled` state; all-fail → the last
  non-disabled failure kind persists on that source's own row.
- Request-global budget pool = max configured chain budget; each
  adapter's own cap still enforced; only real upstream calls charged.

## 5. Step-by-step operations guidebook

### 5.1 Boot with an ordered failover chain

```bash
HTTP_ADDR=:8090 TX_PROVIDER_ETH=chainscan,blockscout go run ./cmd/api
```

Boot log proves chain resolution (captured
`reports/stage5/00-boot-chain-resolution.log`):

```json
{"msg":"tx provider failover chain resolved","env":"TX_PROVIDER_ETH","order":["chainscan","blockscout"]}
```

### 5.2 Exercise live indexing

```bash
curl -s -H "X-API-Key: $READER_KEY" \
  "http://127.0.0.1:8090/api/v1/wallets/ethereum/0x0000000000000000000000000000000000012345/transactions?refresh=1"
```

Live capture (`reports/stage5/03-failover-walk-live.json`,
HTTP 200, 1.61s): `provider_status:"ok"`, `index_status:"fresh"`,
`indexed_until_block:26340489`, `rows_indexed:7` — real ERC-20 rows
2017–2025.

### 5.3 Disabled-primary skip (live failover evidence)

`TX_PROVIDER_ETH=nodereal,blockscout` on Ethereum: nodereal reports
`Enabled(ethereum)=false` → zero upstream traffic → blockscout serves
(`reports/stage5/06-disabled-skip-secondary-serves.json`):
`fresh/ok`, `indexed_until_block:26340492`, `rows_indexed:7`.

### 5.4 Honest-partial evidence

Ultra-busy wallet (`0xd8dA…6045`) under the keyless drain bound
(`reports/stage5/02-eth-vitalik-transactions.json`):
`index_status:"partial"`, `history_truncated:true`,
`provider_status:"ok"`, `rows_indexed:0` — unfetched coverage is
reported, never claimed.

### 5.5 Verify per-source watermarks in the DB

```sql
SELECT s.code AS source, wis.status, wis.indexed_until_block,
       wis.history_truncated, wis.rows_indexed, w.address, n.code AS net
FROM wallet_index_state wis
JOIN data_sources s ON s.id = wis.source_id
JOIN wallets w ON w.id = wis.wallet_id
JOIN blockchain_networks n ON n.id = wis.network_id
ORDER BY wis.updated_at DESC;
```

Verified live (`reports/stage5/04-per-source-state.sql.txt`): wallet
`0x…12345` carries **two independent rows** — `chainscan|fresh|26340489`
and `blockscout|fresh|26340492` — proving per-source watermark
isolation post-re-key `(network_id, wallet_id, source_id)`.

### 5.6 Verify the re-keyed PK

```sql
\d wallet_index_state  -- PK: (network_id, wallet_id, source_id)
```

Migration 000019 up+down verified on dedicated test DBs.

## 6. API contract (unchanged surface)

`GET /api/v1/wallets/{network}/{address}/transactions[?refresh=1]`
returns `items[]`, `index_status`, `provider_status`,
`indexed_until_block`, `head_block`, `history_truncated`,
`rows_indexed`, `rows_skipped{missing_contract,invalid_address}` —
provenance unchanged; only the serving source differs per the chain.

## 7. Test evidence index

- Failover suite: `internal/transaction/failover_test.go` (13 tests)
- Adapter suites: `internal/provider/*/adapter_test.go` (real wire shapes)
- Regression tests: head-lag release preservation, releaseClaim unit,
  tokenview catch-up-cap dead end, bitquery NFT drop
- Blind saturation: `plans/.../reports/saturation/` (10/10, shuffled)
- Review trail: `reports/review-round-1.md` → `remediation-round-1.md`
  → `review-round-2.md` (APPROVED)

## 8. Operator notes / known limits

- Keyed adapters are inert until keys arrive; `live-verify` checklist in
  `plan.md` tracking table.
- Blockchair permanently NOT-VIABLE for BSC — do not add to selectors.
- Advisory (review round 2): `MarkWalletIndexing` is an unconditional
  upsert — cross-process `releaseClaim` can stale-restore a concurrent
  terminal write (understate-only, self-healing). Optional hardening:
  conditional release `WHERE status='indexing'`.
- Blockscout/tokenview newest-first scans drain in bounded pages;
  deep-history wallets may stay honest-PARTIAL across refreshes.
- Down-migration 000019 collapses per-source rows to the freshest per
  pair — one-way lossy by design.
