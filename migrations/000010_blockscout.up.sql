-- Migration 000010: blockscout transaction-history provider (phase-02).
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('blockscout', 'Blockscout v2 REST (keyless)', 'api',
     'https://eth.blockscout.com/');
