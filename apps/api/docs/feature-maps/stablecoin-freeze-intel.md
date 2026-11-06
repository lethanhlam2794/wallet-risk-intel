# Stablecoin Freeze Intelligence — Feature Map & Operational Guidebook

Feature: incremental on-chain scanning of the **USDT/USDC issuer blacklist
event surface on Ethereum**, materialized as `FROZEN` `wallet_labels` with
full provenance. Shipped via migration `000024` + `internal/provider/stablefreeze`
+ `internal/ingestion/pipeline_freeze.go`.

> Verified live 2026-11-06 against real Ankr RPC on the dev stack. All
> outputs below are verbatim terminal/DB captures, not paraphrase.

## 1. What it does (and deliberately does not do)

| Does | Does NOT |
|---|---|
| `eth_getLogs` scans of issuer events: `AddedBlackList`/`RemovedBlackList` (USDT), `Blacklisted`/`UnBlacklisted` (USDC), `DestroyedBlackFunds` (USDT) | Claim *why* an address was frozen — blacklist events carry no reason field; `reason` stays unavailable until an attribution source supplies one |
| Per-contract cursor (`freeze_scan_state`) advanced **only** inside the publish transaction | BSC/Tron (BSC "USDT/USDC" are peg contracts without issuer blacklist methods; out of scope) |
| Delta semantics: freeze→upsert, unfreeze→revoke, destroy→evidence only (never revoke) | Full-snapshot revoke — absence from a scan window never revokes (`PublishRevokeAbsentWalletLabels` is not called) |
| `-until-caught-up` bounded backfill from deploy blocks (USDT `4634748`, USDC `6082465`) | A scheduler/daemon — runs are operator- or admin-triggered |
| Confirmation lag (`FREEZE_SCAN_CONFIRMATIONS=12`) for reorg safety | Interactive `EnsureIndexed` path changes — zero diff there |

## 2. Architecture

```
cmd/worker -mode ingest -source stablecoin_freeze [-until-caught-up]
        │  (also registered in cmd/api fetchers → POST /admin/ingestion-runs works)
        ▼
stablefreeze.Scanner (SnapshotSource)          cursor read (fail-closed)
   eth_blockNumber → head                       freeze_scan_state
   eth_getLogs per contract, paged              window = (last_scanned .. min(head−conf, +MaxBlocksPerRun)]
   eth_getBlockByNumber batch → block_ts        JSONL artifact + checksum
        ▼
ingestion pipeline_freeze (Import→Validate→Publish, one tx)
   ensure wallets → upsert FROZEN labels → keyed destroy_events append
   → chronological unfreeze revoke → orphan-unfreeze no-op → cursor GREATEST
        ▼
wallet_labels (source=stablecoin_freeze) + freeze_scan_state + ingestion_runs
```

### File map

| Path | Role |
|---|---|
| `migrations/000024_stablecoin_freeze.{up,down}.sql` | `freeze_scan_state` (PK `network_id,contract_address`, seeded), `stg_freeze_events`, `data_sources` row |
| `queries/stablecoin_freeze.sql` + `internal/db/stablecoin_freeze.sql.go` | cursor read/GREATEST update, staging, delta publish set |
| `internal/provider/stablefreeze/` | `contracts.go` (registry + topic0s), `rpc.go` (JSON-RPC, SSRF, batch-by-id), `scanner.go`, `fingerprint.go` (`"stablecoin_freeze\|"` namespace), `build.go` (providerconfig→scanner) |
| `internal/ingestion/pipeline_freeze.go` | delta pipeline; boot assertion that all `StagingNames()` exist in both staging maps (`service.go`) |
| `queries/labels_read.sql` | `LatestLabelSnapshotPublished` broadened → freeze publishes bump `/wallets/labels` + `/screenings` cache versions |
| `cmd/worker/main.go`, `cmd/api/main.go`, `internal/config/config.go` | wiring + `FREEZE_SCAN_*` config |
| `tests/freeze_scan_state_test.go`, `tests/freeze_pipeline_test.go`, `internal/provider/stablefreeze/*_test.go` | coverage |

### Config (`.env`)

```
FREEZE_SCAN_BLOCK_RANGE=50000          # blocks per eth_getLogs page
FREEZE_SCAN_MAX_BLOCKS_PER_RUN=2000000 # per-run coverage cap
FREEZE_SCAN_CONFIRMATIONS=12           # head lag floor
FREEZE_SCAN_RPC_PROVIDER=ankr          # provider_configs row used for RPC
```

Fail-closed: missing/inactive provider config → fetcher omitted → runs land
FAILED ("no downloader configured"), never fabricated empty success.

## 3. Runbook

```bash
cd apps/api
make deps-up                 # postgres + redis
go build -o /tmp/freeze-worker ./cmd/worker

# incremental one-shot (scans to head−12):
/tmp/freeze-worker -mode ingest -source stablecoin_freeze

# bounded backfill until all watched cursors reach head−conf:
/tmp/freeze-worker -mode ingest -source stablecoin_freeze -until-caught-up
```

Observed live (incremental run, 2026-11-06):

```
run_id=db109013-c0d9-4ddd-886f-89ed23d31406 status=COMPLETED version=freeze-26353712-1793942847-db109013
```

`-until-caught-up` correctly refuses file mode (would loop forever — a
file artifact cannot advance cursors past its own `toBlock`):

```
$ /tmp/freeze-worker -mode ingest -source stablecoin_freeze -file /tmp/x -until-caught-up
worker: -until-caught-up requires chain-scan mode (no -file); a file-mode artifact cannot advance cursors past its own toBlock   → exit 1
```

## 4. Live database state (verified output)

### Cursors — pinned exactly at head−confirmations

```sql
SELECT c.contract_address, c.last_scanned_block, c.last_head_block,
       c.last_head_block - c.last_scanned_block AS gap
FROM freeze_scan_state c ORDER BY c.contract_address;
```
```
              contract_address              | last_scanned_block | last_head_block | gap
--------------------------------------------+--------------------+-----------------+-----
 0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48 |           26353700 |        26353712 |  12
 0xdac17f958d2ee523a2206206994597c13d831ec7 |           26353700 |        26353712 |  12
```

### Labels by contract

```sql
SELECT wl.label, wl.evidence->>'contract' AS contract, count(*) total,
       count(*) FILTER (WHERE wl.revoked_at IS NULL)  active,
       count(*) FILTER (WHERE wl.revoked_at IS NOT NULL) revoked,
       count(*) FILTER (WHERE wl.evidence ? 'destroy_events') with_destroy
FROM wallet_labels wl JOIN data_sources ds ON ds.id=wl.source_id
WHERE ds.code='stablecoin_freeze' GROUP BY 1,2 ORDER BY 1;
```
```
     label      |                  contract                  | total | active | revoked | with_destroy
----------------+--------------------------------------------+-------+--------+---------+--------------
 usdc_blacklist | 0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48 |   886 |    689 |     197 |            0
 usdt_blacklist | 0xdac17f958d2ee523a2206206994597c13d831ec7 |  3198 |   2855 |     343 |         1208
```

(USDC exposes no destroy event → `with_destroy=0` is correct, not a gap.)

### Run history — failures stay visible

```sql
SELECT status, count(*) FROM ingestion_runs
WHERE source_id=(SELECT id FROM data_sources WHERE code='stablecoin_freeze') GROUP BY 1;
```
```
  status   | count
-----------+-------
 FAILED    |     3     -- pre-fix attempts + one live upstream 429; cursor never advanced past them
 COMPLETED |    23
```

`stg_freeze_events` holds **0** rows after runs (staging purge verified).

### Real-world detection — Binance Hot Wallet Exploiter

`0x5227f603c1ee34ce3c06efe6477003bdfccb2f8c` (≈1.175B USDT + 823M USDC
frozen by both issuers on 2026-10-29) — captured by the scanner with both
issuer events:

```sql
SELECT wl.label, wl.evidence->>'event' AS ev, wl.evidence->>'block_number' AS blk,
       to_timestamp((wl.evidence->>'block_ts')::bigint) AS ts_utc, wl.source_url
FROM wallet_labels wl JOIN data_sources ds ON ds.id=wl.source_id
JOIN wallets w ON w.id=wl.wallet_id
WHERE ds.code='stablecoin_freeze'
  AND w.normalized_address='0x5227f603c1ee34ce3c06efe6477003bdfccb2f8c'
ORDER BY 1;
```
```
     label      |       ev       |   blk    |         ts_utc         |                                         source_url
----------------+----------------+----------+------------------------+--------------------------------------------------------------------------------------------
 usdc_blacklist | Blacklisted    | 26300971 | 2026-10-29 21:04:59+00 | https://etherscan.io/tx/0xc954c86f118e8e410e6a3eb9e3b38ff4a95fbdae9d2d58e13166afb7a1f4fc5f
 usdt_blacklist | AddedBlackList | 26300954 | 2026-10-29 21:01:35+00 | https://etherscan.io/tx/0x7baba482077843514d9c72bac0110ba0916b0814288a72917e2c3bb27a9d8659
```

### Evidence shape (truncated real row)

A destroy-first label (orphan `DestroyedBlackFunds` → upsert, later
`RemovedBlackList` → revoke with merged evidence):

```jsonc
{
  "event": "DestroyedBlackFunds",
  "topic": "0x61e6e66b0d6339b2980aecc6ccc0039736791f0ccde9ed512e789a7fbdd698c6",
  "tx_hash": "0x3b54c7b1…8275", "block_number": 5758964, "block_ts": 1528547555,
  "contract": "0xdac17f958d2ee523a2206206994597c13d831ec7",
  "destroy_events": { "0x…:8": { "event": "DestroyedBlackFunds", "tx_hash": "…", "block_number": 5758964, "block_ts": "2018-06-09T12:32:35+00:00" } },
  "unfreeze_event":   { "tx_hash": "0x520a66…5ba8", "block_number": 4638882, "block_ts": "2017-11-28T16:59:39+00:00" }
}
```

Schema (`\d+` live):

- `freeze_scan_state`: PK `(network_id, contract_address)`; `genesis_block`,
  `last_scanned_block`, `last_head_block`, `updated_at`; FK→`blockchain_networks`.
- `stg_freeze_events`: PK `(run_id, contract_address, tx_hash, log_index)`;
  `event_type CHECK IN ('freeze','unfreeze','destroy')`; fingerprint index.

## 5. Semantics contract (enforced + tested)

1. **Destroy ≠ revoke** — `DestroyedBlackFunds` burns funds while the address
   stays blacklisted; it only appends keyed `destroy_events` evidence.
2. **Absence ≠ revoke** — only explicit unfreeze events revoke. Orphan
   unfreeze (no prior label) is a visible no-op, never an error.
3. **Atomic cursor** — `freeze_scan_state` advances via `GREATEST` inside the
   publish tx; a mid-backfill 429 left cursors untouched and resumed cleanly.
4. **Reorg guard** — `Removed:true` logs rejected; head−12 lag bounds exposure.
5. **Fingerprint twin** — `"stablecoin_freeze|network|address|category|label"`
   namespace; never collides with `wallet_attribution`.
6. **Provenance, never invention** — `verification_status=verified`,
   `confidence=high`, etherscan tx `source_url`; **no reason is fabricated** —
   freeze events prove the freeze, not the motive.

## 6. Tests

| Suite | Coverage |
|---|---|
| `internal/provider/stablefreeze/scanner_test.go` | topics pinned, SSRF, cursor fail-closed, paging, two-word destroy regression, degenerate window, malformed logs, ctx cancel, **`TestBlockTimestamps_OutOfOrderBatch`** (id-rebound batch) |
| `internal/provider/stablefreeze/build_test.go` | endpoint derivation, fail-closed matrix |
| `tests/freeze_scan_state_test.go` | schema, seeds, cursor monotonicity, staging idempotency |
| `tests/freeze_pipeline_test.go` | full `RunSync`: freeze upsert, unfreeze revoke+merge, destroy retention, orphan unfreeze, refreeze, cursor, idempotent replay, malformed rejection, snapshot version, admin trigger |

Verification commands (all run green during pipeline Stages 2–4):

```bash
go test -count=1 ./internal/provider/stablefreeze/ ./tests/ -run 'Freeze'
go test -count=10 -shuffle=on ./internal/provider/stablefreeze/   # 10/10
go test -race -count=1 ./tests/ -run 'Freeze'
go test -count=1 ./...                                          # full suite
```

## 7. Limitations & follow-ups

- **Ethereum only.** `stg_freeze_events` joins currently pin contract
  address alone; before seeding a second network for the same contract,
  add `network_id` to staging (review finding F4 — inert today).
- **No reason enrichment.** `reason` stays unavailable until an attribution
  source (Chainabuse, OFAC update, curated feed) supplies it — Chainabuse
  API-key onboarding is a manual follow-up.
- **Worker boot dep:** file-less ingest now resolves providerconfig even for
  non-freeze sources (`-mode ingest -source ofac_sdn` needs a working
  keystore/`ResolveAll`, consistent with `cmd/api` boot posture).
- **No scheduler** — wire cron/systemd externally if periodic scanning wanted.
- Migration `000024` was edited post-first-apply locally (USDC address fix);
  fresh installs get the correct seed directly.
