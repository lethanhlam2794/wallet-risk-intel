-- Migration 000014: tatum transaction-history provider (phase-06).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('tatum', 'Tatum Data API v4 transaction/history + node RPC head', 'api',
     'https://tatum.io/');
