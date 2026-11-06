-- Split the alchemy provider config into per-chain rows: Alchemy exposes a
-- distinct endpoint host per network (eth-mainnet.g.alchemy.com vs
-- bnb-mainnet.g.alchemy.com), so each chain gets its own provider_configs
-- row with its own key slot and base_url. The api_key is copied verbatim
-- (same value — possibly keystore-encrypted — is valid for both rows; a
-- different BSC key can be set later via the admin endpoint).
-- The legacy 'alchemy' row is kept but deactivated so nothing double-serves.
INSERT INTO data_sources (code, name, source_type, homepage_url) VALUES
    ('alchemy_eth', 'Alchemy (Ethereum)', 'api', 'https://www.alchemy.com/'),
    ('alchemy_bsc', 'Alchemy (BSC)', 'api', 'https://www.alchemy.com/')
ON CONFLICT (code) DO NOTHING;

INSERT INTO provider_configs (source_code, api_key, enabled_chains, status, updated_by)
SELECT 'alchemy_eth', api_key, '["ethereum"]'::jsonb, status, 'migration-000025'
FROM provider_configs WHERE source_code = 'alchemy'
ON CONFLICT (source_code) DO NOTHING;

INSERT INTO provider_configs (source_code, api_key, enabled_chains, status, updated_by)
SELECT 'alchemy_bsc', api_key, '["bsc"]'::jsonb, status, 'migration-000025'
FROM provider_configs WHERE source_code = 'alchemy'
ON CONFLICT (source_code) DO NOTHING;

-- Fallbacks when the legacy row was never materialized: seed empty rows so
-- the admin surface shows them (key still required to serve traffic).
INSERT INTO provider_configs (source_code, enabled_chains, status, updated_by)
SELECT 'alchemy_eth', '["ethereum"]'::jsonb, 'active', 'migration-000025'
WHERE NOT EXISTS (SELECT 1 FROM provider_configs WHERE source_code = 'alchemy_eth');

INSERT INTO provider_configs (source_code, enabled_chains, status, updated_by)
SELECT 'alchemy_bsc', '["bsc"]'::jsonb, 'active', 'migration-000025'
WHERE NOT EXISTS (SELECT 1 FROM provider_configs WHERE source_code = 'alchemy_bsc');

UPDATE provider_configs SET status = 'inactive', updated_at = now()
WHERE source_code = 'alchemy';
