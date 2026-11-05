-- Blockscout PRO API: keyed multichain surface (api.blockscout.com,
-- chain_id param) alongside the existing keyless blockscout adapter.
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('blockscout_pro', 'Blockscout PRO API (keyed multichain)', 'api',
     'https://blockscout.com/');
