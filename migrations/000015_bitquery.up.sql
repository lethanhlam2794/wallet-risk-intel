-- Migration 000015: bitquery transaction-history provider (phase-07).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('bitquery', 'Bitquery v2 GraphQL EVM Transfers (bsc)', 'api',
     'https://bitquery.io/');
