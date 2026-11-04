# Blockchair viability probe — phase 09

**NOT-VIABLE: no BNB Smart Chain coverage.**

## Probe evidence (keyless, 2026-11-04)

| Request | Result |
|---|---|
| `GET https://api.blockchair.com/bnb-smart-chain/stats` | HTTP 404 |
| `GET https://api.blockchair.com/binance-smart-chain/stats` | HTTP 404 |
| `GET https://api.blockchair.com/ethereum/stats` | HTTP 200, live `data.blocks` |

Both plausible BNB chain slugs 404 while the Ethereum sibling endpoint
serves live data — the chain genuinely is not indexed, not an auth gate
(keyless tier serves the same endpoints on supported chains).

## Documentation cross-check

- `blockchair.com/api/docs` supported-chain list: Bitcoin, Bitcoin Cash,
  Litecoin, Bitcoin SV, Dogecoin, Dash, Groestlcoin, Zcash, Ethereum
  (+testnet), EOS, Tezos, Mixin, Cardano, Monero, Stellar, Ripple —
  **no BNB Smart Chain** entry. The only EVM-family coverage is Ethereum.
- Phase scope was BSC (ETH is already covered by chainscan + blockscout),
  so an ethereum-only adapter adds nothing this plan needs.

## Decision

Phase 09 completes as NOT-VIABLE — no adapter code written. Revisit only
if Blockchair adds a BNB-family index later.
