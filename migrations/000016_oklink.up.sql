-- Migration 000016: oklink transaction-history provider (phase-08).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('oklink', 'OKLink explorer address transaction-list (bsc)', 'api',
     'https://www.oklink.com/');
