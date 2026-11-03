# Guidebook — OFAC Sanctions Intelligence (Foundation Release)

A step-by-step operational guide to the Foundation + OFAC sanctions
deliverable. Every response shown below is a **real capture** from the dev
environment after importing the live OFAC SDN Advanced XML
(snapshot `ofac-c1279a38fa18-1793687967`, published 2026-11-03 06:39 UTC:
19,484 entities · 1,075 wallet listings · 72 programs).

Raw captures: `docs/feature-maps/assets/live-*.json`.

---

## 1. Prerequisites

```bash
docker compose up -d            # postgres :5433, redis :6380
go run ./cmd/migrate up         # schema (4 migrations)
go run ./cmd/api &              # serves :8080 (HTTP_ADDR to change)
```

Dev bootstrap keys (never production): `API_BOOTSTRAP_ADMIN_KEY`,
`API_BOOTSTRAP_READER_KEY` — see `.env.example`.

Health gates:

```bash
curl -s localhost:8080/healthz   # {"status":"ok"}
curl -s localhost:8080/readyz    # {"postgres":"ok","redis":"ok"}
```

## 2. Import the OFAC Snapshot

```bash
go run ./cmd/worker -source ofac_sdn          # live download + import
go run ./cmd/worker -source ofac_sdn -file sdn.xml   # or a local file
```

The worker downloads over HTTPS from the allow-listed OFAC host, verifies
the SHA-256 checksum, streams the ~128 MB XML through a strict
`xml.Decoder` (DOCTYPE rejected), stages rows, validates, then publishes
atomically in one transaction.

Verify in PostgreSQL:

```sql
SELECT version, published_at, entity_count, wallet_count, program_count
FROM sanctions_snapshots ORDER BY published_at DESC LIMIT 1;
```

```
 ofac-c1279a38fa18-1793687967 | 2026-11-03 06:39:22+00 | 19484 | 1075 | 72
```

```sql
SELECT id, status, records_inserted, snapshot_version FROM ingestion_runs;
-- f57a9812-… | COMPLETED | 43886 | ofac-c1279a38fa18-1793687967
```

Re-running with an unchanged checksum is a **no-op** — no duplicate rows.

## 3. Query a Sanctioned Wallet

```bash
curl -s -H "X-API-Key: $READER" \
  localhost:8080/api/v1/sanctions/wallets/ethereum/0x252a8bd2319d8a555b872990601221b3a2053bce
```

Real response (abbreviated — full file `assets/live-wallet-match.json`):

```json
{
  "data": {
    "matched": true,
    "match_type": "WALLET_EXACT",
    "match_scope": "CHAIN_SPECIFIC",
    "data_status": "AVAILABLE",
    "last_synced_at": "2026-11-03T06:39:22.391136Z",
    "sanctions": [{
      "sanctions_entity_id": 4703,
      "external_entity_id": "24003",
      "entity_name": "MESRI Behzad",
      "programs": [{"code": "CYBER2"}, {"code": "HRIT-IR"}],
      "reason": {"summary": null, "status": "PROGRAM_CONTEXT_ONLY"}
    }],
    "cross_chain_observations": [],
    "unresolved_network_listings": []
  }
}
```

Reading it: **MESRI Behzad** (OFAC entity 24003, Iranian MOIS officer) is a
direct `CHAIN_SPECIFIC` match on Ethereum under programs `CYBER2` +
`HRIT-IR`. `reason.summary` is `null` with status `PROGRAM_CONTEXT_ONLY` —
OFAC publishes no per-address narrative, so the API honestly reports
program context rather than inventing text.

## 4. The Three Match Scopes

The response always separates three evidence classes — SQL `NULL` semantics
can never hide a listing:

| Field | Predicate | Example |
|---|---|---|
| `sanctions` | `network_id = :net` | Direct match on the queried chain |
| `cross_chain_observations` | `network_id <> :net` | Same address listed on another chain — evidence, **not** a direct match |
| `unresolved_network_listings` | `network_id IS NULL` | Source named a currency but no chain — surfaced, never dropped |

Real unresolved example — SUEX OTC (`assets/live-wallet-unresolved.json`),
a wallet that is both a direct ETH match and an unresolvable USDT listing:

```json
"unresolved_network_listings": [{
  "network_code": null, "currency_code": "USDT",
  "listed_address": "0x2f389ce8bd8ff92de3402ffce4691d17fc4f6535",
  "entity_name": "SUEX OTC, S.R.O.", "match_scope": "UNRESOLVED"
}]
```

Of 1,075 real listings: **125 resolved** (124 ethereum + 1 bsc),
**950 unresolved** — mostly USDT/other-currency features OFAC never ties to
a chain.

## 5. Non-Match Is Not "Safe"

```bash
curl -s -H "X-API-Key: $READER" \
  localhost:8080/api/v1/sanctions/wallets/ethereum/0x000000000000000000000000000000000000dEaD
```

```json
{"data": {"matched": false, "data_status": "AVAILABLE",
          "sanctions": [], "unresolved_network_listings": []}}
```

A `false` match means "not on this snapshot" — never "clean". Consumers
must read `data_status`: `AVAILABLE` / `STALE` / `UNAVAILABLE` is recomputed
per response from the latest published snapshot, so a degraded source can
never masquerade as a pass.

## 6. Entity & Program Detail

```bash
curl -s -H "X-API-Key: $READER" localhost:8080/api/v1/sanctions/entities/4703
```

Real entity (`assets/live-entity-detail.json`) — note the provenance
details visible end-to-end:

- 15 digital-currency addresses; `listed_address` preserved **verbatim**
  (`0x1CAb8177ACe78b1B6B1c393371F4f2dCAE40CbEB`) beside lowercase
  `normalized_address` (`0x1cab8177…`).
- XBT/TRX listings carry `network_id: null` — honestly unresolved rather
  than guessed.
- Every row exposes `revoked: false` and `source_identifier_id` (OFAC
  feature ID) for source-level traceability.
- `data_status: "AVAILABLE"` + `last_synced_at` on the envelope.

```bash
curl -s -H "X-API-Key: $READER" localhost:8080/api/v1/sanctions/programs/CYBER2
# {"code":"CYBER2","associated_entities_count":260,"data_status":"AVAILABLE",…}
```

## 7. Errors & Auth (stable envelope)

Every failure is `{error:{code,message,request_id}}` — real captures:

```json
{"code":"UNAUTHORIZED","message":"valid API key required"}        // missing key
{"code":"INVALID_ADDRESS","message":"invalid address"}            // 0xZZZZ…
{"code":"FORBIDDEN","message":"insufficient role"}                // reader → admin route
```

Admin-only surface (`admin` role): `POST /api/v1/admin/ingestion-runs`
(returns `202` + run id), `GET /api/v1/admin/ingestion-runs/{id}`:

```json
{"status":"COMPLETED","records_inserted":43886,
 "snapshot_checksum":"c1279a38fa18…","snapshot_version":"ofac-c1279a38fa18-1793687967"}
```

Rate limiting is Redis-backed per API key; anonymous traffic is bucketed by
client IP — exceeding it returns `RATE_LIMITED` (429).

## 8. SQL Verification Queries

```sql
-- Direct vs unresolved split (mirrors the API partition)
SELECT count(*) FILTER (WHERE network_id IS NOT NULL) AS direct,
       count(*) FILTER (WHERE network_id IS NULL)     AS unresolved
FROM sanctions_wallets WHERE revoked_at IS NULL;
-- 125 | 950

-- Provenance: soft-revoked rows retained, not deleted
SELECT count(*) FROM sanctions_wallets    WHERE revoked_at IS NOT NULL;
SELECT count(*) FROM sanctions_references WHERE revoked_at IS NOT NULL;

-- Per-network listings
SELECT n.code, count(*) FROM sanctions_wallets w
JOIN blockchain_networks n ON n.id = w.network_id
WHERE w.revoked_at IS NULL GROUP BY n.code;
-- ethereum 124 | bsc 1

-- Programs per entity
SELECT p.code, count(*) FROM sanctions_entity_programs ep
JOIN sanctions_programs p ON p.id = ep.program_id GROUP BY p.code
ORDER BY 2 DESC LIMIT 5;

-- Run health incl. heartbeat liveness
SELECT status, heartbeat_at, completed_at FROM ingestion_runs
ORDER BY started_at DESC LIMIT 5;
```

## 9. Operational Runbook

| Task | Command / Check |
|---|---|
| Full verification | `gofmt -l .` · `go vet ./...` · `go build ./...` · `go test -count=1 ./...` · `go test ./... -race` |
| Integration tests | need `*_test` DB (safety guard refuses dev DB) |
| Saturation loop | `scripts/saturation-loop.sh` — 10 shuffled iterations, reports `SCORE: n/10` |
| Migration rollback | `go run ./cmd/migrate down 1 && go run ./cmd/migrate up` |
| Manual import | `go run ./cmd/worker -source ofac_sdn` |
| Stuck run | check `ingestion_runs.heartbeat_at`; stale sweeps act on dead heartbeats |
| Failed run diagnosis | `stg_*` rows retained on failure (`run_id` scoped), deleted on success |

## 10. Guarantees & Non-Guarantees

**Guaranteed:** atomic publish (stage→validate→publish in one tx);
source-scoped soft revocation; never-invented reason text; direct vs
cross-chain vs unresolved never conflated; checksum-identical re-import is
a no-op; cache keys versioned by snapshot so new publishes never serve old
data; concurrent imports blocked by advisory lock + partial unique index.

**Not yet in scope** (later spec phases): public labels, live enrichment,
risk scoring, transaction tracing. A sanctions non-match here carries **no**
risk verdict.
