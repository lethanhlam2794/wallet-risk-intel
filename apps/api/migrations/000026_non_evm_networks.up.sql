-- Migration 000026: register solana and tron as INACTIVE networks.
-- Groundwork only — is_active=FALSE keeps them invisible to
-- ListActiveNetworks, so the resolver keeps rejecting both codes with
-- ErrInvalidNetwork until a future AGENTS.md §15-reviewed enablement.

INSERT INTO blockchain_networks (namespace, chain_reference, code, name, is_active) VALUES
    ('solana', '5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp', 'solana', 'Solana Mainnet', FALSE),
    ('tron', '0x2b6653dc', 'tron', 'TRON Mainnet', FALSE)
ON CONFLICT DO NOTHING;
