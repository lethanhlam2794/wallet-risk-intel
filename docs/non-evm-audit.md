# Non-EVM Expansion — Compatibility Audit (Solana + TRON groundwork)

Status: groundwork only — solana/tron are registered INACTIVE
(migration `000026_non_evm_networks`) and rejected at the resolver. Every
item below is a **pre-enablement blocker**; nothing here is live-traffic
behavior. Enablement itself requires an AGENTS.md §15 review.

Audited: 2026-11-06, post-implementation of `AddressFormat`,
`wallet.Normalize` dispatch, and the D4b `is_active` predicates.

## What groundwork already shipped

| Piece | Location |
|---|---|
| `AddressFormat` (`evm\|solana\|tron`, derived from CAIP-2 `namespace`, never a column — D1) | `apps/api/internal/network/address_format.go` |
| Base58 decoder (stdlib `math/big`) + `NormalizeSolana` (base58, 32-byte decode, verbatim) | `apps/api/internal/wallet/base58.go`, `address_solana.go` |
| `NormalizeTron` (base58check, `0x41` + 20B + double-SHA256 checksum, verbatim) | `apps/api/internal/wallet/address_tron.go` |
| `wallet.Normalize(net, raw)` dispatch; `ResolveAndNormalize` routes through it, error contract unchanged | `apps/api/internal/wallet/address.go:51-81` |
| solana/tron rows `is_active=FALSE`; resolver rejection preserved | `apps/api/migrations/000026_non_evm_networks.up.sql` |
| D4b: `AND is_active` on all five code-keyed `bn.code` joins | `apps/api/queries/labels_staging.sql`, `labels_publish.sql` |
| OpenAPI enums extended with INACTIVE marking | `apps/api/api/openapi.yaml` (`parameters.Network`, probe body) |

## Per-module blockers (verified file:line)

### transactions schema — `migrations/000008_transactions.up.sql`
- `tx_hash VARCHAR(255)` (L12): Solana signatures (~88-char base58) and TRON
  64-hex ids fit the column, but are not hex — any hex assumption
  downstream must be audited before enablement.
- `transfer_index VARCHAR(100)` (L13) + `UNIQUE(network_id, tx_hash,
  transfer_index)` (L33): identity scheme for SPL inner instructions /
  TRC-20 log entries is **undefined** — the unique key holds, but the
  indexer must define what a transfer_index is per chain.
- `block_number BIGINT` (L22): fits Solana slots numerically, but slot ≠
  block (skipped slots, commitment levels) — semantics must be renamed or
  reinterpreted; queries that order/compare by it carry EVM assumptions.

### wallet_index_state — `migrations/000008_transactions.up.sql:42-77`
- `indexed_until_block BIGINT` (L55) is a contiguous-coverage **block**
  watermark. Solana commitment-level finality (processed/confirmed/
  finalized) does not map to confirmations-count semantics; the watermark
  contract needs a per-format interpretation before a non-EVM indexer runs.

### provider.TxProvider — `internal/provider/provider.go:242-288`
- The contract is block-window shaped: `TransferLookupOpts.FromBlock/
  ToBlock` (L248-249), `HeadBlock` (L286), and an **ascending-block
  ordering requirement** the coverage watermark depends on (L269-271).
  Slot-based Solana needs a reviewed slot↔watermark mapping.
- `Kinds` (`NATIVE`/`TOKEN`) vs SPL / TRC-20 transfer typing must be mapped
  in the future adapter; comments/adapters assume hex-ish tx_hash.

### tracing — `internal/tracing/`
- BFS keys off `network_id` + `normalized_address`; disjoint address
  alphabets across chains mean no cross-chain path can form — structurally
  safe today. Same-literal cross-chain correlation is a separate feature,
  out of MVP scope.

### sanctions ingestion — `internal/provider/ofac/mapper.go:32-44`
- `resolveListing` maps `ETH→ethereum`, `BSC|BNB→bsc`; `TRX`/`SOL` currency
  codes currently fall through to `("", CHAIN_SPECIFIC)` / UNRESOLVED —
  correct per spec §5.6 (never turn a non-EVM listing into an EVM match).
  TRX/SOL mapping must be defined at enablement; `normalizeListed` keeps
  non-EVM verbatim and should be wired to `wallet.Normalize` later
  (mapper.go:124 currently normalizes EVM only).

### scoring/screening — `address_type` `'evm'` literals
- Literal sites: `queries/transactions.sql:27`,
  `queries/ofac_publish.sql:54`, `queries/labels_publish.sql:9`,
  `queries/stablecoin_freeze.sql:58`.
- The real enablement blocker is `internal/wallet/service.go:25`:
  `Service.GetOrCreate` stamps `AddressType: "evm"` via the `$4` param of
  `GetOrCreateWallet` — its signature has no format/type argument. NOTE:
  `queries/wallets.sql` is **not** a literal site (param-driven); no SQL
  change needed there, only a service-signature change at enablement.

### labels publish path — D4b (fixed this change, re-check pattern)
- Five `bn.code` joins previously matched inactive codes unfiltered:
  `queries/labels_staging.sql` (`StgLabelOrphanNetworks` EXISTS,
  `CountLiveLabelsForStagedNetworks` IN-subquery) and
  `queries/labels_publish.sql` (`PublishEnsureLabelWallets`,
  `PublishUpsertWalletLabels`, `PublishRevokeAbsentWalletLabels`). Without
  the predicates a staged `network_code='solana'` row would have published
  `'0x'+lower(base58)` garbage `wallets` rows and FK-blocked the down
  migration. **Pattern to re-check**: every future code-keyed join on
  `blockchain_networks` must carry `is_active` unless it is the resolver
  itself.

### harness seed assertion — `internal/testutil/testdb_integration_test.go`
- Was `count(*) == 2`; now asserts the `{bsc, ethereum, solana, tron}` code
  set plus `is_active=FALSE` on the two new codes (spec change, not a
  weakened assertion).

### warmindex — `internal/warmindex/queue.go:46-47`
- Stored-address consistency check assumes `NormalizeEVM` form; a non-EVM
  wallet row would fail the check — revisit when non-EVM wallets can exist.

### OpenAPI — `api/openapi.yaml`
- `parameters.Address` keeps `pattern: "^(0x)?[0-9a-fA-F]{40}$"` and
  description "EVM address (20-byte hex…)" — must be generalized per-format
  at enablement; the `Network` enum marking already documents this
  unexercisable combination explicitly.

### networks registry — `cmd/api/main.go:651-658`, `cmd/worker/main.go:285-292`
- `wattrFiles` maps `code + ".csv"` for every resolver-listed network.
  Inactive networks never reach the resolver, so no `solana.csv`/`tron.csv`
  is requested today; at enablement a file (or a different artifact
  mapping) must exist or runs will fail fetch.

### Direct `NormalizeEVM` callers (EVM-correct — stay as-is)
- `internal/transaction/identity.go:88,93,104`,
  `internal/transaction/list.go:91`,
  `internal/provider/ofac/mapper.go:124`,
  `internal/provider/walletattribution/mapper.go:30`,
  `internal/provider/stablefreeze/scanner.go:284`,
  `internal/ingestion/pipeline_freeze.go:176`,
  `internal/warmindex/queue.go:47`.
  These handle EVM-only upstream data (EVM API payloads, OFAC hex
  listings, freeze feeds); they were deliberately not routed through
  `Normalize` — revisit per-adapter at enablement.

## D1 known limitation (recorded)
- `AddressFormat` derives from `namespace` and namespace↔format is not
  universally 1:1 (e.g. `bip122` covers multiple encodings). Unknown
  namespaces fail closed to `""` → `ErrInvalidAddress` — safe at exactly
  the §15-gated enablement moment, but multi-format namespaces will need
  an explicit discriminator when introduced.

## Pending §15 review (not started, by design)
- Flipping `is_active` on solana/tron (traffic enablement).
- Any TxProvider/SnapshotSource adapter code (see
  `non-evm-provider-discovery.md`).
- Transactions/indexer schema semantics for slot-based chains.
- `Service.GetOrCreate` signature (address_type as a parameter).
- OpenAPI `Address` pattern generalization.
