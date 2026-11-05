
# BLOCKCHAIN WALLET RISK INTELLIGENCE

## Technical Design Specification — Golang + PostgreSQL

**Version:** 2.0
**Status:** Proposed Architecture
**Scope:** Backend, Database, REST API, Sanctions Intelligence, Wallet Attribution, Transaction Tracing
**Primary Networks:** Ethereum, BNB Smart Chain (BSC)
**Architecture:** Modular Monolith

---

# 1. MỤC TIÊU DỰ ÁN

Xây dựng hệ thống Blockchain Wallet Risk Intelligence nhằm thu thập, chuẩn hóa, lưu trữ, đối chiếu và phân tích thông tin rủi ro của các địa chỉ ví blockchain.

Hệ thống cung cấp REST API để các đối tác kiểm tra địa chỉ ví và nhận được kết quả có bằng chứng, nguồn dữ liệu và lý do đánh giá rõ ràng.

## 1.1. Những câu hỏi hệ thống phải trả lời

1. Ví có nằm trong danh sách trừng phạt OFAC hoặc danh sách được hỗ trợ khác không?
2. Nếu bị trừng phạt, chủ thể liên quan là ai?
3. Chủ thể bị trừng phạt theo chương trình nào?
4. Có thông tin chính thức nào giải thích bối cảnh hoặc lý do trừng phạt không?
5. Ví có bị gắn nhãn SCAM, PHISHING, HACK, RANSOMWARE không?
6. Ví có thuộc MIXER, GAMBLING, EXCHANGE hoặc dịch vụ khác không?
7. Ví có giao dịch trực tiếp với địa chỉ rủi ro không?
8. Ví có liên kết gián tiếp với địa chỉ rủi ro qua nhiều giao dịch không?
9. Liên kết đó có bao nhiêu hops?
10. Có những transaction hash nào chứng minh đường đi?
11. Điểm rủi ro được tính như thế nào?
12. Dữ liệu được cập nhật khi nào?
13. Kết quả nào đã xác minh và kết quả nào chỉ là nhận định của nhà cung cấp?
14. Có đủ dữ liệu để kết luận hay không?

## 1.2. Nguyên tắc cốt lõi

- Không phụ thuộc duy nhất vào một nhà cung cấp.
- Ưu tiên nguồn dữ liệu miễn phí và có thể sử dụng hợp pháp.
- Không tự động sử dụng dịch vụ trả phí.
- Mọi nhãn phải truy vết được nguồn.
- Phân biệt nhãn trực tiếp với liên kết giao dịch gián tiếp.
- Không đánh đồng "không có dữ liệu" với "an toàn".
- Không đánh đồng địa chỉ từng giao dịch với hacker là địa chỉ thuộc hacker.
- Không đánh đồng kết quả không khớp sanctions với ví an toàn.
- Không tự suy diễn lý do trừng phạt khi nguồn chính thức không công bố.
- Không tự gán chủ sở hữu ví chỉ dựa vào giao dịch.
- Mọi kết quả đánh giá phải có trạng thái độ đầy đủ dữ liệu.

---

# 2. TECHNOLOGY STACK

## 2.1. Backend

- Language: Golang
- Framework: Gin
- Database: PostgreSQL
- Database Driver: pgx
- SQL Generation: sqlc
- Cache: Redis
- Background Workers: Go
- Migrations: golang-migrate
- API Specification: OpenAPI 3.1
- Logging: slog
- Metrics: Prometheus

## 2.2. Architecture

Sử dụng Modular Monolith.

Các module chính:

1. Wallet Management
2. Blockchain Network Management
3. Data Source Management
4. Sanctions Intelligence
5. Wallet Attribution
6. Transaction Indexing
7. Transaction Tracing
8. Risk Scoring
9. Screening
10. Data Ingestion
11. API Authentication
12. Audit and Monitoring

Không triển khai microservices trong giai đoạn MVP.

---

# 3. PHẠM VI HỆ THỐNG

## 3.1. In Scope

- Quản lý blockchain networks.
- Quản lý địa chỉ ví.
- Đồng bộ danh sách OFAC SDN.
- Liên kết địa chỉ ví với chủ thể bị trừng phạt.
- Lưu chương trình trừng phạt và nguồn tham chiếu.
- Tra cứu lý do hoặc bối cảnh trừng phạt khi có nguồn chính thức.
- Thu thập wallet labels từ nguồn công khai.
- Tích hợp API enrichment tùy chọn.
- Lưu transaction edges.
- Phân tích direct exposure.
- Thiết kế hỗ trợ multi-hop exposure.
- Tính điểm rủi ro theo policy.
- Cung cấp REST API.
- Lưu lịch sử screening.
- Theo dõi ingestion và provider health.

## 3.2. Out of Scope — MVP

- Tự xây dựng blockchain full node.
- Index toàn bộ blockchain ngay từ đầu.
- Phân tích toàn bộ giao dịch DeFi.
- Truy vết xuyên chuỗi qua bridge.
- Chứng minh chính xác nguồn gốc từng đơn vị tài sản qua mixer.
- Machine learning tự nhận diện hacker.
- Tự động đóng băng tài sản.
- Đưa ra kết luận pháp lý cuối cùng.

---

# 4. DATA SOURCES

## 4.1. OFAC SDN — PRIMARY SANCTIONS SOURCE

### Purpose

Sử dụng dữ liệu chính thức của Office of Foreign Assets Control (OFAC) thuộc U.S. Department of the Treasury.

Mục tiêu:

- Phát hiện địa chỉ ví được OFAC liệt kê trực tiếp.
- Xác định chủ thể trong hồ sơ SDN.
- Xác định sanctions program.
- Lưu định danh bản ghi.
- Lưu nguồn dữ liệu chính thức.
- Lưu thông tin mô tả và tham chiếu liên quan.
- Theo dõi cập nhật và thay đổi của danh sách.

### Official source

Website:

https://ofac.treasury.gov/

Sanctions List Service:

https://sanctionslistservice.ofac.treas.gov/

Advanced XML export:

https://sanctionslistservice.ofac.treas.gov/api/PublicationPreview/exports/SDN_ADVANCED.XML

Endpoint trên là nguồn tải dữ liệu bulk.

Không phải REST API tra cứu từng địa chỉ ví.

### Download

```bash
curl -fL --retry 3 \
  "https://sanctionslistservice.ofac.treas.gov/api/PublicationPreview/exports/SDN_ADVANCED.XML" \
  -o sdn_advanced.xml
```

Có thể dùng resume nếu máy chủ hỗ trợ:

```bash
curl -fL -C - \
  "https://sanctionslistservice.ofac.treas.gov/api/PublicationPreview/exports/SDN_ADVANCED.XML" \
  -o sdn_advanced.xml
```

### Data characteristics

- File XML có dung lượng lớn, có thể vượt 100 MB.
- Chứa nhiều loại chủ thể và định danh.
- Không phải tất cả bản ghi đều liên quan đến cryptocurrency.
- Không phải tất cả bản ghi đều chứa địa chỉ ví.
- Một chủ thể có thể có nhiều địa chỉ ví.
- Một chủ thể có thể thuộc nhiều chương trình trừng phạt.
- Một bản ghi có thể có nhiều reference và identifier.

### Ingestion strategy

Không đọc toàn bộ XML vào RAM.

Sử dụng Go `encoding/xml.Decoder` để xử lý theo luồng.

Workflow:

1. Download XML vào file tạm.
2. Kiểm tra HTTP status.
3. Kiểm tra kích thước và file hoàn chỉnh.
4. Kiểm tra XML hợp lệ.
5. Ghi nhận checksum.
6. Parse từng entry.
7. Trích xuất entity.
8. Trích xuất sanctions programs.
9. Trích xuất digital currency identifiers.
10. Chuẩn hóa blockchain network.
11. Chuẩn hóa wallet address.
12. Liên kết wallet với entity.
13. Lưu source references.
14. Upsert vào staging tables.
15. Kiểm tra thống kê dữ liệu.
16. Publish snapshot mới.
17. Đánh dấu bản ghi không còn xuất hiện trong snapshot hợp lệ.
18. Ghi nhận ingestion result.

### Critical rules

Không thay thế snapshot hiện tại khi:

- Download thất bại.
- File bị cắt giữa chừng.
- XML không hợp lệ.
- Parser lỗi.
- Không đủ dữ liệu để xác định snapshot hoàn chỉnh.

Không suy diễn một địa chỉ trên BSC có bản ghi sanctions riêng chỉ vì địa chỉ EVM tương ứng được OFAC ghi là ETH.

Có thể cung cấp cross-chain address observation, nhưng phải phân biệt rõ với direct chain-specific listing.

---

## 4.2. SanctionsAI — OPTIONAL SANCTIONS VALIDATION

API đã thử nghiệm:

https://sanctionsai.dev/sanctions

Example:

```bash
curl \
  "https://sanctionsai.dev/sanctions?wallet=0x098B716B8Aaf21512996dC57EB0615e2383E2f96"
```

Observed response:

```json
{
  "matches": [
    {
      "list": "OFAC_SDN",
      "entity": "0x098B716B8Aaf21512996dC57EB0615e2383E2f96",
      "match_type": "wallet_exact",
      "confidence": 1.0,
      "detail": "Digital Currency Address - ETH"
    }
  ],
  "clean": false,
  "provider": "osint",
  "degraded": false
}
```

### Limitations

Trong lần thử nghiệm:

- Free tier: 5 requests mỗi kỳ hạn mức 24 giờ.
- Không trả về tên chủ thể thực sự.
- Không trả về sanctions program.
- Không trả về lý do trừng phạt.
- Không trả về toàn bộ hồ sơ SDN.

Không sử dụng làm nguồn dữ liệu chính của hệ thống thương mại.

Có thể sử dụng để đối chiếu trong phát triển nếu quota và điều khoản cho phép.

Không giả định quota hoặc điều khoản hiện tại sẽ giữ nguyên.

---

## 4.3. Wallet Attribution

Repository:

https://github.com/prettydeath/wallet-attribution

Purpose:

- Public wallet labels.
- Entity attribution.
- Scam/hack/phishing observations.
- Exchange labels.
- Mixer labels.
- Other service categories.

Potential categories:

- HACK
- SCAM
- PHISHING
- DRAINER
- RANSOMWARE
- GAMBLING
- MIXER
- EXCHANGE
- SANCTIONED

Rules:

- Giữ original source.
- Giữ original label.
- Giữ source URL.
- Giữ thời điểm dữ liệu được thu thập.
- Không tự coi dữ liệu tổng hợp là đã xác minh.
- Kiểm tra license trước khi dùng thương mại.
- Không tự coi mọi label là ground truth.

---

## 4.4. GoPlus Security

Purpose:

- Address security signals.
- Phishing indicators.
- Cybercrime indicators.
- Suspicious activity indicators.

Integration:

- On-demand adapter.
- Local caching.
- Provider-specific rate limiting.

Rules:

- Kiểm tra schema và chain support thực tế.
- Không suy diễn category từ field không liên quan.
- Field bằng 0 không có nghĩa ví an toàn.
- Provider failure không được chuyển thành clean result.

---

## 4.5. Chainabuse

Purpose:

- Community reports.
- Scam and phishing allegations.
- Additional wallet intelligence.

Integration:

- Chỉ sử dụng API khi có quyền truy cập.
- Kiểm tra quota và license.
- Phân biệt reported với verified.

Không tự động xem community report là kết luận xác thực.

---

## 4.6. PublicAML

Purpose:

- External risk enrichment.
- External risk score comparison.
- Direct attribution.
- Indirect exposure.
- Additional risk signals.

Integration:

- Optional adapter.
- Provider response caching.
- Raw response persistence.
- Degraded status tracking.

### Important distinctions

Direct attribution:

Ví được nguồn dữ liệu gắn nhãn trực tiếp.

Indirect exposure:

Ví có liên kết giao dịch với một địa chỉ hoặc chủ thể được gắn nhãn.

Provider risk score:

Điểm do nhà cung cấp tính theo chính sách riêng.

Không mặc định tương đương với điểm rủi ro nội bộ.

### Observed integration issues

Trong thử nghiệm trước đó:

- Có trường hợp cùng địa chỉ trả kết quả khác nhau giữa chain-specific và batch requests.
- Có phản hồi `degraded` với lỗi backend provider.
- Kết quả `risk.score = 0` trong trường hợp degraded không được hiểu là an toàn.
- API truy vấn `from` và `to` trực tiếp không chứng minh được đường đi multi-hop.

Implementation requirements:

- Lưu nguyên response.
- Lưu provider status.
- Lưu chain requested.
- Lưu thời điểm truy vấn.
- Không overwrite dữ liệu xác minh nội bộ bằng response degraded.
- Không tự coi điểm provider là ground truth.

---

## 4.7. Blockchain Transaction Data

Mục tiêu:

Thu thập các giao dịch cần thiết để phân tích liên kết giữa địa chỉ ví.

Potential sources:

- Blockscout
- Routescan
- Blockchain explorer APIs
- Public RPC endpoints
- Other providers with verified free tiers

Mỗi provider phải được kiểm tra:

- Chain support.
- API schema.
- Rate limit.
- Historical coverage.
- Token transfer support.
- Commercial usage terms.
- Availability.
- Cost.

Không mặc định public RPC hỗ trợ truy vấn lịch sử token transfers trực tiếp.

Đối với BEP-20/ERC-20, có thể cần event logs hoặc explorer indexing API.

---

# 5. SYSTEM ARCHITECTURE

```text
                    REST API CLIENT
                           |
                           v
                    GIN HTTP SERVER
                           |
                 AUTH / RATE LIMIT
                           |
                           v
                    SCREENING SERVICE
                           |
           +---------------+---------------+
           |               |               |
           v               v               v
      SANCTIONS        ATTRIBUTION       TRACING
       SERVICE          SERVICE         SERVICE
           |               |               |
           +---------------+---------------+
                           |
                           v
                     RISK ENGINE
                           |
                           v
                      POSTGRESQL
                           |
                           v
                       REDIS
```

Background workflow:

```text
                    DATA SOURCES
                         |
        +----------------+----------------+
        |                |                |
        v                v                v
       OFAC         Attribution       Other APIs
        |                |                |
        +----------------+----------------+
                         |
                         v
                    INGESTION
                         |
                         v
                    VALIDATION
                         |
                         v
                   NORMALIZATION
                         |
                         v
                    STAGING
                         |
                         v
                     PUBLISH
                         |
                         v
                    POSTGRESQL
```

---

# 6. PROJECT STRUCTURE

> Monorepo note: the tree below is the Go backend module root, which lives
> at `apps/api/` in the repository root (see AGENTS.md §3).

```text
wallet-risk/
|
|-- cmd/
|   |-- api/
|   |   `-- main.go
|   `-- worker/
|       `-- main.go
|
|-- internal/
|   |-- config/
|   |
|   |-- platform/
|   |   |-- database/
|   |   |-- cache/
|   |   `-- logger/
|   |
|   |-- network/
|   |-- wallet/
|   |-- entity/
|   |-- sanctions/
|   |   |-- handler.go
|   |   |-- service.go
|   |   |-- repository.go
|   |   `-- model.go
|   |
|   |-- attribution/
|   |-- label/
|   |-- screening/
|   |-- scoring/
|   |-- tracing/
|   |-- transaction/
|   |
|   |-- provider/
|   |   |-- ofac/
|   |   |   |-- downloader.go
|   |   |   |-- parser.go
|   |   |   |-- mapper.go
|   |   |   `-- importer.go
|   |   |-- walletattribution/
|   |   |-- goplus/
|   |   |-- chainabuse/
|   |   |-- publicaml/
|   |   `-- blockscout/
|   |
|   |-- ingestion/
|   |   |-- service.go
|   |   |-- worker.go
|   |   `-- scheduler.go
|   |
|   `-- middleware/
|       |-- auth.go
|       `-- rate_limit.go
|
|-- migrations/
|-- queries/
|-- api/
|   `-- openapi.yaml
|-- configs/
|-- tests/
|-- go.mod
|-- .env.example
`-- README.md
```

---

# 7. DATABASE DESIGN

Database: PostgreSQL

## 7.1. Core principles

- Một địa chỉ ví có thể tồn tại trên nhiều blockchain.
- Một địa chỉ ví có thể có nhiều nhãn.
- Một nhãn có thể có nhiều bằng chứng.
- Một sanctions entity có thể có nhiều địa chỉ.
- Một sanctions entity có thể thuộc nhiều chương trình.
- Một địa chỉ có thể có nhiều nguồn attribution.
- Mọi dữ liệu phải có provenance.
- Không xóa bằng chứng cũ khi nguồn thay đổi.
- Không dùng JSONB thay thế toàn bộ relational model.
- JSONB chỉ lưu metadata, raw observations và dữ liệu có cấu trúc biến động.

## 7.2. blockchain_networks

```sql
CREATE TABLE blockchain_networks (
    id BIGSERIAL PRIMARY KEY,
    namespace VARCHAR(30) NOT NULL,
    chain_reference VARCHAR(100) NOT NULL,
    code VARCHAR(50) NOT NULL UNIQUE,
    name VARCHAR(100) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    UNIQUE(namespace, chain_reference)
);
```

Examples:

- ETH: eip155 / 1
- BSC: eip155 / 56

## 7.3. wallets

```sql
CREATE TABLE wallets (
    id BIGSERIAL PRIMARY KEY,
    network_id BIGINT NOT NULL
        REFERENCES blockchain_networks(id),
    address VARCHAR(255) NOT NULL,
    normalized_address VARCHAR(255) NOT NULL,
    address_type VARCHAR(30) NOT NULL DEFAULT 'unknown',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(network_id, normalized_address)
);

CREATE INDEX idx_wallets_normalized
ON wallets(normalized_address);
```

Normalization:

- EVM: validate 20-byte hex address.
- EVM indexing: lowercase.
- Preserve original representation.
- Other chains require chain-specific codecs.

## 7.4. data_sources

```sql
CREATE TABLE data_sources (
    id BIGSERIAL PRIMARY KEY,
    code VARCHAR(100) NOT NULL UNIQUE,
    name VARCHAR(150) NOT NULL,
    source_type VARCHAR(30) NOT NULL,
    homepage_url TEXT,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

## 7.5. entities

```sql
CREATE TABLE entities (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    entity_type VARCHAR(60),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Examples:

- Lazarus Group
- Binance
- HTX
- Other attributed organizations

Không tự gán entity ownership dựa trên transaction exposure.

## 7.6. sanctions_entities

```sql
CREATE TABLE sanctions_entities (
    id BIGSERIAL PRIMARY KEY,
    source_id BIGINT NOT NULL REFERENCES data_sources(id),
    external_entity_id TEXT NOT NULL,
    entity_id BIGINT REFERENCES entities(id),
    primary_name TEXT NOT NULL,
    entity_type VARCHAR(60),
    source_url TEXT,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,
    UNIQUE(source_id, external_entity_id)
);
```

Purpose:

Lưu hồ sơ sanctions entity từ nguồn chính thức.

Không lưu lý do trừng phạt dưới dạng một câu tự suy diễn.

## 7.7. sanctions_programs

```sql
CREATE TABLE sanctions_programs (
    id BIGSERIAL PRIMARY KEY,
    source_id BIGINT NOT NULL REFERENCES data_sources(id),
    code VARCHAR(100) NOT NULL,
    name TEXT,
    description TEXT,
    source_url TEXT,
    UNIQUE(source_id, code)
);
```

## 7.8. sanctions_entity_programs

```sql
CREATE TABLE sanctions_entity_programs (
    sanctions_entity_id BIGINT NOT NULL
        REFERENCES sanctions_entities(id),
    program_id BIGINT NOT NULL
        REFERENCES sanctions_programs(id),
    PRIMARY KEY(sanctions_entity_id, program_id)
);
```

## 7.9. sanctions_wallets

```sql
CREATE TABLE sanctions_wallets (
    id BIGSERIAL PRIMARY KEY,
    sanctions_entity_id BIGINT NOT NULL
        REFERENCES sanctions_entities(id),

    wallet_id BIGINT REFERENCES wallets(id),

    listed_address TEXT NOT NULL,
    normalized_address TEXT NOT NULL,

    currency_code VARCHAR(40),
    network_id BIGINT REFERENCES blockchain_networks(id),

    identifier_type TEXT NOT NULL,
    source_identifier_id TEXT,
    match_scope VARCHAR(30) NOT NULL DEFAULT 'CHAIN_SPECIFIC',

    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,

    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

CREATE INDEX idx_sanctions_wallets_address
ON sanctions_wallets(normalized_address);

CREATE INDEX idx_sanctions_wallets_entity
ON sanctions_wallets(sanctions_entity_id);

CREATE UNIQUE INDEX uq_sanctions_wallet_identifier
ON sanctions_wallets(
    sanctions_entity_id,
    normalized_address,
    identifier_type,
    COALESCE(currency_code, ''),
    COALESCE(source_identifier_id, '')
);
```

Important:

- `listed_address`: địa chỉ gốc từ OFAC.
- `currency_code`: loại tiền hoặc blockchain được OFAC mô tả.
- `network_id`: chỉ gán khi mapping có cơ sở.
- `match_scope`: phạm vi khớp.
- `evidence`: original identifier và metadata.

Không tự biến ETH listing thành BSC listing.

## 7.10. sanctions_references

```sql
CREATE TABLE sanctions_references (
    id BIGSERIAL PRIMARY KEY,
    sanctions_entity_id BIGINT NOT NULL
        REFERENCES sanctions_entities(id),

    reference_type VARCHAR(60) NOT NULL,
    title TEXT,
    reference_url TEXT,
    published_at TIMESTAMPTZ,
    details JSONB NOT NULL DEFAULT '{}'::jsonb
);
```

Reference types:

- OFFICIAL_NOTICE
- SANCTIONS_PROGRAM
- LEGAL_AUTHORITY
- SOURCE_RECORD
- OTHER

Purpose:

Cung cấp bằng chứng và bối cảnh trừng phạt.

## 7.11. risk_categories

```sql
CREATE TABLE risk_categories (
    id BIGSERIAL PRIMARY KEY,
    code VARCHAR(60) NOT NULL UNIQUE,
    name VARCHAR(150) NOT NULL,
    category_type VARCHAR(30) NOT NULL
);
```

Suggested categories:

| Code | Type |
|---|---|
| SANCTIONED | LEGAL |
| HACK | MALICIOUS_ACTIVITY |
| SCAM | MALICIOUS_ACTIVITY |
| PHISHING | MALICIOUS_ACTIVITY |
| DRAINER | MALICIOUS_ACTIVITY |
| RANSOMWARE | MALICIOUS_ACTIVITY |
| MONEY_LAUNDERING | MALICIOUS_ACTIVITY |
| GAMBLING | SERVICE_TYPE |
| MIXER | SERVICE_TYPE |
| EXCHANGE | ENTITY_TYPE |

GAMBLING và EXCHANGE không mặc định đồng nghĩa với tội phạm.

## 7.12. wallet_labels

```sql
CREATE TABLE wallet_labels (
    id BIGSERIAL PRIMARY KEY,

    wallet_id BIGINT NOT NULL REFERENCES wallets(id),
    category_id BIGINT NOT NULL REFERENCES risk_categories(id),
    source_id BIGINT NOT NULL REFERENCES data_sources(id),
    entity_id BIGINT REFERENCES entities(id),

    external_record_id TEXT,
    fingerprint TEXT NOT NULL UNIQUE,

    label TEXT NOT NULL,
    confidence VARCHAR(20) NOT NULL DEFAULT 'unknown',
    verification_status VARCHAR(30) NOT NULL DEFAULT 'unverified',

    source_url TEXT,
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,

    observed_at TIMESTAMPTZ,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,

    CHECK (
        confidence IN ('unknown', 'low', 'medium', 'high')
    ),

    CHECK (
        verification_status IN (
            'unverified',
            'reported',
            'verified',
            'disputed'
        )
    )
);

CREATE INDEX idx_wallet_labels_wallet
ON wallet_labels(wallet_id);

CREATE INDEX idx_wallet_labels_evidence
ON wallet_labels USING GIN(evidence);
```

Fingerprint must be stable across ingestion runs.

Không UPSERT chỉ dựa vào wallet_id.

## 7.13. transactions

```sql
CREATE TABLE transactions (
    id BIGSERIAL PRIMARY KEY,

    network_id BIGINT NOT NULL
        REFERENCES blockchain_networks(id),

    tx_hash VARCHAR(255) NOT NULL,
    transfer_index VARCHAR(100) NOT NULL,
    transfer_type VARCHAR(30) NOT NULL,

    from_wallet_id BIGINT REFERENCES wallets(id),
    to_wallet_id BIGINT REFERENCES wallets(id),

    asset_identifier VARCHAR(255),
    amount_raw NUMERIC(78, 0) NOT NULL,

    block_number BIGINT,
    block_timestamp TIMESTAMPTZ,

    confirmation_status VARCHAR(20)
        NOT NULL DEFAULT 'pending',

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(network_id, tx_hash, transfer_index)
);

CREATE INDEX idx_transactions_from
ON transactions(from_wallet_id, block_timestamp DESC);

CREATE INDEX idx_transactions_to
ON transactions(to_wallet_id, block_timestamp DESC);
```

Transfer types:

- NATIVE
- TOKEN
- INTERNAL

Rules:

- Không lưu token amount bằng float.
- Giữ raw integer amount.
- Token transfer phải có token contract.
- Cần định danh riêng cho event log.
- Phải xử lý blockchain reorganization.

## 7.14. screening_results

```sql
CREATE TABLE screening_results (
    id UUID PRIMARY KEY,

    wallet_id BIGINT NOT NULL REFERENCES wallets(id),

    risk_score SMALLINT,
    risk_level VARCHAR(30) NOT NULL,
    screening_status VARCHAR(30) NOT NULL,

    scoring_policy_version VARCHAR(50) NOT NULL,

    findings JSONB NOT NULL DEFAULT '[]'::jsonb,
    provider_status JSONB NOT NULL DEFAULT '{}'::jsonb,

    screened_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CHECK (
        risk_score BETWEEN 0 AND 100
        OR risk_score IS NULL
    )
);
```

Possible statuses:

- COMPLETED
- PARTIAL
- INSUFFICIENT_DATA
- FAILED

## 7.15. ingestion_runs

```sql
CREATE TABLE ingestion_runs (
    id UUID PRIMARY KEY,

    source_id BIGINT NOT NULL REFERENCES data_sources(id),

    status VARCHAR(30) NOT NULL,

    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,

    records_fetched BIGINT NOT NULL DEFAULT 0,
    records_inserted BIGINT NOT NULL DEFAULT 0,
    records_updated BIGINT NOT NULL DEFAULT 0,
    records_rejected BIGINT NOT NULL DEFAULT 0,

    snapshot_checksum TEXT,
    snapshot_version TEXT,
    cursor_value TEXT,
    error_message TEXT
);
```

---

# 8. SANCTIONS INTELLIGENCE WORKFLOW

## 8.1. Bulk ingestion

```text
OFAC ADVANCED XML
        |
        v
DOWNLOAD TEMP FILE
        |
        v
VALIDATE FILE
        |
        v
STREAM XML PARSER
        |
        v
EXTRACT SDN ENTRIES
        |
        +-- Entity
        |
        +-- Sanctions Programs
        |
        +-- Digital Currency Addresses
        |
        +-- Source References
        |
        v
NORMALIZE
        |
        v
STAGING TABLES
        |
        v
VALIDATE SNAPSHOT
        |
        v
PUBLISH TO DATABASE
```

## 8.2. Wallet sanctions lookup

```text
Wallet Address
      |
      v
Normalize Address
      |
      v
Resolve Network
      |
      v
Lookup sanctions_wallets
      |
      v
Resolve sanctions_entities
      |
      v
Resolve sanctions_programs
      |
      v
Resolve sanctions_references
      |
      v
Build Sanctions Response
```

## 8.3. Sanctions reason model

Phải phân biệt:

1. `entity_name`: tên chủ thể.
2. `programs`: mã chương trình trừng phạt.
3. `program_descriptions`: mô tả chương trình.
4. `official_notices`: thông báo chính thức.
5. `reason_summary`: tóm tắt có nguồn.
6. `reason_status`: trạng thái thông tin lý do.

Possible reason statuses:

- OFFICIAL_EXPLICIT
- PROGRAM_CONTEXT_ONLY
- NOT_AVAILABLE

Không tự coi mô tả chương trình là lý do cụ thể của một chủ thể.

Nếu không có nguồn giải thích cụ thể:

- `reason_summary = null`
- `reason_status = NOT_AVAILABLE`

---

# 9. RESTFUL API DESIGN

Base URL:

`/api/v1`

Authentication:

- API Key hoặc JWT.
- Rate limit theo client.
- Admin endpoints yêu cầu quyền riêng.

Response conventions:

- JSON
- UTC timestamps
- Stable error codes
- Cursor pagination
- Request ID

## 9.1. GET /networks

Lấy danh sách blockchain được hỗ trợ.

## 9.2. GET /wallets/{network}/{address}

Lấy thông tin và nhãn đã lưu của ví.

Example:

`GET /api/v1/wallets/ethereum/0x098B716B8Aaf21512996dC57EB0615e2383E2f96`

## 9.3. GET /sanctions/wallets/{network}/{address}

Purpose:

Tra cứu sanctions trực tiếp.

Example:

`GET /api/v1/sanctions/wallets/ethereum/0x098B716B8Aaf21512996dC57EB0615e2383E2f96`

Example response contract:

```json
{
  "data": {
    "network": "ethereum",
    "address": "0x098B716B8Aaf21512996dC57EB0615e2383E2f96",
    "matched": true,
    "match_type": "WALLET_EXACT",
    "match_scope": "CHAIN_SPECIFIC",
    "sanctions": [
      {
        "list": "OFAC_SDN",
        "entity_name": "Lazarus Group",
        "programs": [
          {
            "code": "DPRK3",
            "name": null
          }
        ],
        "reason": {
          "summary": null,
          "status": "NOT_AVAILABLE"
        },
        "source": {
          "provider": "OFAC",
          "record_id": null,
          "url": "https://ofac.treasury.gov/"
        }
      }
    ],
    "data_status": "AVAILABLE",
    "last_synced_at": null
  }
}
```

The response above illustrates the intended schema.

Actual values must be populated from verified OFAC records.

Do not fabricate record IDs, notices or reason summaries.

## 9.4. GET /sanctions/entities/{id}

Purpose:

Lấy hồ sơ sanctions entity.

Response fields:

- entity_id
- primary_name
- entity_type
- source
- external_entity_id
- programs
- digital_currency_addresses
- official_references
- details
- last_synced_at

## 9.5. GET /sanctions/programs/{code}

Purpose:

Lấy thông tin sanctions program.

Response fields:

- code
- name
- description
- source_url
- associated_entities_count

## 9.6. POST /screenings

Purpose:

Tạo yêu cầu kiểm tra rủi ro ví.

Request:

```json
{
  "network": "ethereum",
  "address": "0x098B716B8Aaf21512996dC57EB0615e2383E2f96",
  "options": {
    "include_sanctions": true,
    "include_labels": true,
    "include_exposure": true,
    "max_hops": 2,
    "refresh_providers": false
  }
}
```

Response:

HTTP 202 Accepted

```json
{
  "data": {
    "screening_id": "uuid",
    "status": "PENDING"
  }
}
```

Use Idempotency-Key.

## 9.7. GET /screenings/{id}

Purpose:

Lấy kết quả screening.

Response fields:

- screening_id
- network
- address
- status
- risk_score
- risk_level
- direct_sanctions
- direct_labels
- exposures
- scoring
- evidence
- provider_status
- data_completeness
- screened_at

## 9.8. GET /wallets/{network}/{address}/labels

Purpose:

Trả toàn bộ labels và evidence.

Filters:

- category
- source
- verification_status
- active_only
- cursor
- limit

## 9.9. GET /wallets/{network}/{address}/transactions

Purpose:

Lấy các giao dịch đã được index.

Filters:

- direction
- asset
- from_timestamp
- to_timestamp
- cursor
- limit

## 9.10. GET /wallets/{network}/{address}/exposures

Purpose:

Phân tích liên kết ví rủi ro.

Parameters:

- max_hops
- category
- direction
- limit

Response fields:

- counterparty
- category
- relationship
- hops
- path
- transaction_evidence
- coverage
- truncated

## 9.11. POST /admin/ingestion-runs

Purpose:

Kích hoạt ingestion thủ công.

Example:

```json
{
  "source": "ofac"
}
```

## 9.12. GET /admin/ingestion-runs/{id}

Purpose:

Xem tiến độ và kết quả ingestion.

---

# 10. RISK SCORING ENGINE

## 10.1. Risk dimensions

Phân biệt:

1. Direct Sanctions
2. Direct Malicious Labels
3. Indirect Transaction Exposure
4. Service Classification
5. Data Confidence
6. Data Freshness
7. Provider Coverage

## 10.2. Proposed Policy V1

Score range:

0–100

Illustrative weights:

- Direct verified sanctions: 100
- Direct verified hack: 90
- Direct verified phishing: 90
- Direct verified scam: 85
- Direct verified ransomware: 95

Indirect exposure:

- 1-hop: configurable weight.
- 2-hop: reduced weight.
- Beyond 2 hops: not included in MVP scoring.

Gambling:

- Service classification by default.
- Không mặc định là malicious activity.

Exchange:

- Entity information only.
- Không mặc định tăng điểm rủi ro.

Scoring formula:

```text
score = min(
    100,
    direct_risk + exposure_risk + other_signals
)
```

Rules:

- Không double count duplicate evidence.
- Không cộng điểm nhiều lần từ các nguồn mirror.
- Phải lưu scoring policy version.
- Scores are heuristic, not probabilities.
- Không đủ dữ liệu thì risk_score có thể NULL.
- Direct sanctions phải có sanctions flag độc lập với risk score.

Weights là ví dụ chính sách ban đầu, chưa được xác thực thống kê.

---

# 11. TRANSACTION TRACING

## 11.1. Purpose

Phân tích quan hệ giao dịch giữa các địa chỉ ví.

Example:

```text
Wallet A
   |
   v
Wallet B
   |
   v
Wallet C
   |
   v
Wallet D [SANCTIONED]
```

Wallet A có liên kết đồ thị 3-hop với Wallet D.

Điều này không chứng minh:

- Wallet A thuộc chủ thể bị trừng phạt.
- Wallet B thuộc chủ thể bị trừng phạt.
- Wallet A trực tiếp gửi tiền cho Wallet D.
- Tài sản cụ thể của A đã đến D.
- Các ví có chung chủ sở hữu.

## 11.2. Direct vs Indirect

Direct attribution:

Ví được nguồn dữ liệu gắn nhãn trực tiếp.

Direct exposure:

Ví giao dịch trực tiếp với ví được gắn nhãn.

Indirect exposure:

Ví có đường liên kết qua nhiều giao dịch.

Phải giữ các loại quan hệ này riêng biệt.

## 11.3. BSC multi-hop tracing

Target chain:

BNB Smart Chain.

Transfer types:

- Native BNB
- BEP-20 Transfer
- Internal transfers, khi có nguồn dữ liệu phù hợp

Workflow:

1. Resolve source wallet.
2. Fetch outgoing transfers.
3. Normalize transaction edges.
4. Store edges.
5. Expand next-hop wallets.
6. Apply node and edge limits.
7. Stop at configured max hops.
8. Detect labeled counterparties.
9. Build evidence path.
10. Return coverage metadata.

## 11.4. Graph traversal

MVP:

- 1-hop
- 2-hop

Future:

- 3-hop
- 4-hop
- 5-hop or more

Algorithms:

- Bounded BFS
- Recursive SQL queries where appropriate

Constraints:

- Max hops
- Max nodes
- Max edges
- Query timeout
- Maximum response size
- Maximum provider requests

## 11.5. Evidence requirements

Each path edge should contain:

- from_address
- to_address
- network
- tx_hash
- transfer_type
- asset
- amount_raw
- block_number
- block_timestamp
- confirmation_status

Path-level metadata:

- hop_count
- path_verified
- temporal_order_valid
- data_coverage
- truncated
- tracing_method

A valid graph path is not necessarily proof of identical funds flowing through every wallet.

---

# 12. DATA INGESTION WORKFLOW

Generic workflow:

1. Fetch source data.
2. Validate transport result.
3. Validate schema or file.
4. Normalize identifiers.
5. Resolve blockchain network.
6. Resolve entity.
7. Resolve category.
8. Generate stable fingerprint.
9. Upsert records.
10. Store original evidence.
11. Record ingestion metrics.
12. Update source freshness.
13. Invalidate relevant cache.

For bulk snapshots:

1. Download to temporary location.
2. Validate completeness.
3. Parse into staging.
4. Verify row counts.
5. Verify snapshot consistency.
6. Publish atomically.
7. Mark disappeared records inactive only after successful full snapshot.

Do not delete labels when:

- Provider timed out.
- API quota was exceeded.
- Download failed.
- Response was partial.
- XML parser failed.
- Snapshot validation failed.

---

# 13. PROVIDER ADAPTER CONTRACT

On-demand providers:

```go
type Provider interface {
    Name() string

    LookupAddress(
        ctx context.Context,
        network string,
        address string,
    ) ([]Observation, error)
}
```

Bulk sources:

```go
type BulkSource interface {
    FetchBatch(
        ctx context.Context,
        cursor string,
    ) (BatchResult, error)
}
```

File-based bulk sources:

```go
type SnapshotSource interface {
    Download(
        ctx context.Context,
    ) (Snapshot, error)

    Import(
        ctx context.Context,
        snapshot Snapshot,
    ) (ImportResult, error)
}
```

Observation fields:

- Network
- Address
- Category
- Label
- Entity
- Source
- Original source
- Source URL
- Confidence
- Verification status
- Observed timestamp
- Evidence

Not every provider supports every interface.

Do not force incompatible providers into the same workflow.

---

# 14. CACHE STRATEGY

Redis keys:

```text
wallet:labels:{network}:{address}

wallet:sanctions:{network}:{address}:{snapshot_version}

wallet:screening:{network}:{address}:{policy_version}

provider:{source}:{network}:{address}
```

Suggested TTL:

- Direct labels: 1 hour.
- Provider lookup: 15 minutes.
- Screening results: 5 minutes.

Sanctions cache:

- Invalidate when a new OFAC snapshot is published.
- Include snapshot version in cache key.
- Do not silently return outdated sanctions data as current.

Rules:

- Prevent cache stampedes.
- Do not cache provider errors as clean results.
- Separate positive and negative cache entries.
- Return data freshness metadata.

---

# 15. RATE LIMIT AND PROVIDER FAILURES

Each provider requires:

- Timeout.
- Concurrency limit.
- Rate limit.
- Retry policy.
- Circuit breaker.
- Failure metrics.

HTTP 429:

- Honor Retry-After.
- Use bounded exponential backoff.
- Do not retry indefinitely.

HTTP 5xx:

- Retry with limits.

HTTP 4xx:

- Distinguish invalid request from rate limit.

Provider outage:

- Continue with available local data.
- Mark result PARTIAL when appropriate.
- Return failed provider metadata.
- Do not convert provider failure into clean result.

---

# 16. SECURITY

Requirements:

- API authentication.
- Role-based admin access.
- Client rate limiting.
- Chain-specific address validation.
- Parameterized SQL.
- No raw SQL interpolation.
- SSRF protection.
- Fixed allowlist for provider endpoints.
- Secure secrets management.
- Audit logging.
- Request size limits.
- XML parser resource limits.
- Download timeout and size limits.
- Safe temporary file handling.
- Bounded graph traversal.

Never log API keys or secrets.

---

# 17. OBSERVABILITY

Metrics:

- screening_requests_total
- screening_duration_seconds
- screening_partial_total
- sanctions_matches_total
- sanctions_ingestion_duration_seconds
- sanctions_snapshot_age_seconds
- provider_requests_total
- provider_errors_total
- provider_rate_limited_total
- ingestion_records_total
- ingestion_last_success_timestamp
- wallet_labels_active_total
- tracing_nodes_examined_total

Logs:

- request_id
- screening_id
- source
- network
- duration
- status
- error_code
- snapshot_version

Do not log authentication credentials.

---

# 18. TEST PLAN

## 18.1. Unit tests

- EVM address normalization.
- Chain-specific validation.
- OFAC identifier extraction.
- Entity mapping.
- Program mapping.
- Source reference mapping.
- Duplicate wallet identifiers.
- Label fingerprint generation.
- Risk scoring.
- Empty labels.
- Unknown source categories.

## 18.2. Integration tests

- PostgreSQL migrations.
- OFAC XML import.
- Snapshot staging and publish.
- Idempotent upsert.
- Redis invalidation.
- Provider mock responses.
- Transaction graph queries.
- Screening persistence.

## 18.3. Failure tests

- Download interrupted.
- XML truncated.
- XML malformed.
- Unexpected XML schema.
- Snapshot missing records.
- Worker crashes.
- Concurrent ingestion.
- API returns 429.
- Provider returns malformed JSON.
- Provider times out.
- Blockchain reorganization.
- Previously listed address disappears.
- Same wallet has conflicting labels.
- Sanctions provider unavailable.

## 18.4. Acceptance tests

- Unknown wallet is not marked safe.
- OFAC exact match is detectable.
- Sanctions entity is returned.
- Sanctions programs are returned.
- Reason is not invented when unavailable.
- Every sanctions finding retains source evidence.
- Reported scam is not automatically verified.
- Exchange wallet is not automatically risky.
- 1-hop exposure does not imply ownership.
- Repeated ingestion does not create duplicates.
- Provider failure is visible to API consumers.
- Scores are reproducible by policy version.

---

# 19. IMPLEMENTATION ROADMAP

## PHASE 1 — FOUNDATION

Tasks:

- Initialize Go project.
- Setup PostgreSQL.
- Setup Redis.
- Create migrations.
- Implement network management.
- Implement wallet management.
- Implement base REST API.
- Implement API authentication.

Deliverable:

Working backend and database.

## PHASE 2 — OFAC SANCTIONS INTELLIGENCE

Tasks:

- Implement OFAC XML downloader.
- Implement streaming XML parser.
- Extract entities.
- Extract sanctions programs.
- Extract digital currency addresses.
- Extract source references.
- Implement snapshot staging.
- Implement atomic publish.
- Implement local sanctions lookup API.
- Implement sanctions entity detail API.
- Implement sanctions program API.

Deliverable:

Locally searchable OFAC sanctions database with entity, program and source evidence.

## PHASE 3 — PUBLIC WALLET LABELS

Tasks:

- Implement Wallet Attribution ingestion.
- Normalize categories.
- Deduplicate labels.
- Store provenance.
- Implement label lookup API.

Deliverable:

Searchable local wallet intelligence database.

## PHASE 4 — LIVE ENRICHMENT

Tasks:

- Implement GoPlus adapter.
- Implement optional Chainabuse adapter.
- Implement optional PublicAML adapter.
- Add provider caching.
- Add provider rate limiting.
- Handle degraded responses.

Deliverable:

Multi-source wallet screening.

## PHASE 5 — RISK SCORING

Tasks:

- Implement versioned risk policy.
- Separate direct risk and indirect exposure.
- Implement reason codes.
- Persist screening history.
- Implement screening API.

Deliverable:

Explainable wallet risk assessment.

## PHASE 6 — TRANSACTION TRACING

Tasks:

- Select transaction data provider.
- Index relevant wallet transfers.
- Support native transfers.
- Support BEP-20/ERC-20 transfers.
- Implement bounded BFS.
- Implement 1-hop/2-hop exposure.
- Return transaction evidence.
- Record data coverage.

Deliverable:

Transaction-based wallet exposure analysis.

## PHASE 7 — ADVANCED TRACING

Requires separate review.

Potential tasks:

- 3–5 hop tracing.
- Large graph optimizations.
- Better fund-flow heuristics.
- Cross-chain correlation.
- Advanced exposure scoring.

Do not implement without approval.

---

# 20. DEFINITION OF DONE

The MVP is complete when:

- [x] Ethereum and BSC addresses can be validated.
- [x] OFAC Advanced XML can be downloaded.
- [x] XML can be parsed without loading the entire file into RAM.
- [x] OFAC entities can be imported into PostgreSQL.
- [x] OFAC wallet identifiers can be imported.
- [x] OFAC programs can be linked to entities.
- [x] Official source references are preserved.
- [x] Sanctions lookup returns direct matches.
- [x] Sanctions lookup returns entity information.
- [x] Sanctions lookup returns program information.
- [x] Missing reason details are explicitly represented.
- [x] Snapshot updates are atomic.
- [x] Failed snapshots do not destroy existing data.
- [x] Public wallet labels can be imported.
- [x] Multiple labels can coexist for one wallet.
- [x] Unknown addresses are not marked safe.
- [x] Screening requests are idempotent.
- [x] Provider outages do not corrupt local data.
- [x] Risk scores use a versioned policy.
- [x] Direct attribution and transaction exposure are separate.
- [x] Transaction paths retain supporting hashes.
- [x] Ingestion can recover after interruption.
- [x] Database constraints prevent duplicates.
- [x] Integration and failure tests pass.

---

# 21. EXECUTION CONSTRAINTS

## MUST

- Follow the defined Modular Monolith architecture.
- Use PostgreSQL migrations.
- Validate addresses by chain.
- Preserve original source evidence.
- Use streaming parsing for large XML files.
- Use context cancellation in Go.
- Implement bounded retries and timeouts.
- Implement idempotent ingestion.
- Use atomic snapshot publication.
- Keep scoring rules configurable.
- Write tests for critical flows.
- Preserve sanctions provenance.
- Report data freshness and completeness.

## MUST NOT

- Invent provider responses.
- Invent sanctions reasons.
- Invent source references.
- Assume provider quotas are unlimited.
- Assume free tier permits commercial use.
- Treat missing labels as proof of safety.
- Treat gambling as proof of criminal activity.
- Attribute wallet ownership from transfers alone.
- Treat a multi-hop graph path as proof of identical fund flow.
- Delete records after incomplete sync.
- Hardcode secrets.
- Introduce microservices without approval.
- Add paid providers without approval.

## STOP AND REQUEST REVIEW BEFORE

- Changing database architecture.
- Adding a paid provider.
- Changing risk scoring policy.
- Introducing automatic enforcement decisions.
- Expanding tracing beyond agreed scope.
- Adding unsupported chains.
- Introducing cross-chain tracing.

---

# 22. FINAL SYSTEM CAPABILITIES

## Input

- Blockchain network.
- Wallet address.

## Output

- Sanctions match status.
- Sanctions entity.
- Sanctions programs.
- Sanctions reason, when supported by official evidence.
- Official sanctions references.
- Known entity attribution.
- Direct wallet labels.
- Risk categories.
- Risk score.
- Scoring policy version.
- Transaction exposure.
- Transaction path evidence.
- Data source.
- Data freshness.
- Screening completeness.

## Core Principle

A Blockchain Wallet Risk Intelligence system must explain:

**WHAT** risk was detected.

**WHO** is associated with the address, when attribution is supported.

**WHY** the risk or sanctions finding exists.

**WHERE** the evidence came from.

**HOW** the assessment was calculated.

**WHEN** the underlying data was last updated.

The system must distinguish verified facts, third-party observations, inferred relationships and missing information.

---

# END OF TECHNICAL DESIGN SPECIFICATION
