-- Migration 000008: transaction index + wallet index state (spec §7.13,
-- phase 6). transactions follows the spec columns verbatim plus is_error
-- (declared deviation: receiptsStatus/isError is execution status, never
-- conflated with confirmation_status).

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
        NOT NULL DEFAULT 'pending'
        CHECK (confirmation_status IN ('pending','confirmed')),

    is_error BOOLEAN NOT NULL DEFAULT false,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(network_id, tx_hash, transfer_index)
);

CREATE INDEX idx_transactions_from
ON transactions(from_wallet_id, block_timestamp DESC);

CREATE INDEX idx_transactions_to
ON transactions(to_wallet_id, block_timestamp DESC);

-- Per-wallet lazy-indexing coverage state. indexed_until_block is the
-- contiguous-coverage watermark; indexed_at is the last terminal write
-- (fresh|partial|failed|disabled) — it doubles as the FAILED-cooldown and
-- staleness clock. rows_* counters are cumulative per source and reset on
-- provider switch together with the watermark.
CREATE TABLE wallet_index_state (
    network_id BIGINT NOT NULL
        REFERENCES blockchain_networks(id),
    wallet_id BIGINT NOT NULL
        REFERENCES wallets(id),

    source_id BIGINT REFERENCES data_sources(id),

    indexed_until_block BIGINT,
    indexed_at TIMESTAMPTZ,

    status VARCHAR(20) NOT NULL DEFAULT 'not_indexed'
        CHECK (status IN ('not_indexed','indexing','fresh','partial',
                          'failed','disabled')),

    -- Typed provider failure kind (ErrKind) or 'disabled'; cleared on
    -- success. provider_status on the wire reads this verbatim.
    error TEXT,

    rows_indexed BIGINT NOT NULL DEFAULT 0,
    rows_skipped_missing_contract BIGINT NOT NULL DEFAULT 0,
    rows_skipped_invalid_address BIGINT NOT NULL DEFAULT 0,

    -- True exactly when the last fetch stopped on a bound (max_pages,
    -- provider budget, window cap) — written together with status.
    history_truncated BOOLEAN NOT NULL DEFAULT false,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (network_id, wallet_id)
);

-- On-demand transaction providers (per-adapter provenance; source_type
-- 'api' matches the provider_observations convention from 000006).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('chainscan', 'Etherscan V2 unified explorer API', 'api',
     'https://etherscan.io/'),
    ('nodereal', 'NodeReal MegaNode nr_getAssetTransfers', 'api',
     'https://nodereal.io/');
