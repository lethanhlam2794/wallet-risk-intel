# Stage 5 — Live Surface Evidence (internal-only: CLI + DB)

**Classification**: `internal-only`. The change adds a CLI worker mode
(`go run ./cmd/worker -mode warmseed`) — no UI route, no new or modified
API endpoint, `api/openapi.yaml` untouched (binding prompt forbids
both). Browser viewports and `curl` surfaces are N/A; the honest live
surface for this feature is the real binary + real Postgres.

**Environment**: scratch DB `check_wallet_warmseed_smoke_test`
(127.0.0.1:5433, created + migrated 2026-11-07, name ends `_test` —
never the dev DB).

## Fixtures

- `sanctions_entities` SMOKE-ENT-1 + 6 `sanctions_wallets` rows:
  `0x…0b01` eligible eth; `0x…0b02` NULL network_id; TRON Base58 on
  inactive tron net; `not-evm-smoke` on eth; `0x…0b03` revoked;
  `0x…0B04` uppercase non-canonical on eth.
- `wallet_labels`: SCAM on `0x…0c01`, EXCHANGE on `0x…0c02`.
- `wallet_index_state`: `partial` on `0x…0c03`, `fresh` on `0x…0c01`.

## Run 1 — `WARM_INDEX_ENABLED=1 go run ./cmd/worker -mode warmseed` → exit 0

```json
{"level":"WARN","msg":"warm seed row rejected (non-canonical address)","source":"ofac_list","addr":"0x0000…0B04"}
{"msg":"warm seed source complete","source":"ofac_list","scanned":5,"enqueued":1,"skipped_non_evm":1,"skipped_inactive_net":2,"skipped_fresh":0,"errors":1}
{"msg":"warm seed source complete","source":"risky_label","scanned":1,"enqueued":1,"skipped_non_evm":0,"skipped_inactive_net":0,"skipped_fresh":0,"errors":0}
{"msg":"warm seed source complete","source":"refresh_stale","scanned":2,"enqueued":1,"skipped_non_evm":0,"skipped_inactive_net":0,"skipped_fresh":1,"errors":0}
{"msg":"warm seed complete","scanned":8,"enqueued":3,"skipped_non_evm":1,"skipped_inactive_net":2,"skipped_fresh":1,"errors":1}
```

Counter verification: 6 sanctions rows − 1 revoked = **scanned 5**;
`0b02` NULL-net + tron-net → **skipped_inactive_net 2**; `not-evm-smoke`
→ **skipped_non_evm 1**; `0B04` → **errors 1** (masked log confirmed);
EXCHANGE label SQL-excluded → labels **scanned 1**; fresh row →
**skipped_fresh 1**. All six keys on every source line.

## DB verification (`psql`)

```sql
SELECT wq.priority, wq.reason, wq.status, wq.address, bn.code AS net
FROM warm_queue wq JOIN blockchain_networks bn ON bn.id=wq.network_id
ORDER BY wq.priority DESC;
```
```
 priority |       reason       | status  |                  address                   |   net
----------+--------------------+---------+--------------------------------------------+----------
       20 | seed:ofac_list     | pending | 0x0000000000000000000000000000000000000b01 | ethereum
       10 | seed:risky_label   | pending | 0x0000000000000000000000000000000000000c01 | ethereum
        5 | seed:refresh_stale | pending | 0x0000000000000000000000000000000000000c03 | ethereum
```

## Run 2 — idempotent replay

Same command again → same counters (`enqueued=3` honest attempts) and
`SELECT count(*) FROM warm_queue WHERE status IN ('pending','claimed')`
= **3** — zero duplicate active rows.

## Disabled mode — `WARM_INDEX_ENABLED=0`

After `TRUNCATE warm_queue`: logs `warm index disabled
(WARM_INDEX_ENABLED); exiting`, exit 0, `warm_queue` count = **0**.

## Unknown mode — `-mode bogus`

`worker: unknown -mode "bogus"` → `exit status 1` (dispatch regression
guard intact).
