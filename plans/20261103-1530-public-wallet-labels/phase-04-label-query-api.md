# Phase 04 — Label Lookup API

## Context

Spec §9.8: `GET /wallets/{network}/{address}/labels`. Reuses
`wallet.ResolveAndNormalize`, error envelope, auth (reader role),
`data_status` freshness pattern from sanctions service.

## Route

`GET /api/v1/wallets/{network}/{address}/labels`

## Response (proposed — deviations recorded in this file)

```json
{
  "data": {
    "network": "ethereum",
    "address": "0x…",
    "normalized_address": "0x…",
    "labels": [
      {
        "wallet_label_id": 12,
        "category": "EXCHANGE",
        "category_type": "ENTITY_TYPE",
        "upstream_category": "exchange",
        "label": "Binance 14",
        "entity": {"id": 5, "name": "Binance"} ,
        "confidence": "high",
        "verification_status": "reported",
        "source": {"code": "wallet_attribution", "name": "…"},
        "source_url": "https://github.com/dawsbot/eth-labels",
        "source_field": "eth-labels+cex-list",
        "observed_at": "2026-10-01",
        "first_seen_at": "…", "last_seen_at": "…",
        "evidence": {…}
      }
    ],
    "labels_count": 1,
    "data_status": "AVAILABLE",
    "last_synced_at": "…",
    "next_cursor": null
  }
}
```

Rules:
- `labels: []` + `data_status: AVAILABLE` is a valid response — **missing
  labels are never a safety verdict** (spec §1996 anti-pattern)
- `data_status` reuses the sanctions enum `AVAILABLE|STALE|UNAVAILABLE`
  (F9): `UNAVAILABLE` when no published `data_source_snapshots` row exists
  for `wallet_attribution`; `STALE` past `LABELS_STALE_AFTER` config
  (default 168h — upstream rebuilds ~monthly); freshness recomputed per
  response and **excluded from cached payloads**, exactly like sanctions
- `last_synced_at` = latest `data_source_snapshots.published_at` for
  `wallet_attribution` (new query — `LatestSnapshotPublished` in
  sanctions.sql is `ofac_sdn`-hardcoded, do not reuse)
- `labels_count` = **total matching** the active filter set (not the
  returned page size — documented)
- Revoked labels only visible with `active_only=false`; include `revoked_at`
- GAMBLING/EXCHANGE labels are classification info — response contains no
  risk verdict field at all

## Filters (§9.8 + F11)

- `category` — normalized code (e.g. `EXCHANGE`) or upstream (`exchange`)
- `source` — `data_sources.code` (`wallet_attribution` today;
  forward-compatible for future label sources)
- `source_field` — upstream contributor string filter: exact match or
  `ILIKE '%token%'` on `source_field` (e.g. `?source_field=forta`) — makes
  the §9.8 `source` dimension real instead of a silent no-op
- `verification_status` — `reported|unverified|verified|disputed`
- `active_only` — default `true`
- `cursor` — opaque, id-ordered (base64(label id))
- `limit` — default 50, max 200

## Wallet endpoint (F10 decision — populate, don't defer)

`GET /wallets/{net}/{addr}` currently emits `"labels": []` reserved.
Populate it: `labels` = first 5 active labels (compact shape) +
`labels_count` = active total. Wallet-level `data_status` stays
**sanctions-scoped** (documented in OpenAPI); label freshness lives on
the §9.8 response only — two snapshot domains, no conflation.

## Cache (F15)

- `wallet:labels:{net}:{addr}:{snapshot_version}` TTL 1h — versioned
  (deviation from spec literal key; consistent with anti-stale invariant).
  `cache.WalletLabels(network, addr)` helper **already exists** with the
  unversioned key (cache/keys.go:15-17) — change its signature to take
  version; update call sites.
- `singleflight.Group` on the labels service lookup path (spec §14
  stampede rule — same pattern as sanctions)
- Negative (empty labels) results cached under the same versioned key;
  freshness fields never enter the cached payload

## Errors

- invalid network/address → existing codes
- `cursor` malformed → `INVALID_CURSOR` 400 — **new code** added to
  `middleware/errors.go` (does not exist today)
- otherwise stable envelope

## Tests

- labeled wallet → full shape + filters (each filter incl. active_only=false)
- unlabeled wallet → `labels: []`, `data_status` present
- invalid cursor → 400; pagination across >limit rows → stable order
- revoked label hidden by default, shown with flag
- cache: second request served from Redis (key contains snapshot version);
  after new publish, new key namespace
- auth: no key → 401; reader ok
- GAMBLING/EXCHANGE label returns no risk-ish fields (shape assertion)
