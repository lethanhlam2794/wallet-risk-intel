---
phase: 2
title: "sqlc queries + static price/meaningful evaluator"
status: done
priority: P1
dependencies: [1]
---

# Phase 2: sqlc queries + static price/meaningful evaluator

## Overview

Add the three bounded read queries the analyzer needs (counterparty
transfer scan, bulk sanctions match, per-wallet index states) and the
pure pricing evaluator that decides whether a raw transfer is
"meaningful" (USD ≥ floor) under the policy-pinned static price table —
`math/big` integer math only, never floats on money.

## Requirements

- Functional:
  - Query `ListConfirmedCounterpartyTransfers` — one scan shape for both
    directions and both call sites (candidate inbound, receiver-check
    inbound, candidate outbound):
    ```sql
    -- name: ListConfirmedCounterpartyTransfers :many
    SELECT t.from_wallet_id, t.to_wallet_id, t.tx_hash, t.transfer_index,
           t.transfer_type, t.asset_identifier, t.amount_raw::text AS amount_raw,
           t.block_number, t.block_timestamp,
           COALESCE(fw.normalized_address, '') AS from_address,
           COALESCE(tw.normalized_address, '') AS to_address
    FROM transactions t
    LEFT JOIN wallets fw ON fw.id = t.from_wallet_id
    LEFT JOIN wallets tw ON tw.id = t.to_wallet_id
    WHERE t.network_id = $1
      AND t.confirmation_status = 'confirmed'
      AND NOT t.is_error
      AND t.from_wallet_id IS DISTINCT FROM t.to_wallet_id
      AND ((sqlc.arg('direction') = 'in'  AND t.to_wallet_id   = ANY($2::bigint[]))
        OR (sqlc.arg('direction') = 'out' AND t.from_wallet_id = ANY($2::bigint[])))
    ORDER BY t.block_timestamp, t.tx_hash, t.transfer_index
    LIMIT $3;
    ```
    Rows with NULL `from_wallet_id` (defensive) have `from_address=''`
    and can never be a source — the analyzer treats them as
    unsourced evidence rows.
  - Query `BulkMatchSanctionedAddresses` — bulk direct sanctions match
    over a normalized-address set:
    ```sql
    -- name: BulkMatchSanctionedAddresses :many
    SELECT DISTINCT sw.normalized_address, se.id AS sanctions_entity_id
    FROM sanctions_wallets sw
    JOIN sanctions_entities se ON se.id = sw.sanctions_entity_id
         AND se.revoked_at IS NULL
    WHERE sw.network_id = $1
      AND sw.normalized_address = ANY($2::text[])
      AND sw.revoked_at IS NULL;
    ```
  - Query `ListWalletIndexStates` — index-state rows for a SET of
    wallets (candidate + all sources in ONE call, A2): chain-taint
    receiver checks read `to_wallet_id ∈ source_ids` — rows that only
    exist when the SOURCE wallet was itself indexed (lazy per-wallet
    indexing). Without per-source state, `coverage:"complete"` could be
    emitted while every receiver check was unevaluatable.
    ```sql
    -- name: ListWalletIndexStates :many
    SELECT * FROM wallet_index_state
    WHERE network_id = $1 AND wallet_id = ANY($2::bigint[]);
    ```
  - Pricing model in `policy_v11.go`:
    `type AssetPriceV1_1 struct { MicroUSD int64; Decimals int }`,
    `assetKey{network, asset}` table with native sentinel `"native"`.
    Pinned defaults (policy constants, documented as such):
    ETH $3 000 / BNB $600 / USDT·USDC·DAI $1 (decimals 18/18/6/6/18),
    token keys = canonical lowercase contracts on ethereum+bsc
    (USDT eth `0xdac1…31ec7`, bsc `0x55d3…7955`; USDC eth `0xa0b8…eb48`,
    bsc `0x8ac7…580d`; DAI eth `0x6b17…1d0f`, bsc `0x1af3…dbc3`).
  - `PriceForV1_1(network, transferType string, asset *string) (AssetPriceV1_1, bool)`
    — native rows key on the chain coin; token rows key on normalized
    contract; unknown → `false` (unpriced).
  - `IsMeaningfulV1_1(amountRaw string, p AssetPriceV1_1) bool` —
    `big.Int`: `amount × p.MicroUSD ≥ floorMicro × 10^p.Decimals`
    (floorMicro = `meaningfulFloorUSDV1_1 × 1e6`). Invalid/negative
    amount → `false` (never meaningful, never a parse panic).
  - `SCORING_ASSET_PRICES` env: optional JSON
    `{"<net>:<asset|native>":{"micro_usd":N,"decimals":D}}` merged over
    the pinned table at boot; malformed → `Load` error (fail-closed);
    validated entries must have `micro_usd>0`, `0≤decimals≤30`, asset =
    `"native"` or a valid EVM address (normalized via
    `wallet.NormalizeEVM`). Network keys validated against the
    resolver's ACTIVE network codes at wiring time (A14 — non-EVM
    networks exist post-000026; hardcoding `{ethereum,bsc}` would rot);
    unknown net → boot error. EVM-only by deliberate constraint —
    `transactions` indexing is EVM-only today (`BulkResolveWallets`
    'evm' literal).
- Non-functional:
  - No float64 anywhere in the money path.
  - Query plans: bounded scans, deterministic order, explicit LIMIT.

## Architecture

Price evaluation is a **policy artifact** — it lives in `scoring`
(versioned with the policy), not in the analyzer, so the floor rule and
the table are reviewable as one unit. The analyzer only calls
`PriceForV1_1`/`IsMeaningfulV1_1`. Config holds the parsed env overlay;
the merged table is passed to the engine-side evaluator via a package
function taking the table explicitly — the default path uses the pinned
table so tests are hermetic.

Concretely: `type PriceTableV1_1 map[assetKey]AssetPriceV1_1` (unexported
key; constructor `NewPriceTableV1_1(overrides map[string]AssetPriceV1_1)`),
evaluator methods on the table; `scoring.DefaultPriceTableV1_1()`
returns the pinned one. Config parses env → overrides map → screening
Deps carries `PriceTable` (nil → default).

## Related Code Files

- Modify: `apps/api/queries/transactions.sql` (3 new queries),
  `apps/api/queries/sanctions.sql` (bulk match),
  `apps/api/internal/db/` (regenerated via `sqlc generate`)
- Modify: `apps/api/internal/scoring/policy_v11.go` (price table +
  evaluator + `DefaultPriceTableV1_1`),
  `apps/api/internal/scoring/policy_v11_test.go`
- Modify: `apps/api/internal/config/config.go` (`ScoringAssetPrices`
  parsed field + validation), `apps/api/internal/config/config_test.go`

## Implementation Steps (TDD)

1. Red: `policy_v11_test.go` —
   - boundary: exactly $100 meaningful, $99.999999 meaningful=false,
     wei-scale big amounts (1e30) no overflow;
   - unpriced asset → `PriceForV1_1` false;
   - token vs native keying per network (BSC-USDT ≠ ETH-USDT);
   - malformed amount strings → false.
2. Red: `config_test.go` — `SCORING_ASSET_PRICES` valid JSON merges;
   malformed JSON / negative micro_usd / decimals>30 / bad asset → Load
   error; unknown network → error.
3. Red (integration, `tests/`): seed `transactions` rows (confirmed /
   pending / is_error / self-transfer / NULL cases) →
   `ListConfirmedCounterpartyTransfers` returns exactly the confirmed
   non-error non-self set in declared order; `BulkMatchSanctionedAddresses`
   matches only same-network non-revoked; `ListWalletIndexStates` returns
   all source rows.
4. Green: write queries → `sqlc generate` → implement pricing +
   env parsing.
5. `gofmt`, `go build`, `go vet`, targeted tests green.

## Success Criteria

- [ ] `sqlc generate` clean; generated code compiles
- [ ] Boundary tests: 100 USD exact = meaningful; below = false;
      1e38-digit NUMERIC handled via big.Int without overflow
- [ ] Integration test on dedicated test DB passes for all 3 queries
- [ ] No float64 token in money path (`grep -n float64 policy_v11.go` → 0
      hits outside comments)

## Risk Assessment

- NUMERIC text → big.Int parse edge cases (empty, `+` sign, decimals) →
  `canonAmount` parity: digits-only after normalization; tests cover.
- `ANY($2::bigint[])` with empty slice → sqlc passes empty array → zero
  rows; analyzer skips the call when the id set is empty (guard in code).
- ORDER BY determinism for reproducible coverage/first-N evidence →
  pinned `(block_timestamp, tx_hash, transfer_index)` ascending.
- Price-table env drift breaking reproducibility → documented: values
  are deployment-pinned config; default unset keeps pure policy table.
