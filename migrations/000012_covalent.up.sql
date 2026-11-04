-- Migration 000012: covalent transaction-history provider (phase-04).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('covalent', 'GoldRush (Covalent) transactions_v3', 'api',
     'https://goldrush.dev/');
