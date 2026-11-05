-- Migration 000006 down: drop the audit table and the provider seeds.
DELETE FROM data_sources WHERE code IN ('goplus', 'publicaml', 'chainabuse');

DROP TABLE provider_observations;
