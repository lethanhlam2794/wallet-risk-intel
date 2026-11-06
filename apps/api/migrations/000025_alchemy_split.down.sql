-- Remove the per-chain split rows. The legacy 'alchemy' row is NOT
-- reactivated here — re-enable it manually if rolling back for real use.
DELETE FROM provider_configs WHERE source_code IN ('alchemy_eth', 'alchemy_bsc');
DELETE FROM data_sources WHERE code IN ('alchemy_eth', 'alchemy_bsc');
