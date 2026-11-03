# Guidebook — Public Wallet Labels (Spec Phase 3)

A step-by-step operational guide to the public wallet-labels deliverable:
ingestion of the community `prettydeath/wallet-attribution` dataset (MIT)
plus the label lookup API. Every response below is a **real capture** from
the dev environment after a live import
(snapshot `wattr-304b40821630-1793699980`, published 2026-11-03 09:58 UTC:
**108,792 labels** — 105,267 ethereum + 3,525 bsc — across **14 categories**).

Raw captures: `docs/feature-maps/assets/live-*.json`.

---

## 1. Prerequisites

Same stack as the OFAC release — see `docs/guidebook-ofac-sanctions.md` §1:

```bash
docker compose up -d            # postgres :5433, redis :6380
go run ./cmd/migrate up         # schema now at version 5 (labels tables)
go run ./cmd/api &              # HTTP_ADDR=:8090 in these captures
```

Dev bootstrap keys: `dev-reader-key-change-me` (reader),
`dev-admin-key-change-me` (admin).

## 2. Import the Label Snapshot

```bash
go run ./cmd/worker -source wallet_attribution            # live download + import
go run ./cmd/worker -source wallet_attribution \
     -file ethereum.csv,bsc.csv                           # or local files
```

The worker dispatches on `data_sources.code` (no source is hardcoded):
downloads `ethereum.csv` + `bsc.csv` over HTTPS from the allow-listed GitHub
host, sha256s each file, stages all rows, validates (per-file staged>0,
network codes resolvable, new categories auto-provisioned), then publishes
in one transaction: entities deduplicated, labels upserted by fingerprint,
and **only the touched networks'** absent labels soft-revoked.

Verify in PostgreSQL:

```sql
SELECT ds.code, s.version, s.row_count, s.checksum
FROM data_source_snapshots s JOIN data_sources ds ON ds.id = s.source_id;
```

```
 wallet_attribution | wattr-304b40821630-1793699980 | 108792 | 304b4082…
```

```sql
SELECT status, records_fetched, records_inserted, snapshot_version
FROM ingestion_runs ORDER BY id DESC LIMIT 1;
-- COMPLETED | 108792 | 108792 | wattr-304b40821630-1793699980
```

Re-running with an unchanged checksum is a **no-op** (<1s verified live) —
no duplicate rows, no new snapshot.

Category coverage in the dev database:

```sql
SELECT rc.code, rc.category_type, COUNT(*) AS labels
FROM wallet_labels wl JOIN risk_categories rc ON rc.id = wl.category_id
WHERE wl.revoked_at IS NULL GROUP BY 1, 2 ORDER BY 3 DESC;
```

| code | type | labels | | code | type | labels |
|---|---|---|---|---|---|---|
| ENTITY | ENTITY_TYPE | 42,009 | | BRIDGE | SERVICE_TYPE | 1,759 |
| EXCHANGE | ENTITY_TYPE | 33,271 | | HACK | MALICIOUS_ACTIVITY | 1,465 |
| DEFI | SERVICE_TYPE | 12,255 | | MINING | SERVICE_TYPE | 202 |
| SCAM | MALICIOUS_ACTIVITY | 7,892 | | SANCTIONED | LEGAL | 164 |
| MEV | SERVICE_TYPE | 3,755 | | SERVICE | SERVICE_TYPE | 83 |
| FROZEN | MALICIOUS_ACTIVITY | 3,199 | | MIXER | SERVICE_TYPE | 62 |
| GAMBLING | SERVICE_TYPE | 2,642 | | EXTREMISM | MALICIOUS_ACTIVITY | 34 |

## 3. Look Up Labels for a Wallet

```bash
curl -s -H "X-API-Key: dev-reader-key-change-me" \
  "http://127.0.0.1:8090/api/v1/wallets/ethereum/0x46ce2da01290ea2d1bced39bba149d017df34cb9/labels"
```

Real response (`live-labels-list.json`, abridged):

```json
{
  "data": {
    "labels": [{
      "wallet_label_id": 326499,
      "category": "EXCHANGE",
      "category_type": "ENTITY_TYPE",
      "label": "FTX 10",
      "entity": {"id": 11149, "name": "ftx"},
      "source": {"code": "wallet_attribution"},
      "source_field": "eth-labels",
      "source_url": "https://github.com/dawsbot/eth-labels",
      "upstream_category": "exchange",
      "confidence": "medium",
      "verification_status": "reported",
      "evidence": {
        "observed_at": "2026-10-01T00:00:00Z",
        "upstream_record": {"label": "FTX 10", "entity": "ftx"},
        "fetch_checksum_part": "0392efcc…"
      },
      "first_seen_at": "2026-11-03T09:58:12.818578Z",
      "revoked_at": null
    }],
    "labels_count": 1,
    "data_status": "AVAILABLE",
    "last_synced_at": "2026-11-03T09:58:12.818578Z"
  }
}
```

Every label carries its **full provenance**: original source field
(`eth-labels`), upstream record verbatim, the checksum of the file it came
from, and first/last-seen timestamps.

### Filters

`category`, `source`, `verification_status`, `active_only`, `cursor`,
`limit` (default 50, max 200):

```bash
curl -s -H "$KEY" ".../labels?category=EXCHANGE"   # match → FTX 10
curl -s -H "$KEY" ".../labels?category=SCAM"       # no match →
```

```json
{"data": {"labels": [], "labels_count": 0, "data_status": "AVAILABLE"}}
```

An empty list is **not** a clean verdict — it means "no reported labels in
this snapshot", surfaced as `labels: []` with honest `data_status`.

`labels_count` counts the total matching the active filters — it does
**not** shrink as you page with `cursor` (fixed in review remediation N-2).

## 4. Labels on the Wallet Endpoint

`GET /api/v1/wallets/{net}/{addr}` embeds a compact summary
(`live-wallet-summary.json`):

```json
"labels": [{"category": "EXCHANGE", "label": "FTX 10",
            "confidence": "medium", "verification_status": "reported",
            "entity": {"id": 11149, "name": "ftx"}}],
"labels_count": 1
```

If the summary query fails, the endpoint still answers — the failure is
logged (`wallet label summary failed`) rather than silently returning an
empty list (remediation N-3).

## 5. Labels vs. Sanctions — Seeing Both on One Wallet

`0x0330070fd38ec3bb94f58fa55d40368271e9e54a` demonstrates the two evidence
layers side by side:

```bash
curl -s -H "$KEY" ".../api/v1/sanctions/wallets/ethereum/0x0330…" 
```

```json
"matched": true,
"sanctions": [{"entity_name": "AMNOKGANG TECHNOLOGY DEVELOPMENT COMPANY",
               "programs": [{"code": "DPRK4"}],
               "match_scope": "CHAIN_SPECIFIC"}]
```

```bash
curl -s -H "$KEY" ".../api/v1/wallets/ethereum/0x0330…/labels"
```

```json
"labels": [{"category": "SANCTIONED", "label": "OFAC sanctioned (ETH)",
            "entity": {"name": "OFAC SDN"}, "confidence": "high",
            "verification_status": "reported"}]
```

The community dataset *also* flags this wallet — but it stays
**`reported`**, never auto-verified. Only the OFAC snapshot itself is the
authoritative sanctions source. This is the spec invariant
"reported ≠ verified" in action (`live-wallet-sanctioned-labeled.json`,
`live-labels-sanctioned-wallet.json`).

The same EVM address can legitimately hold labels on both chains — the
fingerprint includes the network, so ethereum and bsc rows are distinct
labels on distinct `wallets` rows, never merged.

## 6. Error Envelopes

Stable error codes, request id on every line:

```bash
curl -s     ".../labels?cursor=!!!bad"        # → INVALID_CURSOR (400)
curl -s     ".../labels"  (no key)            # → UNAUTHORIZED (401)
curl -s -X POST -H "X-API-Key: <admin>" \
  ".../admin/ingestion-runs" -d '{"source":"nope"}'   # → INVALID_SOURCE
```

```json
{"error": {"code": "INVALID_CURSOR", "message": "invalid cursor",
           "request_id": "fbd5a552-…"}}
```

Captures: `live-labels-error-cursor.json`, `live-labels-error-auth.json`.

## 7. Watching an Import Run

```bash
curl -s -H "X-API-Key: dev-admin-key-change-me" \
  "http://127.0.0.1:8090/api/v1/admin/ingestion-runs/5ecb15de-9634-4321-a011-855caa52b098"
```

```json
{"data": {"status": "COMPLETED", "records_fetched": 108792,
          "records_inserted": 108792, "records_rejected": 0,
          "snapshot_version": "wattr-304b40821630-1793699980",
          "started_at": "…T09:55:59Z", "completed_at": "…T09:59:42Z"}}
```

~3.7 minutes end-to-end on the real 108k-row dataset (capture:
`live-admin-labels-run.json`). A failed run keeps its staging rows for
diagnosis and **never** revokes existing labels; only a fully validated,
successfully published snapshot can soft-revoke — and only within the
networks it actually imported.

## 8. Operational Notes

- **Freshness**: `data_status`/`last_synced_at` are recomputed per request
  from the latest published snapshot — never cached. Label responses are
  cached under a snapshot-versioned key; publishing a new snapshot serves
  the new version automatically.
- **Revocation semantics**: rows absent from a *successful* snapshot get
  `revoked_at` set (soft revoke); they stay queryable with
  `active_only=false`. Nothing is hard-deleted.
- **Partial snapshots**: each provided file must produce staged rows or the
  run FAILS — a truncated/empty upstream file cannot wipe labels.
- **Verification discipline**: this dataset is community-aggregated;
  `verification_status` is always `reported` on import. Do not treat
  GAMBLING/EXCHANGE labels as criminality signals — they are
  classifications (`category_type` makes the distinction explicit).
- **License**: upstream is MIT — checked before integration; keep the
  attribution notice in `data_sources` metadata.
