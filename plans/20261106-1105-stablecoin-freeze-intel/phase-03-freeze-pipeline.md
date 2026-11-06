---
phase: 03
title: internal/ingestion/pipeline_freeze.go — stage → validate → delta publish
status: pending
priority: high
dependencies: [01, 02]
---

## Overview

`freezePipeline implements Pipeline` registered under
`"stablecoin_freeze"`. Consumes the events JSONL artifact: stage rows,
validate reconciliation, publish in the service-managed tx with **delta
semantics** — freeze events upsert FROZEN labels; unfreeze events revoke
matching labels (destroy events never revoke — evidence append only);
per-contract cursor advances — all in one tx.

## Requirements

- Import: parse header + event lines into `stg_freeze_events`; malformed
  lines → `Rejected` count; header window carried in `freezeRunMeta`
  (from/to/head per contract).
- Validate: header contracts ⊆ registry; staged == parsed reconciliation;
  no staged event with `block_number > head`; `toBlock ≥ fromBlock`
  **unless header `empty:true`** (degenerate window: header-only file,
  publish advances nothing, run COMPLETEDs).
- Publish (single tx, service-managed):
  - `wallets` ensure for staged addresses (EVM normalized, lowercase —
    reuse the pattern `PublishEnsureLabelWallets` established).
  - Freeze events → `wallet_labels` upsert: category `FROZEN`, label
    `usdt_blacklist`/`usdc_blacklist`, source `stablecoin_freeze`,
    `verification_status = 'verified'` (on-chain event IS the primary
    evidence — reason stays `NOT_AVAILABLE`, entity **null** — do NOT
    invent a Tether/Circle entity),
    `confidence='high'`, `observed_at = block_ts`, `evidence =
    {contract, event, tx_hash, block_number, block_ts, topic}` JSONB,
    `source_url = https://etherscan.io/tx/<tx_hash>`,
    `upstream_category = 'FROZEN'`, `fingerprint =
    stablefreeze.Fingerprint(net, addr, "FROZEN", label)` — a
    source-namespaced twin of walletattribution's formula pinning
    `"stablecoin_freeze|"` (reusing the wattr function embeds the wrong
    namespace and re-opens absence-as-revoke via wattr publishes).
  - **`unfreeze` events only** → `revoked_at = now` on matching active
    labels + merge `{unfreeze_event, tx_hash, block_number, block_ts}`
    into the label's evidence JSONB (auditor needs the revoking tx).
  - **`destroy` events NEVER revoke** — `destroyBlackFunds` keeps
    `isBlackListed` set; append `{event:'DestroyedBlackFunds', tx_hash,
    block}` to the active label's evidence as terminal lifecycle marker.
    A `destroy` with **no matching active label** (missed freeze —
    `destroyBlackFunds` requires `isBlackListed`, so the event itself
    proves the address was blacklisted at that block) upserts a FROZEN
    label with destroy-only evidence.
  - `freeze_scan_state.last_scanned_block = toBlock` per contract via
    `GREATEST` — same tx.
  - `data_source_snapshots` row, version `freeze-<head>-<unix>-<runid8>`
    (UNIQUE(source_id,version) collision-proof).
  - **Never** call `PublishRevokeAbsentWalletLabels` — absence is not a
    signal in this source.
- **Cache invalidation (BLOCKER fix)**: `ListLabels` cache keys embed the
  version from `LatestLabelSnapshotPublished`, which is hard-scoped to
  `code='wallet_attribution'` (`queries/labels_read.sql`). Broaden that
  query to the latest publish across **all** label-producing sources
  (`wallet_attribution` + `stablecoin_freeze`) so each freeze publish
  bumps `/wallets/labels` and `/screenings` cache keys. One semantic for
  both consumers: `lastSynced`/`data_status` freshness now means
  "latest publish across label-producing sources" — documented in the
  service comment and guidebook (do NOT split into two queries; a single
  version+published_at row serves both the cache key and freshness
  field consistently).
- Write `ingestion_runs.cursor_value` = `contract:last_scanned` summary —
  free observability, column already exists.
- `StagingNames()`: `["freeze_events"]` + purge/delete wiring.
- `LatestVersion` via `data_source_snapshots` like labels pipeline.

## Related Code Files

**Create**
- `internal/ingestion/pipeline_freeze.go`
- `internal/ingestion/pipeline_freeze_test.go`

**Modify**
- `internal/ingestion/service.go` — register pipeline in `NewService`
- `internal/ingestion/pipeline.go` — staging purge/delete entries
  (phase 01 overlap — whichever lands first owns it)

## Implementation Steps (TDD)

1. Red: `pipeline_freeze_test.go` on `_test` DB — freeze event → label
   row fields; unfreeze → revoked_at + evidence merge; **destroy → label
   STAYS active** + evidence appended; absent address NOT revoked (the
   core invariant test); cursor advanced in tx; second run same window →
   zero net change (idempotent); degenerate empty window → COMPLETED, no
   advance; fingerprint namespace test (wattr fingerprint ≠ freeze
   fingerprint for same address — no cross-source collision).
2. Green: Import/Validate/Publish + queries.
3. Refactor: shared fingerprint/ensure-wallets helpers.

## Success Criteria

- [ ] All tests green on dedicated `_test` DB
- [ ] Absence-is-not-revoke test explicitly proves a label survives a
      later run that doesn't contain it
- [ ] Destroy keeps label active — verified against TetherToken semantics
- [ ] Publish tx atomicity: kill mid-publish → cursor unadvanced
- [ ] Cache-version query returns freeze publish → cache key bumps
- [ ] `go build ./...` + `go vet` clean

## Risk Assessment

- Fingerprint formula drift → mirror walletattribution's formula in a
  `stablefreeze.Fingerprint` twin pinning the `"stablecoin_freeze|"`
  namespace; never reuse the wattr function (wrong namespace) and never
  reimplement the algorithm differently.
- A label frozen→unfrozen→refrozen must preserve history: upsert revives
  (revoked_at=NULL on matching fingerprint re-freeze) — test it.
- `ImportResult.Labels` is reused as the staged-event counter (semantic
  stretch — the run row's records_fetched counts events, not labels).
- `data_sources.is_active` is NOT honored by `GetSourceByCode` —
  disabling the row won't stop runs; the killswitch is
  `provider_configs.status` on the RPC provider. Documented caveat.
