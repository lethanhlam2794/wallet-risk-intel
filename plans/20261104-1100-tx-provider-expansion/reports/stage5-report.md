# Stage 5 Report — SKIPPED-NO-UI with live-API substitution

## Skip justification

This change has **no UI surface** — the repository is a Go REST API +
worker (Gin + pgx + Redis). There is no frontend, admin console, or
browser-renderable page anywhere in the diff. Per pipeline rules, Stage
5 is `SKIPPED-NO-UI`; the required substitute evidence is live API
traffic + latency + secret-hygiene + DB state, all captured below.

## Environment

- API: `go run ./cmd/api`, `HTTP_ADDR=:8090`, dev DB
  `check_wallet@127.0.0.1:5433` (migrated through 000019), Redis `6380/0`
- Auth: `X-API-Key` dev bootstrap reader key (dev-only seed, never a
  real credential)

## Captures (`reports/stage5/`)

| File | Evidence |
|---|---|
| `00-boot-chain-resolution.log` | `TX_PROVIDER_ETH=chainscan,blockscout` resolves the ordered chain at boot |
| `01-networks.json` | `GET /api/v1/networks` → 200, 3.7ms |
| `02-eth-vitalik-transactions.json` | `?refresh=1` on ultra-busy wallet → 200, 10.39s, **honest PARTIAL**: `provider_status:"ok"`, `index_status:"partial"`, `history_truncated:true`, `rows_indexed:0` — unfetched coverage reported, never claimed |
| `03-failover-walk-live.json` | `?refresh=1` on `0x…12345` via primary → 200, 1.61s, `fresh`, `indexed_until_block:26340489`, `rows_indexed:7`, real ERC-20 rows 2017–2025 |
| `04-per-source-state.sql.txt` | Live SQL: same wallet carries `chainscan\|fresh\|26340489` **and** `blockscout\|fresh\|26340492` — per-source watermark isolation post-re-key; plus `wallet_index_state` PK `(network_id,wallet_id,source_id)` |
| `05-boot-chain-nodereal-blockscout.log` | `TX_PROVIDER_ETH=nodereal,blockscout` resolves |
| `06-disabled-skip-secondary-serves.json` | **Live disabled-skip**: nodereal `Enabled(eth)=false` → zero traffic → blockscout serves → `fresh/ok`, `rows_indexed:7`, 1.90s |

## Latency

- `GET /networks`: **3.7ms**
- Transactions (indexed, warm): **1.61s / 1.90s** — upstream-bound,
  within budget envelope
- Deep-history partial (bounded drain): 10.39s, honestly `partial`
- Zero 5xx, zero errors observed; all responses 200 with provenance.

## Secret hygiene

- No keys in code/commits/tests; `.env.example` placeholders only.
- Dev bootstrap keys (`dev-*-change-me`) are local seeds documented in
  README — never real credentials, never logged (`access_log.go` never
  logs headers).

## Console/runtime error audit

Boot log reviewed: only expected `disabled` notices for unkeyed
adapters + chain-resolution lines. No panics, no unhandled errors,
no CORS concerns (no browser surface).
