-- Migration 000009: alchemy transaction-history provider (phase-01).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('alchemy', 'Alchemy alchemy_getAssetTransfers', 'api',
     'https://alchemy.com/');
