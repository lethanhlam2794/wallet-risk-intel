-- Dedicated integration-test database (P-020). Created once at first boot.
SELECT 'CREATE DATABASE check_wallet_test'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'check_wallet_test')\gexec
