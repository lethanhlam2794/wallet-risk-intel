---
phase: 1
title: "Seed queries + batch-size config"
status: completed
priority: P2
dependencies: []
---

# Phase 1: Seed queries + batch-size config

## Overview

Add the three keyset-paginated seed SELECTs to a new
`queries/warm_seed.sql`, regenerate sqlc output, and add the
`WarmSeedBatchSize` config knob (`WARM_SEED_BATCH_SIZE`, default 500).

## Requirements

- Functional: three named sqlc queries returning every column the
  classifier needs — row identity (keyset cursor), `network_id`,
  `normalized_address`, network `is_active` + `namespace`; source 3 also
  returns `status` + `history_truncated` + composite cursor.
- Functional: `Config.WarmSeedBatchSize` parsed via existing `envInt`
  (malformed → boot error, matching warm-index knob conventions).
- Non-functional: queries must be keyset-paginated (stable ORDER BY +
  `LIMIT`), never unbounded; parameter style follows the established
  `sqlc.narg`/`sqlc.arg` convention from `queries/transactions.sql`.

## Architecture

```sql
-- name: ListWarmSeedSanctionsBatch :many
SELECT sw.id, sw.network_id, sw.normalized_address,
       bn.is_active AS net_active, bn.namespace AS net_namespace
FROM sanctions_wallets sw
LEFT JOIN blockchain_networks bn ON bn.id = sw.network_id
WHERE sw.revoked_at IS NULL
  AND sw.id > sqlc.narg('cursor_id')::bigint
ORDER BY sw.id
LIMIT sqlc.arg('lim')::bigint;
```
LEFT JOIN is deliberate: `network_id` is nullable and rows with NULL or
inactive/non-EVM networks must stay visible so the seeder can count
`skipped_inactive_net` instead of silently dropping them.

```sql
-- name: ListWarmSeedLabelBatch :many
SELECT wl.id, w.network_id, w.normalized_address,
       bn.is_active AS net_active, bn.namespace AS net_namespace
FROM wallet_labels wl
JOIN wallets w ON w.id = wl.wallet_id
JOIN risk_categories rc ON rc.id = wl.category_id
LEFT JOIN blockchain_networks bn ON bn.id = w.network_id
WHERE wl.revoked_at IS NULL
  AND rc.code IN ('SCAM','PHISHING','HACK','RANSOMWARE','MIXER')
  AND wl.id > sqlc.narg('cursor_id')::bigint
ORDER BY wl.id
LIMIT sqlc.arg('lim')::bigint;
```
Revoked labels are dead evidence and excluded in SQL (same convention as
the partial index `idx_wallet_labels_category`).

```sql
-- name: ListWarmSeedIndexStateBatch :many
SELECT wis.network_id, wis.wallet_id, wis.source_id, wis.status,
       wis.history_truncated, w.normalized_address,
       bn.is_active AS net_active, bn.namespace AS net_namespace
FROM wallet_index_state wis
JOIN wallets w ON w.id = wis.wallet_id
LEFT JOIN blockchain_networks bn ON bn.id = wis.network_id
WHERE (wis.network_id, wis.wallet_id, wis.source_id)
      > (sqlc.narg('cursor_net')::bigint,
         sqlc.narg('cursor_wallet')::bigint,
         sqlc.narg('cursor_source')::bigint)
ORDER BY wis.network_id, wis.wallet_id, wis.source_id
LIMIT sqlc.arg('lim')::bigint;
```
Source 3 returns ALL coverage rows (no status predicate): the Go
classifier distinguishes stale (`partial`/`failed`/`history_truncated`)
from adequate coverage (`skipped_fresh`), which is what makes the
`skipped_fresh` counter honest. Row-value keyset comparisons are already
used in this codebase (`queries/transactions.sql` cursor pattern), so
this shape is proven under sqlc v1.30.0.

Config: `WarmSeedBatchSize int` field beside the `WarmIndex*` block;
`envInt("WARM_SEED_BATCH_SIZE", 500)` in `Load()`. A `<1` value is
rejected by `Seeder.Run` (Phase 2), not at parse time — `envInt` already
fails on malformed input.

## Related Code Files

- Create: `apps/api/queries/warm_seed.sql`
- Create (generated): `apps/api/internal/db/warm_seed.sql.go`
- Modify: `apps/api/internal/config/config.go`

## Implementation Steps

1. `make tools` if needed — sqlc is pinned at v1.30.0
   (`SQLC_VERSION ?= v1.30.0`); do not generate with a different version.
2. Write `queries/warm_seed.sql` with the three queries above.
3. `cd apps/api && sqlc generate` — verify the diff in `internal/db/` is
   scoped to the new `warm_seed.sql.go` only (regeneration must not
   churn unrelated generated files).
4. Add `WarmSeedBatchSize` to `Config` + `Load()` next to
   `WarmIndexMaxPages` block.
5. `go build ./...` — generated code must compile cleanly.

## Success Criteria

- [x] `sqlc generate` produces `internal/db/warm_seed.sql.go` with
  `ListWarmSeedSanctionsBatch`, `ListWarmSeedLabelBatch`,
  `ListWarmSeedIndexStateBatch`; `internal/db` diff scoped to it
- [x] `WarmSeedBatchSize` parses; malformed env → `Load()` error
- [x] `go build ./...` exit 0
- [x] TDD note: query-shape tests land in Phase 2 via the seeder's
  integration suite (queries are exercised through seeded fixtures)

## Risk Assessment

- If sqlc still chokes on the row-value form, fall back to the explicit
  disjunctive keyset `(a > $1) OR (a = $1 AND b > $2) OR
  (a = $1 AND b = $2 AND c > $3)` — same semantics.
- LEFT JOIN nullable fields map to `pgtype`/pointer types in generated
  code — seeder must handle `.Valid == false` (NULL network →
  `skipped_inactive_net`).
- Regeneration churn: verify `git diff internal/db/` touches only
  `warm_seed.sql.go`; wider drift means wrong sqlc version — stop.
