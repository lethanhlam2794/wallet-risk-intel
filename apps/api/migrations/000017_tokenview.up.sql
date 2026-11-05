-- Migration 000017: tokenview transaction-history provider (phase-10).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('tokenview', 'Tokenview vipapi address transaction history (eth, bsc)', 'api',
     'https://services.tokenview.io/');
