-- Migration 000011: ankr transaction-history provider (phase-03).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('ankr', 'Ankr Advanced API ankr_getTransactionsByAddress', 'api',
     'https://rpc.ankr.com/');
