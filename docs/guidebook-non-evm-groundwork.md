# Guidebook — Non-EVM Expansion Groundwork (Solana + TRON)

A step-by-step operational guide to the groundwork shipped by
`plans/20261106-1555-non-evm-expansion/` (Devin Critic round 2 →
`PLAN_CONSENSUS_REACHED`; Devin Cook TDD; Devin Review → APPROVED;
blind saturation 10/10). **Nothing here enables Solana/TRON traffic** —
this is the registration-and-validation substrate that a future
AGENTS.md §15 review can activate.

Every capture below is a **real response** from the dev environment at
schema v26. Raw captures: `docs/feature-maps/assets/non-evm-*.json|txt`.

---

## 1. What was delivered (groundwork only)

| Piece | Deliverable |
|---|---|
| Address abstraction | `network.AddressFormat` (`evm`/`solana`/`tron`) derived from CAIP-2 namespace; `wallet.Normalize` dispatches on it |
| Validators | `NormalizeSolana` (base58, 32–44 chars, exactly 32 decoded bytes), `NormalizeTron` (base58check, `0x41`+20-byte payload, double-SHA256 checksum) |
| Registry rows | Migration `000026` inserts solana+tron `is_active = FALSE` (CAIP-2 `solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp`, `tron:0x2b6653dc`) |
| Leak guard | All five `bn.code`-keyed label joins gained `AND is_active` |
| Contract | `openapi.yaml` `network` enums extended (marked not-yet-available); `Address` EVM `pattern` noted |
| Findings docs | `docs/non-evm-audit.md`, `docs/non-evm-provider-discovery.md` |

Explicitly **not** delivered (stop conditions held): no TxProvider
adapter, no schema/index change beyond the two registry rows, no
tracing/scoring/screening/ingestion behavior change, no `is_active`
flip, no EVM path change.

## 2. Prerequisites

```bash
docker compose up -d            # postgres :5433, redis :6380
go run ./cmd/migrate up         # schema at version 26
DATABASE_URL=postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable \
REDIS_URL=redis://127.0.0.1:6380/0 HTTP_ADDR=:8099 \
  go run ./cmd/api &
```

## 3. Verify: registry rows exist but are inactive

```sql
SELECT code, name, namespace, chain_reference, is_active
FROM blockchain_networks ORDER BY is_active DESC, code;
```

Real capture (`assets/non-evm-networks-table.txt`):

```
   code   |       name       | namespace |         chain_reference          | is_active
----------+------------------+-----------+----------------------------------+-----------
 bsc      | BNB Smart Chain  | eip155    | 56                               | t
 ethereum | Ethereum Mainnet | eip155    | 1                                | t
 solana   | Solana Mainnet   | solana    | 5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp | f
 tron     | TRON Mainnet     | tron      | 0x2b6653dc                       | f
```

## 4. Verify: `/networks` does not leak inactive rows

```bash
curl -s -H "X-API-Key: $API_BOOTSTRAP_READER_KEY" \
  http://127.0.0.1:8099/api/v1/networks
```

Real capture — **200**, 4.8ms (`assets/non-evm-networks-list.json`):

```json
{"data":[{"code":"bsc","name":"BNB Smart Chain","chain_id":"eip155:56","namespace":"eip155"},
         {"code":"ethereum","name":"Ethereum Mainnet","chain_id":"eip155:1","namespace":"eip155"}]}
```

`ListActiveNetworks` filters `is_active = TRUE`, so the inactive rows are
invisible to the HTTP surface and the resolver.

## 5. Verify: valid non-EVM addresses are still rejected

A **structurally valid** Solana base58 address (System Program) and a
**checksum-valid** TRON address (USDT contract) both fail at network
resolution — never reaching address validation:

```bash
curl -s -H "X-API-Key: $KEY" \
  http://127.0.0.1:8099/api/v1/wallets/solana/So11111111111111111111111111111111111111112
# → 400  {"error":{"code":"INVALID_NETWORK","message":"unsupported network"}}   (3.2ms)

curl -s -H "X-API-Key: $KEY" \
  http://127.0.0.1:8099/api/v1/wallets/tron/TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t
# → 400  {"error":{"code":"INVALID_NETWORK","message":"unsupported network"}}   (4.1ms)
```

## 6. Verify: EVM control unchanged

```bash
curl -s -H "X-API-Key: $KEY" \
  http://127.0.0.1:8099/api/v1/wallets/ethereum/0x098B716B8Aaf21512996DC57EB0615e2383E2f96
```

**200**, 19.4ms — same OFAC-listed wallet payload as before the change
(`assets/non-evm-eth-control-200.json`): mixed-case input normalized to
lowercase `0x098b716b…`, labels returned (SANCTIONED/FROZEN).

## 7. Migration lifecycle

```bash
go run ./cmd/migrate up      # → version 26, inserts 2 inactive rows (ON CONFLICT DO NOTHING → idempotent)
go run ./cmd/migrate down    # → version 25, deletes ONLY rows still is_active=FALSE
```

Down-guard is deliberate: `DELETE … WHERE code IN ('solana','tron')
AND is_active = FALSE`. If a future review activates a row, rollback
leaves it — `TestNonEVM_DownGuardNeverDeletesActive` pins this.

## 8. What activation will require (not done — see audit doc)

Per `docs/non-evm-audit.md`, enabling traffic needs:

1. §15 review sign-off, then `UPDATE blockchain_networks SET is_active=TRUE`.
2. A `TxProvider` adapter per chain (slots vs blocks; base58 vs hex
   signatures; SPL/TRC-20) — candidates evaluated, all findings
   **UNCONFIRMED**: `docs/non-evm-provider-discovery.md`.
3. Transaction-schema semantics decision (slot↔`block_number`,
   signature↔`tx_hash`, `transfer_index` identity).
4. `Service.GetOrCreate` `address_type` generalization (currently
   stamps `'evm'`).
5. OpenAPI `Address` `pattern` generalization; `wattrFiles` mapping.

## 9. Rollback

`go run ./cmd/migrate down` removes the inactive rows; the
`AddressFormat`/validator code is dormant without matching rows (empty
format fails closed → `ErrInvalidAddress`). No EVM behavior to revert —
it never changed.
