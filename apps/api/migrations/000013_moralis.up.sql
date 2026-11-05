-- Migration 000013: moralis transaction-history provider (phase-05).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('moralis', 'Moralis Web3 API v2.2 address txs + erc20/transfers', 'api',
     'https://moralis.io/');
