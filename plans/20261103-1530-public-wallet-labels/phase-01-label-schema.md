# Phase 01 — Label Schema (migration 000005)

## Context

Spec §7.11 `risk_categories`, §7.12 `wallet_labels`. Existing reusable:
`wallets(network_id, normalized_address)` unique, `entities(name, entity_type)`,
`data_sources`. OFAC staging pattern in migration 000002 + staging tables
scoped by `run_id` is the template.

## Files

- `migrations/000005_labels.up.sql`
- `migrations/000005_labels.down.sql`

## DDL

```sql
CREATE TABLE risk_categories (
    id BIGSERIAL PRIMARY KEY,
    code VARCHAR(60) NOT NULL UNIQUE,
    name VARCHAR(150) NOT NULL,
    category_type VARCHAR(30) NOT NULL,
    CHECK (category_type IN ('LEGAL','MALICIOUS_ACTIVITY','SERVICE_TYPE','ENTITY_TYPE','OTHER'))
);
```

Seed rows (spec §7.11 suggested set + upstream-observed categories):
SANCTIONED/LEGAL, HACK, SCAM, PHISHING, DRAINER, RANSOMWARE,
MONEY_LAUNDERING, TERRORISM, DARKNET, EXTREMISM, FROZEN — MALICIOUS_ACTIVITY;
GAMBLING, MIXER, MINING, SERVICE, BRIDGE, MEV, DEFI — SERVICE_TYPE;
EXCHANGE, ENTITY — ENTITY_TYPE; OTHER — OTHER (fallback bucket only when
an unseen upstream category arrives; never silently drop).

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
    upstream_category TEXT,            -- original upstream category string
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    observed_at TIMESTAMPTZ,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,
    CHECK (confidence IN ('unknown','low','medium','high')),
    CHECK (verification_status IN ('unverified','reported','verified','disputed'))
);
CREATE INDEX idx_wallet_labels_wallet ON wallet_labels(wallet_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_wallet_labels_evidence ON wallet_labels USING GIN(evidence);
CREATE INDEX idx_wallet_labels_category ON wallet_labels(category_id) WHERE revoked_at IS NULL;
```

`upstream_category` keeps the original string per spec §4.3 ("giữ original
label" — never lose source vocabulary); `category_id` is the normalized form.

```sql
-- F8 amendment: NO FK / no cascade — matches OFAC staging convention
-- (failed-run staging retained for diagnosis; lifecycle via purge lists).
CREATE TABLE stg_wallet_labels (
    run_id UUID NOT NULL,
    network_code TEXT NOT NULL,
    address TEXT NOT NULL,
    entity_name TEXT,
    label TEXT NOT NULL,
    upstream_category TEXT NOT NULL,
    source_field TEXT,                 -- upstream `source` column (a+b+c)
    source_url TEXT,
    confidence TEXT NOT NULL,
    last_updated TEXT,
    fingerprint TEXT NOT NULL,
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (run_id, fingerprint)
);
```

```sql
-- F5/F9: generic per-source snapshot registry (labels + future bulk
-- sources); sanctions_snapshots stays OFAC-shaped, NOT migrated.
CREATE TABLE data_source_snapshots (
    id BIGSERIAL PRIMARY KEY,
    source_id BIGINT NOT NULL REFERENCES data_sources(id),
    version TEXT NOT NULL,
    published_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    row_count BIGINT NOT NULL,
    UNIQUE(source_id, version)
);
```

Data source seed: `INSERT INTO data_sources(code,name,source_type,homepage_url) VALUES ('wallet_attribution','Wallet Attribution (prettydeath)','snapshot','https://github.com/prettydeath/wallet-attribution')`.

(Registered as `snapshot` type not `bulk`: the artifact is a full-file
snapshot with soft-revoke lifecycle — BulkSource cursor semantics don't
fit a git-hosted file; taxonomy rationale recorded per Critic F17.)

## Entities dedup (F6 amendment)

`entities` has no unique constraint and no writer today. Get-or-create via
unique index `uq_entities_name_ci` on **`LOWER(TRIM(name))`** (whitespace
normalized at write time — `'Binance '` ≈ `'Binance'`). Shared registry is
spec intent — same real-world org gets one row across sources; label
import may link an OFAC entity. Rules:

- `entity_type` first-write-wins: `COALESCE(entities.entity_type,
  EXCLUDED.entity_type)` — never downgrade a non-NULL existing type.
- Empty/whitespace name → `entity_id = NULL`; literal junk (`'Null'`)
  stays verbatim as a name (upstream vocabulary, evidence-level).
- `wallet_labels.entity_id` conveys *reported attribution vocabulary*,
  not verified identity; raw string always duplicated into
  `evidence.upstream_record.entity` so per-row provenance survives any
  registry-level conflation.

## Validation

- `go run ./cmd/migrate up` then `down 1` then `up` — clean, dirty=f
- \d wallet_labels — columns/constraints/indexes as designed
