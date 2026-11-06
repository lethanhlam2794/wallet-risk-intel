---
phase: 2
title: "Inactive Solana/TRON network registration (migration 000026)"
status: completed
priority: P1
dependencies: [1]
---

# Phase 2: Inactive network registration — migration + resolver rejection proof

## Overview

Register `solana` and `tron` in `blockchain_networks` marked **INACTIVE**
using the existing `is_active` column (verified present in
`000001_init.up.sql` L10). Because `ListActiveNetworks` filters
`WHERE is_active = TRUE`, the resolver continues to reject both codes with
`ErrInvalidNetwork` → HTTP 400 — zero behavior change, no new flag invented.

## Requirements

- Functional:
  - Migration `000026_non_evm_networks.up.sql`: insert both rows with
    `is_active = FALSE`, idempotent (`ON CONFLICT DO NOTHING` — no target,
    silently skips collisions on either unique; acceptable for fixed seed
    values, and the integration test asserts exact row contents so drift
    is caught downstream).
  - `000026_non_evm_networks.down.sql`: `DELETE ... WHERE code IN
    ('solana','tron') AND is_active = FALSE` — never reverts a future
    enablement; `wallets.network_id` has NO cascade, so inactive rows must
    never gain wallet references (see D4b below).
  - Rows carry correct CAIP-2 coordinates: `('solana',
    '5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp', 'solana', 'Solana Mainnet')`,
    `('tron', '0x2b6653dc', 'tron', 'TRON Mainnet')`.
  - **D4b — SQL-layer `is_active` predicates (post-debate amendment).**
    Five `bn.code` joins ignore `is_active`; after 000026 a staged
    `network_code='solana'`/`'tron'` label row would join the inactive
    row and publish `'0x'+lower(base58)` garbage `wallets` rows (and
    those FK references would break this migration's down). Add
    `AND bn.is_active` (or `AND bn2.is_active` inside the IN-subqueries)
    to ALL of:
      * `queries/labels_staging.sql` `StgLabelOrphanNetworks` EXISTS clause
        (L~19-21) — keeps staged inactive codes ORPHAN-rejected exactly
        as today
      * `queries/labels_staging.sql` `CountLiveLabelsForStagedNetworks`
        IN-subquery (L~43-47)
      * `queries/labels_publish.sql` `PublishEnsureLabelWallets` join (L~15)
      * `queries/labels_publish.sql` `PublishUpsertWalletLabels` join (L~60)
      * `queries/labels_publish.sql` `PublishRevokeAbsentWalletLabels`
        IN-subquery (L~100)
    Then `sqlc generate` (v1.30.0 installed) — generated `internal/db/*.sql.go`
    changes are part of the diff. Semantics-preserving: every code matchable
    today is already `is_active`; inactive codes keep today's
    orphan/rejected outcome. Zero behavior change for ethereum/bsc.
  - **Update `internal/testutil/testdb_integration_test.go` L23**: the
    `count(*) == 2` assertion breaks on the new rows — change to assert the
    post-migration seed code SET `{bsc, ethereum, solana, tron}` (and note
    solana/tron `is_active=false`), preserving the "seeds survive
    truncation" intent. This is a spec change, not a weakened assertion —
    commit message says so.
  - Integration test proving: rows exist with `is_active=false`,
    `ListActiveNetworks` excludes them, `resolver.Resolve("solana"|"tron")`
    fails, `ResolveAndNormalize(resolver,"solana",<valid>)` yields
    `ErrInvalidNetwork` — plus a `tests/` case staging a
    `network_code='solana'` label row asserting it remains
    orphan-rejected (the D4b regression pin).
- Non-functional:
  - Migration applies cleanly **up and down** against dev DB
    (`make migrate` / manual down-one verification).
  - No new columns/flags; no schema redesign (AGENTS.md §15).

## Architecture

`is_active` is the registry's existing enable gate (same mechanism other
flag-driven rows use — `data_sources.is_active`, `api_clients.is_active`).
Inactive ⇒ invisible to `LoadResolver`/`loadResolver` in `cmd/api` and
`cmd/worker` (both build from `ListActiveNetworks`), so every downstream
consumer (`wallet`, `sanctions`, `screening`, `tracing`, `warmindex`,
`wattrFiles`) is unreachable for these codes by construction — at the **app
layer**. At the **SQL layer** that claim only holds after the D4b
predicates below: five `bn.code` joins in `labels_staging`/`labels_publish`
previously matched inactive codes unfiltered.

## Related Code Files

- Create: `apps/api/migrations/000026_non_evm_networks.up.sql`
- Create: `apps/api/migrations/000026_non_evm_networks.down.sql`
- Create: `apps/api/tests/non_evm_networks_test.go` (integration; uses the
  dedicated `check_wallet_test` DB via existing test harness patterns in
  `tests/`)
- Modify: `apps/api/queries/labels_staging.sql` (2 sites — `is_active` in
  EXISTS + IN-subquery)
- Modify: `apps/api/queries/labels_publish.sql` (3 sites — `is_active` in
  joins + IN-subquery)
- Modify: `apps/api/internal/db/labels_staging.sql.go`,
  `apps/api/internal/db/labels_publish.sql.go` (via `sqlc generate`)
- Modify: `apps/api/internal/testutil/testdb_integration_test.go`
  (seed-set assertion, L23)
- Delete: —

## Implementation Steps (TDD)

1. **Red**: write `tests/non_evm_networks_test.go`:
   - query `SELECT code, is_active FROM blockchain_networks WHERE code IN
     ('solana','tron')` → expect both present, `is_active=false`
   - `q.ListActiveNetworks` → assert no solana/tron rows
   - build resolver from active rows → `Resolve("solana")`, `Resolve("tron")`
     → `ok=false`
   - `wallet.ResolveAndNormalize(resolver, "solana", <valid solana addr>)`
     → `ErrorIs ErrInvalidNetwork`
2. **Green**: write up/down migrations; run `make migrate` (dev DB on
   :5433); verify `SELECT * FROM blockchain_networks` shows 4 rows with
   solana/tron inactive; run new test → green.
2b. **D4b (Red→Green)**: first add a `tests/` case staging
   `network_code='solana'` label rows → assert orphan-rejected (Red —
   today it would publish); then add the five `AND is_active` predicates
   to the labels queries, run `sqlc generate`, re-test (Green). Update
   `testdb_integration_test.go` L23 to the new seed code set
   `{bsc, ethereum, solana, tron}` with the solana/tron `is_active=false`
   note.
3. Verify **down**: `go run ./cmd/migrate` supports step-down or run
   `migrate` CLI down 1 (check `cmd/migrate/main.go` for the supported
   command shape) → rows gone → re-up → rows back. If the runner lacks a
   down path, verify down SQL manually against a scratch DB or report it.
4. Confirm `UNIQUE(namespace, chain_reference)` and `UNIQUE(code)`
   constraints honored; re-running up migration is a no-op
   (`ON CONFLICT DO NOTHING`, no target — covers both uniques; drift
   collisions still surface as errors, by choice).
5. Run `go test ./tests/ -run NonEvm` plus full `go test ./...`.

## Success Criteria

- [x] `make migrate` applies 000026; table shows solana/tron `is_active=false`
- [x] Down migration removes exactly the two rows; re-up restores them
- [x] Integration test green: resolver rejects both codes; error is
      `ErrInvalidNetwork`
- [x] All five `bn.code` sites carry `AND is_active`; `sqlc generate`
      diff committed; staged `network_code='solana'` stays orphan-rejected
      (test green)
- [x] `testdb_integration_test.go` asserts the `{bsc, ethereum, solana,
      tron}` seed set; full suite green
- [x] `GET /api/v1/networks` still returns only ethereum+bsc (asserted in
      Stage 5 curl evidence; unit-level covered by resolver test)
- [x] No change to `network.Network` consumers; all existing tests pass
- [x] Migration files embedded automatically via `migrations.go` `embed *.sql`

## Risk Assessment

- **Accidentally activating**: only via `is_active` — the insert hardcodes
  `FALSE`; no API surface flips it (verified: no UPDATE on
  `blockchain_networks` in queries/).
- **`bn.code` joins bypass the resolver**: five labels joins match by code
  with no `is_active` — fixed by D4b predicates (see Requirements); the
  `wallets.network_id` FK is not cascading, so any publish-hole would have
  broken this migration's down — the predicates are load-bearing, not
  optional.
- **UNIQUE collisions on re-run**: `ON CONFLICT DO NOTHING` (no target) —
  covers both uniques; drift under a different code errors deliberately.
- **Down-migration deleting an activated row**: guarded by
  `AND is_active = FALSE` — never reverts a future enablement; deletes
  nothing otherwise (documented choice).
- **Seeded-count assertion break**: `testdb_integration_test.go` L23 —
  updated to the new seed code set, not just a number bump.
- **CAIP-2 reference mistakes**: values fixed to the registry entries
  (`solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp`, `tron:0x2b6653dc`).
