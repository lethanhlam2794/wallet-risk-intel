---
phase: 5
title: "Sanctions Query API + Snapshot-Versioned Cache"
status: completed
priority: P1
dependencies: [4]
---

# Phase 5: Sanctions Query API + Snapshot-Versioned Cache

## Overview

Public read APIs over the published OFAC data: wallet sanctions lookup
(spec §9.3 contract), entity detail (§9.4), program detail (§9.5) — with the
spec §8.3 reason model, chain-specific match semantics, cross-chain
observations, freshness metadata, and Redis caching keyed by snapshot
version.

## Requirements

- Functional endpoints (reader role):
  - `GET /api/v1/sanctions/wallets/{network}/{address}` — direct match:
    `normalized_address` + `network_id` + `revoked_at IS NULL`; returns
    `matched`, `match_type:"WALLET_EXACT"`, `match_scope:"CHAIN_SPECIFIC"`,
    `sanctions[]` (list, entity_name, programs[], reason{}, source{} —
    **deduplicated by `sanctions_entity_id`**: OFAC re-lists the same
    address+currency under different `Feature@ID`s for one entity and the
    response must not double-report),
    `cross_chain_observations[]` (same normalized address on an **evidenced
    other network**: `network_id IS NOT NULL AND network_id <> :resolved`),
    `unresolved_network_listings[]` (**`network_id IS NULL` rows** — chain
    unresolvable; never counted in `matched`, always surfaced so a listed
    address can never silently read as "no data"),
    `data_status`, `last_synced_at`.
  - `GET /api/v1/sanctions/entities/{id}` — entity, programs,
    digital_currency_addresses, official_references, details,
    `last_synced_at`.
  - `GET /api/v1/sanctions/programs/{code}` — code, name, description,
    source_url, associated_entities_count.
- Reason model (§8.3): `summary` only when official source text exists →
  `OFFICIAL_EXPLICIT`; else programs-only → `PROGRAM_CONTEXT_ONLY`; else
  `null` + `NOT_AVAILABLE`. Never synthesize text.
- `data_status`: `AVAILABLE` (snapshot published & fresh),
  `STALE` (snapshot older than configured freshness threshold),
  `UNAVAILABLE` (no snapshot ever published). Non-match is a data answer,
  not a safety verdict.
- Cache: `wallet:sanctions:{net}:{addr}:{snapshot_version}` TTL 1h; lookups
  resolve current version first (`sanctions:current_snapshot`, short TTL +
  DB fallback); publish bump makes old keys unreachable (spec §14).

## Architecture

- `internal/sanctions/{model,repository,service,handler}.go`; queries in
  `queries/sanctions.sql` (sqlc).
- Lookup flow: normalize+resolve (shared helper from phase 02) → cache get →
  repo match query → assemble entity+programs+references → reason status →
  cache set → respond.
- `cross_chain_observations` vs `unresolved_network_listings`: explicit
  predicates — `network_id IS NOT NULL AND network_id <> :resolved` vs
  `network_id IS NULL` (Postgres three-valued logic: `!=` silently drops
  NULL — this was Critic finding A1); both `revoked_at IS NULL`; return
  network+currency+entity_name; **never** counted into `matched`. For NULL
  rows `match_scope` is stored as `'UNRESOLVED'` (documented semantics;
  never `CHAIN_SPECIFIC`).
- Freshness: `last_synced_at` = latest `sanctions_snapshots.published_at`;
  `data_status` computed vs `SANCTIONS_STALE_AFTER` (default 48h).
- Negative caching: non-match entries cached too — the versioned key
  separates epochs; cache stores `{matched:false}` result, never provider
  errors. **Freshness fields (`data_status`, `last_synced_at`) are
  recomputed at serve time and excluded from the cached payload** — a
  cached `AVAILABLE` must never outlive the staleness threshold.
- `/sanctions/entities/{id}` contract: `{id}` = internal
  `sanctions_entities.id` PK (documented in OpenAPI);
  `external_entity_id` returned in body.
- `associated_entities_count` counts `revoked_at IS NULL` entities only.

## Related Code Files

- Create: `internal/sanctions/model.go`, `repository.go`, `service.go`,
  `handler.go`, `reason.go`; `queries/sanctions.sql`
- Modify: `internal/httpserver/server.go` (routes), `internal/cache`
  key registry additions.

## Implementation Steps (TDD)

1. Red (`tests/sanctions_lookup_test.go`): publish fixture via phase-04 path
   → lookup listed EVM address → `matched:true`, entity name, program codes,
   source block; non-listed address → `matched:false`, `data_status` set,
   `sanctions:[]`.
2. Red: same normalized address on ETH only → BSC query returns
   `matched:false` + entry under `cross_chain_observations` (never flips
   `matched`); **USDT+0x listing (network NULL) → ethereum query →
   `matched:false` + entry under `unresolved_network_listings`** (A1
   regression: listed address can never read as "no data").
3. Red: entity with programs but no official reason text →
   `reason:{summary:null,status:"PROGRAM_CONTEXT_ONLY"}`; entity with none →
   `NOT_AVAILABLE`; assert no fabricated strings.
4. Red: cache test — first lookup misses→DB; second hits cache (assert via
   DB query count or cache key existence); publish new snapshot → next
   lookup misses old-version key.
5. Red: `/sanctions/entities/{id}` 200 + 404 unknown; `/programs/{code}`
   200 + count correct + 404 unknown; unauthenticated → 401.
6. Green: implement repo/service/handlers/cache.
7. Contract check: response shape matches spec §9.3 example keys exactly.

## Success Criteria

- [x] Exact-match E2E proven on `_test` db (fixture → ingest → GET 200).
- [x] ETH listing never satisfies a BSC direct match (negative test).
- [x] Reason never invented; three statuses each covered by a test.
- [x] Cache key contains snapshot version; post-publish invalidation proven.
- [x] `data_status` covers AVAILABLE/STALE/UNAVAILABLE paths.
- [x] Response keys match spec example contract.
- [x] `go test ./...` green; build/vet/fmt clean.

## Risk Assessment

- Cache stampede on hot address → singleflight around repo fetch (spec §14).
- Stale snapshot served as current → versioned keys + `data_status`
  freshness fields; reviewer grep asserts version in key.
- Cross-chain leakage into `matched` → dedicated test (above).
- Entity/program fan-out N+1 → sqlc queries join or batch fetch per result
  set; lookups are single-round per wallet match.
