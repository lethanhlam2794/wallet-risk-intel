-- Migration 000018: chainbase transaction-history provider (phase-11).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('chainbase', 'Chainbase Web3 API account/token transaction history (eth, bsc)', 'api',
     'https://api.chainbase.online/');
