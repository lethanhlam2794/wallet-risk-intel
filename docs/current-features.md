# Check-Wallet — Tính năng hiện có (feature inventory)

> Snapshot trạng thái code tại 2026-11-06 (nhánh đang có TRON groundwork
> uncommitted). Tài liệu thiết kế gốc: `docs/tailieu.md` (spec v2.0).
> Danh mục chi tiết theo feature: `apps/api/docs/feature-maps/`.

**Sản phẩm**: Blockchain Wallet Risk Intelligence — monorepo Go + PostgreSQL
(+ Redis, Gin, sqlc, golang-migrate), trả lời bằng *bằng chứng*: ví có nằm
trong danh sách trừng phạt không, nhãn rủi ro nào, tiếp xúc trực tiếp/gián
tiếp với địa chỉ rủi ro ra sao (tx nào chứng minh), điểm rủi ro tính theo
policy version nào, dữ liệu tươi/đầy đủ cỡ nào.

**Networks hoạt động**: Ethereum, BSC (`is_active=true`).
**Đã đăng ký nhưng inactive**: Solana, TRON (groundwork only).

---

## 1. API surface (`/api/v1`)

### Public
| Endpoint | Tính năng |
|---|---|
| `GET /healthz` | Liveness |
| `GET /readyz` | Readiness — kiểm tra Postgres + Redis (degraded ≠ dead) |
| `POST /api/v1/auth/login` | Đăng nhập admin system-user → JWT (có rate limit) |

### Reader/Admin (X-API-Key hoặc JWT)
| Endpoint | Tính năng |
|---|---|
| `GET /networks` | Danh sách networks **active** (inactive không lộ ra ngoài) |
| `GET /wallets/:network/:address` | Hồ sơ ví: `address_type`, `data_status`, `labels_count`, `first_seen/last_synced`. `GetOrCreate` — tạo row khi gặp lần đầu |
| `GET /wallets/:network/:address/labels` | Nhãn của ví kèm provenance/freshness |
| `GET /wallets/:network/:address/enrichment` | Enrichment live từ providers (GoPlus, PublicAML, Chainabuse) |
| `GET /wallets/:network/:address/transactions` | Giao dịch đã index (cursor pagination, EnsureIndexed on-demand) |
| `GET /wallets/:network/:address/exposures` | Tracing BFS — exposure 1-hop/2-hop kèm tx evidence + coverage/truncation metadata |
| `GET /sanctions/wallets/:network/:address` | Tra cứu sanctions của ví (exact match + chain-specific semantics) |
| `GET /sanctions/entities/:id` | Chi tiết entity trừng phạt |
| `GET /sanctions/programs/:code` | Chi tiết program trừng phạt |
| `POST /screenings` | Screening tổng hợp — idempotent qua `Idempotency-Key` |
| `GET /screenings/:id` | Kết quả screening (status + score + policy version + evidence) |
| `GET /metrics` | Prometheus scrape (reader+) |

### Admin (role `admin`; `system-users` cần `superadmin`)
| Endpoint | Tính năng |
|---|---|
| `POST /admin/ingestion-runs` · `GET /admin/ingestion-runs/:id` | Trigger + theo dõi ingestion run |
| `GET/PUT /admin/provider-configs[/:code]` · `GET …/changes` | Quản trị provider config trong DB (api key, base url, enabled chains) + audit trail |
| `POST /admin/provider-configs/:code/test` | Probe live một provider config |
| `GET/POST/PATCH /admin/clients[/:id]` | Quản trị API clients (tạo key, role, enable/disable) |
| `GET/POST /admin/system-users` | Quản trị system users (superadmin) |

Conventions: envelope `{data|error}`, `request_id` mọi response, error code
ổn định (`INVALID_NETWORK`, `INVALID_ADDRESS`, `UNAUTHORIZED`…), cursor
pagination, UTC timestamps.

## 2. Domain modules

### Sanctions (OFAC) — `internal/sanctions`, `internal/provider/ofac`
- Bulk snapshot SDN Advanced XML: download → validate (status/size/checksum/XML
  streaming decode) → stage → publish **atomic**; snapshot hỏng không bao giờ
  thay snapshot đang chạy; records chỉ `revoked_at` sau snapshot hợp lệ.
- Exact-match lookup kèm entity/program/references + `reason_status`
  (`NOT_AVAILABLE` khi nguồn không công bố lý do — không bịa lý do).
- **Chain-specific semantics**: listing ETH không thành listing BSC
  (`CHAIN_SPECIFIC`); cross-chain chỉ là observation được gắn nhãn rõ.

### Labels — `internal/label`, `internal/provider/walletattribution`
- Ingest `prettydeath/wallet-attribution` CSV qua staging + fingerprint ổn
  định (idempotent, không duplicate qua nhiều run).
- `reported` vẫn là `reported` — không auto-verify; EXCHANGE/GAMBLING là
  classification, không tự động làm tăng risk.

### Enrichment — `internal/enrichment` + providers goplus/publicaml/chainabuse
- Tra cứu live theo `provider:{source}:{net}:{addr}` cache; provider lỗi →
  `PARTIAL`/visible, không bao giờ giả "clean".

### Screening + Scoring — `internal/screening`, `internal/scoring`
- Tổng hợp sanctions + labels + enrichment + exposure → `risk_score` 0–100
  (heuristic), `scoring_policy_version` trên mọi row (reproducible).
- Statuses: `COMPLETED` / `PARTIAL` / `INSUFFICIENT_DATA` / `FAILED`
  (`risk_score` NULL khi insufficient — `0` ≠ safe).
- Idempotent create, direct-sanctions flag tách khỏi điểm số.

### Transactions + Indexer — `internal/transaction`
- `EnsureIndexed → TxProvider → watermark`: index on-demand theo block
  window, watermark = min per-stream contiguous coverage.
- Identity `n|t:hash.k` (content-hash + occurrence suffix) → ổn định qua
  refetch, unique dưới `UNIQUE(network_id, tx_hash, transfer_index)`.
- **Budget provider theo request**: mỗi adapter `max_provider_requests`,
  meter bằng `UpstreamCalls` thật, hết budget → dừng *thành thật*
  (`BudgetExhausted`, báo partial coverage — không cháy quota lén).
- Per-stream (`native`/`token`) page token + `Finished` status; ascending
  block order enforce ở contract.

### Tracing / Exposures — `internal/tracing`
- Bounded BFS 1-hop/2-hop: `MaxHops`, `MaxNodesPerDepth`, `MaxPages`,
  `ResponseCap`, query timeout — không bao giờ crawl vô hạn.
- Mọi path edge mang tx evidence (hash, type, asset, amount_raw, block);
  response báo coverage/truncation. 1-hop **không** chứng minh ownership.

### Warm index — `internal/warmindex` + `internal/tracing/warm.go`
- **Deep-index theo tín hiệu, không quét tràn** (chống lãng phí quota):
  counterparty hop-1 chỉ vào queue khi có *direct sanctions match*
  (priority 100) hoặc risky label `{SCAM,PHISHING,HACK,RANSOMWARE,MIXER}`
  (priority 50). GAMBLING/EXCHANGE/… không đủ điều kiện.
- Upsert: tín hiệu mới chỉ tăng priority; feature flag
  `WARM_INDEX_ENABLED` (off = no-op); fail-open (ctx tách 2s).
- Worker `-mode warmdrain` poll queue nền.

### Provider layer — `internal/provider/`
- Contracts: `Provider` / `BulkSource` / `SnapshotSource` / `TxProvider`.
- **Resilience toolkit** dùng chung (`internal/provider/resilience`):
  timeout, rate limit, bounded retry + `Retry-After`, circuit breaker,
  concurrency cap, `MaxBodyBytes`, SSRF host allowlist, metrics hooks.
- TxProvider failover chain theo env `TX_PROVIDER_<NET>`: registry hiện có
  chainscan, nodereal, alchemy(eth/bsc), blockscout(+pro), ankr, covalent,
  moralis, tatum, bitquery, oklink, tokenview, chainbase.
- `providerconfig`: cấu hình providers từ DB + env override + audit changes.

### Stablecoin freeze intel — `internal/provider/stablefreeze`
- Quét freeze events USDT/USDC EVM-only; publish `EnsureWallets` từ scan.

### Platform & middleware — `internal/platform`, `internal/middleware`
- Auth: `IdentifyClient` (X-API-Key → sha256 key_hash) + `IdentifyUser`
  (JWT admin); `RequireRole`; rate limit (client/anon buckets);
  `RequestID`, `AccessLog`, `Recovery`, `MaxBodyBytes`.

## 3. Worker & ops
- `cmd/api` — REST API (`HTTP_ADDR`, mặc định `:8010`).
- `cmd/worker` — ingestion snapshot sync (`-source ofac_sdn [-file]`)
  hoặc `-mode warmdrain`; ingestion run có heartbeat 30s.
- `cmd/migrate` — embedded migration runner (26 migrations hiện tại).
- Dev stack `docker-compose` (postgres :5433, redis :6380); test DBs bắt
  buộc hậu tố `_test`; bootstrap dev API keys qua
  `API_BOOTSTRAP_{ADMIN,READER}_KEY`.
- Observability: `slog` JSON, Prometheus metrics, request-id tracing.

## 4. Trạng thái non-EVM (groundwork, §15-gated)

| Item | Trạng thái |
|---|---|
| `network.AddressFormat` (eip155→evm, solana→solana, tron→tron) | ✅ live — `wallets.address_type` stamp theo format, fail-closed namespace lạ |
| Solana/TRON network rows | ✅ registered, `is_active=false` — `/networks` không lộ |
| TronGrid TxProvider adapter | ✅ **dormant** — code-complete + 35 tests, không import nơi nào, `data_sources` không có row |
| OFAC TRX listing, `tron.csv`, `NormalizeEVM`→`Normalize(net)`, selector wiring, OpenAPI pattern | ⛔ §15 blockers — chưa làm, đã liệt kê trong `docs/non-evm-audit.md` |

## 5. Invariants sản phẩm (không bao giờ vi phạm)
- "Không có dữ liệu" ≠ "an toàn"; provider fail ≠ clean result; `score=0` ≠ safe.
- Không bịa sanctions reason/entity/reference; `reported` ≠ `verified`.
- EXCHANGE/GAMBLING là classification; 1-hop exposure ≠ ownership.
- ETH listing không biến thành BSC listing (chain-specific).
- Amount chỉ là integer thô (`NUMERIC`), không float; mọi SQL parameterized.
- Bằng chứng không xóa: snapshot chỉ publish khi validated đầy đủ.
