---
phase: 2
title: "Seeder core + tests (TDD)"
status: completed
priority: P2
dependencies: [1]
---

# Phase 2: Seeder core + tests (TDD)

## Overview

Implement `internal/warmindex/seed.go` — the one-shot batch seeder that
streams all three sources in bounded keyset batches, classifies each
row, enqueues eligible ones via the existing `Queue.Enqueue`, and logs
summary counters. Tests (`seed_test.go`) are written first against the
dedicated `check_wallet_warmindex_test` DB (Red), then the
implementation (Green), then cleanup (Refactor).

## Requirements

- Functional: three scan passes (OFAC → labels → index-state), keyset
  batching, per-row classification, per-source + total counters.
- Functional: honest error taxonomy (D2 below) — bad row → `errors++`
  and continue; infra/DB failure → abort, return error.
- Functional: `WARM_INDEX_ENABLED=0` → disabled `Queue` nil-no-ops every
  enqueue; the flag gate itself lives in `cmd/worker` (D1).
- Non-functional: `ctx` honored end-to-end; `seedBatchTimeout = 30s`
  per-batch child ctx (scope: the SELECT **and** that batch's enqueue
  iteration); no unbounded reads; all per-row log lines mask addresses
  via `MaskAddr` (repo convention — addresses are never logged in full).

## Architecture

```
Seeder{q *db.Queries, queue *Queue, batchSize int, log *slog.Logger}
  NewSeeder(q, queue, batchSize, log) *Seeder      // single return;
                                                   // nil log → DiscardHandler
                                                   // (mirror NewQueue/NewDrainer)
  Run(ctx) error
    ├─ if batchSize < 1 → return error             // A7: rejection site
    ├─ seedSanctions(ctx)  → sourceStats → log line (all 6 keys)
    ├─ seedLabels(ctx)     → sourceStats → log line (all 6 keys)
    ├─ seedIndexState(ctx) → sourceStats → log line (all 6 keys)
    └─ totals line
  each source: keyset loop {
    bctx, cancel := WithTimeout(ctx, seedBatchTimeout)  // covers SELECT
    batch := query(bctx, cursor, batchSize)             // + enqueues below
    for row in batch: classify → maybe Enqueue(bctx, …)
    if bctx died during the batch → return err           // D2: check
    cancel()                                             // BEFORE cancel,
  } until len(batch) < batchSize                         // never after
```

Seed priority consts (live in `seed.go`, alongside the existing
`PrioritySanctionsMatch`/`PriorityRiskyLabel` family):

```go
PrioritySeedOFAC        = 20 // seed:ofac_list
PrioritySeedRiskyLabel  = 10 // seed:risky_label
PrioritySeedRefresh     = 5  // seed:refresh_stale
```

### D1 — Disabled-mode semantics

`warmseed` still scans when the flag is off — `Queue.Enqueue` already
nil-no-ops, counters stay truthful, and the disabled-exit-0 contract
lives in `cmd/worker` (mirroring `runWarmDrain`, which checks the flag
BEFORE building infra). `Seeder` carries no flag state.

### D2 — Error taxonomy (the seam the prompt cares about)

Per row, after `IsEVMAddress` passes, run a **canonical pre-check**:
`n, err := wallet.NormalizeEVM(addr); err != nil || n != addr` →
non-canonical row → `errors++`, continue (NormalizeEVM is only ever
invoked on syntactically-EVM strings — non-EVM input is filtered earlier
by `IsEVMAddress`, never passed through, per the binding prompt).

Once the pre-check passes, any `Enqueue` failure is by construction an
infra/DB failure (CHECK/FK violations are unreachable: the address is
canonical and `network_id` came from a live JOIN row) → return the error
and abort the run (non-zero exit). `ctx`/`bctx` death at any point →
abort, never `errors++`. This gives the prompt's "bad row never aborts /
DB failure always aborts" contract a clean discriminator with zero
error-string inspection.

### Classification order per row

S1/S2: `netOK` (JOIN non-NULL, `is_active`,
`AddressFormatForNamespace(ns) == AddressFormatEVM`) else
`skipped_inactive_net++` → `IsEVMAddress(addr)` else `skipped_non_evm++`
→ canonical pre-check else `errors++` → `Enqueue(ctx, netID, addr,
priority, reason, nil)` (`enqueued++` on nil).

S3 prepends coverage check: `status ∈ {partial,failed} ||
history_truncated` else `skipped_fresh++`, then the same net → addr →
pre-check → enqueue chain at `PrioritySeedRefresh`.

### Counter semantics (documented in-code, A8)

- `enqueued` = accepted upsert **attempts** — a deduped re-enqueue
  counts again (Enqueue returns only error; its signature is frozen by
  the tracing contract). Second-run counters legitimately show
  `enqueued=N` with zero NEW rows.
- `skipped_inactive_net` also counts `network_id IS NULL` (unmapped ≠
  inactive — the anti-cross-listing invariant requires this gap; comment
  it so nobody "fixes" it later).
- `skipped_fresh` also counts `indexing` (live claim) and `disabled`.
  Fidelity note: `indexing`/`disabled` + `history_truncated` rows ARE
  seeded at p5 per the prompt's literal OR-logic — a harmless wasted
  queue row the drain releases at pc=0.
- Every per-source log line emits ALL SIX keys
  (`scanned,enqueued,skipped_non_evm,skipped_inactive_net,skipped_fresh,
  errors`) for uniform log parsing.

## Related Code Files

- Create: `apps/api/internal/warmindex/seed.go`
- Create: `apps/api/internal/warmindex/seed_test.go`

## Implementation Steps (Red → Green → Refactor)

1. **Red**: write `seed_test.go`. **Reuse the package-level helpers
   already declared in `queue_test.go`/`drain_test.go`** — `testDSN`,
   `netByCode`, `warmRow`, `warmRowCount`, `ledgerCalls`,
   `insertWarmItem`, `addrW*` fixtures, `stubTx`, `testCfg`,
   `checkerFunc` — do NOT redeclare them (same package → compile
   blocker). Only add seed-specific fixture helpers (wallet/entity/
   label/index-state inserts). Run — must fail (no `Seeder` type yet).
2. **Green**: implement `seed.go` until all cases pass.
3. **Refactor**: dedupe batch-loop plumbing across sources if a clean
   shared shape emerges (per-source sqlc calls differ); verify
   `gofmt`/`go vet` clean.

### Fixture checklist (NOT NULL / UNIQUE — Critic A3)

- `wallets`: `network_id`, `address`, `normalized_address`
  (`address_type` optional).
- `sanctions_entities`: `source_id` (ofac_sdn), `external_entity_id`,
  `primary_name`.
- `sanctions_wallets`: `sanctions_entity_id`, `listed_address`,
  `normalized_address`, `identifier_type`; keep the
  `uq_sanctions_wallet_identifier` tuple `(entity, normalized_address,
  identifier_type, currency_code, source_identifier_id)` distinct.
- `wallet_labels`: `wallet_id`, `category_id` (seeded codes),
  `source_id` (use a seeded row e.g. `wallet_attribution`), `label`,
  **`fingerprint` UNIQUE** — unique per fixture.
- `wallet_index_state`: `(network_id, wallet_id, source_id)` PK — vary
  `source_id` to create multi-row-per-wallet fixtures.
- `skipped_non_evm` requires pairing a **non-EVM address with an ACTIVE
  EVM network** (net-gate-first order means a Base58 addr on inactive
  TRON counts `skipped_inactive_net` — assert that too).
- No `sanctions_snapshots` fixture needed (tracing-only dependency —
  avoid cargo-cult weight).

### Required cases (from source prompt + debate)

- eligible OFAC row → p20 row, reason `seed:ofac_list`
- risky-label row → p10, `seed:risky_label`; EXCHANGE/GAMBLING never
  scanned (SQL-filtered) — assert no queue rows
- stale (`partial`/`failed`) or `history_truncated` → p5,
  `seed:refresh_stale`; `fresh`/`not_indexed`/`indexing`/`disabled`
  without truncation → `skipped_fresh`, no queue rows
- `revoked_at` sanctions row → never scanned
- non-EVM `normalized_address` on active EVM net → `skipped_non_evm`,
  no queue row; `NormalizeEVM` never reached on it
- `network_id` NULL or inactive TRON net → `skipped_inactive_net`
- idempotent replay: run twice → zero duplicate rows `WHERE status IN
  ('pending','claimed')` (done/dead legitimately re-enqueue — assert
  ACTIVE uniqueness, not total count)
- failing row (uppercase EVM `normalized_address` — passes
  `IsEVMAddress`, fails canonical pre-check) → `errors++`, batch
  continues, other rows still enqueue
- `WARM_INDEX_ENABLED=0` (Queue disabled) → zero warm_queue writes
- invariant: `PrioritySeed{OFAC,RiskyLabel,Refresh} < PriorityRiskyLabel`
- multi-source `wallet_index_state` rows share `(net,wallet)` — keyset
  cursor advances correctly across the composite PK

## Success Criteria

- [x] All cases above pass against `check_wallet_warmindex_test`
- [x] `go test ./internal/warmindex/` exit 0, zero regressions
- [x] Seed priority invariant test enforces `< 50`
- [x] `gofmt -l .` empty, `go vet ./...` clean

## Risk Assessment

- Composite-key keyset cursor carried as three params — tested with
  multi-source fixtures.
- `enqueued` counting attempts (not new rows) is prompt-honest but must
  be commented — A8.
- Per-row `Enqueue` is one round-trip each — acceptable for a one-shot
  batch at MVP scale.
