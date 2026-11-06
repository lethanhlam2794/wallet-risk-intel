-- Migration 000026 down: remove the inactive groundwork rows only.
-- The is_active=FALSE guard makes this a no-op against any future
-- enablement — an activated network is never reverted by this rollback.
-- wallets.network_id carries no cascade; inactive rows must never gain
-- wallet references (enforced by the labels-publish is_active predicates).

DELETE FROM blockchain_networks
WHERE code IN ('solana', 'tron') AND is_active = FALSE;
