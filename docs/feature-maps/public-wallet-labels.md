# Technical Feature Map — Public Wallet Labels (Spec Phase 3)

Shipped in commits `c71db06` (implementation) and `519a1b1` (review remediation).
Plan: `plans/20261103-1530-public-wallet-labels/`. Verdict chain:
Critic 17 findings → all amended → Cook DONE → Review APPROVED (5 LOW fixed).

## Source

| Property | Value |
|---|---|
| Upstream | `github.com/prettydeath/wallet-attribution` (MIT — verified) |
| Shape | per-chain CSVs: `ethereum.csv` (105,267 rows), `bsc.csv` (3,525 rows) |
| Fields | `address, network, entity, label, category, source, source_url, confidence, last_updated` |
| Snapshot | multi-file: `provider.Snapshot{Files, Checksum, SizeBytes}`; combined checksum is order-independent (sorted names) |

## Data flow

```
downloader (per-file fetch, sha256, size cap, host allowlist, https-only prod)
  → Importer.importFile: filename-asserted or first-row-derived network;
    rows disagreeing → rejected; wrong-network/invalid-EVM rows counted
  → MapLabel: EVM-normalize address, category normalize (UPPER),
    entity: ""→NULL / whitespace→NULL / "Null" literal preserved,
    confidence map {high,medium,low}→enum else NULL, fingerprint SHA-256
  → stg_wallet_labels (run-scoped staging, kept on failure for diagnosis)
  → validate: per-file staged>0, network codes resolvable, new categories
    upserted (distinct upstream → risk_categories)
  → publish tx (lock_timeout+statement_timeout SET LOCAL):
      upsert entities (normalized name unique), upsert labels by
      fingerprint (refresh last_seen_at), then
      revoke WHERE network-scoped AND last_seen_at < NOW()
  → snapshot row written only after successful publish; run COMPLETED
```

## Key structures

- `wallet_labels`: `fingerprint` unique, `category_id→risk_categories`,
  `entity_id` nullable, `verification_status='reported'` default,
  `evidence` JSONB = `{observed_at, source_field, upstream_record,
  fetch_checksum_part}`, `observed_at` from `last_updated`,
  `first_seen_at`/`last_seen_at`/`revoked_at` provenance.
- Fingerprint preimage: `source|network|address|category|label` (code
  strings only — no DB ids), `internal/provider/walletattribution/mapper.go:20`.
- Revocation: structural `last_seen_at < NOW()` inside the publish tx —
  refreshed rows carry the tx timestamp, absent rows stay older. No
  anti-join (the `NOT IN` variant hit the 120s statement_timeout on 108k
  unstatisticed staging rows — ~10^10 tuple visits).
- Registry: `ingestion.Service` dispatches `data_sources.code → Pipeline`
  (`pipelines`/`fetchers` maps); `ofac_sdn` and `wallet_attribution` are
  independent entries — no OFAC hardcoding in the dispatch path.
- `data_source_snapshots.checksum`: combined multi-file sha256 → identical
  re-import short-circuits to a no-op (<1s verified live).

## API surface

- `GET /api/v1/wallets/{net}/{addr}/labels` — filters `category`, `source`,
  `verification_status`, `active_only`, `cursor`, `limit` (≤200);
  `labels_count` = total matching the filter set, cursor-independent.
- `GET /api/v1/wallets/{net}/{addr}` — embeds compact `labels` summary +
  `labels_count`; summary DB failures are logged, not swallowed.
- `POST /api/v1/admin/ingestion-runs {source:"wallet_attribution"}` —
  admin role; `INVALID_SOURCE` for unknown codes.
- Response envelopes carry `data_status`/`last_synced_at` recomputed per
  request (never from cache payload).

## Invariants enforced

- Labels stay `reported`; only evidence upgrades `verification_status`.
- Original source fields preserved verbatim (`upstream_record`,
  `source_field`, `source_url`, `upstream_category`).
- Per-network revocation only; partial/failed imports never revoke.
- No-data ≠ safe: empty label list returns `labels: []` + `data_status`,
  not a clean verdict.
- GAMBLING/EXCHANGE are classifications (`category_type` distinguishes
  LEGAL/ENTITY_TYPE/etc.), not risk verdicts.

## Verified live (evidence: `docs/feature-maps/assets/live-*`)

- 108,792 labels published; 14 categories; 2,748 linked entities;
  checksum-identical second run = no-op <1s.
- Same EVM address on eth+bsc → two `wallets` rows → two labels with
  distinct fingerprints (network is in the preimage). Example:
  `0x0330070f…` is OFAC-matched (AMNOKGANG, DPRK4) **and** carries a
  `SANCTIONED` label still marked `reported`.

## Remediated review findings (commit `519a1b1`)

- N-1 `SET LOCAL` failure now rolls back before `finish` (run can't
  linger RUNNING).
- N-2 `labels_count` is total-matching-filters, not remaining-after-cursor.
- N-3 wallet label-summary errors logged (`wallet label summary failed`).
- N-4 filename↔network assertion on import (`ethereum.csv` ⇒ ethereum).
- Nits: dead `DataStatus` removed, `INVALID_SOURCE` constant,
  temp-file cleanup, `::text` cast, stray `strconv` import.
- Test-hygiene: `TestDownload_RetryAfterCapped` wall-clock bound 2s→30s
  (parallel-load flake; 1-year-hint regression still caught via timeout).

## Known limits (documented, not bugs)

- Upstream is community-aggregated; `reported` ≠ verified — Phase 5
  scoring must not treat these labels as ground truth.
- ~4,537 upstream rows have empty entity (NULL link, correct); literal
  `"Null"` entity strings are preserved as source data, not silently nulled.
- Pagination >1 page per wallet is untested against live data (upstream
  emits ≤1 label per (address,network)); cursor semantics covered by
  in-memory tests instead.
