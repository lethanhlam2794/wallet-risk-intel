-- Migration 000024: stablecoin freeze intelligence (chain-scan source).
--
-- Unlike the full-replace labels pipeline, this source is a DELTA feed:
-- absence from a scan window is NOT a revocation signal — only explicit
-- unfreeze events revoke, and destroyBlackFunds keeps isBlackListed set
-- so it never revokes. Scan progress lives in a per-contract cursor that
-- advances inside the publish transaction (crash ⇒ cursor untouched).

-- Per-contract incremental cursor. PK (network_id, contract_address) —
-- EVM deployers collide across chains, so the pair is the identity.
CREATE TABLE freeze_scan_state (
    network_id BIGINT NOT NULL REFERENCES blockchain_networks(id),
    contract_address TEXT NOT NULL,
    genesis_block BIGINT NOT NULL,
    last_scanned_block BIGINT NOT NULL,
    last_head_block BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (network_id, contract_address)
);

-- Run-scoped event staging (no FK — failed-run staging stays for
-- diagnosis; lifecycle via purge lists, same convention as
-- stg_wallet_labels). fingerprint/label/evidence are staged rather than
-- re-derived at publish: the fingerprint is sha256 over code strings —
-- not computable in SQL — and evidence is built once at parse time.
CREATE TABLE stg_freeze_events (
    run_id UUID NOT NULL,
    contract_address TEXT NOT NULL,
    event_type TEXT NOT NULL
        CHECK (event_type IN ('freeze', 'unfreeze', 'destroy')),
    wallet_address TEXT NOT NULL,
    tx_hash TEXT NOT NULL,
    log_index INTEGER NOT NULL,
    block_number BIGINT NOT NULL,
    block_ts TIMESTAMPTZ,
    fingerprint TEXT NOT NULL,
    label TEXT NOT NULL,
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (run_id, contract_address, tx_hash, log_index)
);
CREATE INDEX idx_stg_freeze_events_run ON stg_freeze_events(run_id);
CREATE INDEX idx_stg_freeze_events_fp ON stg_freeze_events(run_id, fingerprint);

-- Cursors seed from the deploy blocks verified live via eth_getCode
-- binary search (plan research): USDT deploys at 4634748, USDC at
-- 6082465. last_scanned starts at genesis-1 so the first window covers
-- the deploy block itself (overlap is harmless — inserts are idempotent).
INSERT INTO freeze_scan_state (network_id, contract_address, genesis_block, last_scanned_block)
SELECT bn.id, v.contract, v.genesis, v.genesis - 1
FROM (VALUES
    ('0xdac17f958d2ee523a2206206994597c13d831ec7', 4634748),
    ('0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48', 6082465)
) AS v(contract, genesis)
CROSS JOIN blockchain_networks bn
WHERE bn.code = 'ethereum';

INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('stablecoin_freeze', 'Stablecoin Freeze (USDT/USDC on-chain)', 'chain_scan',
     'https://etherscan.io/');
