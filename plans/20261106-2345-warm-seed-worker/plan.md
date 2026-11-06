---
title: "Warm Seed Worker — proactive warm_queue seeding from DB-resident signals"
status: completed
priority: P2
source_prompt: docs/prompts/20261106-2322-claude-code-proactive-warm-seed.md
blockedBy: []
blocks: []
---

# Warm Seed Worker (`-mode warmseed`)

## Overview

The warm-index subsystem (`warm-index-queue v1`, completed plan
`20261105-2205-warm-index-queue`) is purely reactive: only hop-1
counterparties that produced a signal during an *interactive* BFS are
enqueued (priorities 100 / 50). This plan adds a one-shot worker mode
`warmseed` that proactively seeds `warm_queue` from three DB-resident
signal sources so the risk graph around known-bad actors is warmed
**before** any user query. Ops schedules it via cron; no daemon loop.

Seed priorities are pinned strictly below the interactive signal floor
of 50 (`PriorityRiskyLabel`) — interactive investigations always win the
`priority DESC, id` claim order. Hard invariant, guarded by a test.

## Grounded Context (verified against code, 2026-11-06)

- Producer: `internal/tracing/warm.go` enqueues signal-bearing hop-1
  counterparties only (p100 `sanctions_direct_match`, p50 risky label).
- Queue: `internal/warmindex/queue.go` — `Enqueue(ctx, netID, addr,
  priority, reason, screeningID)` upserts; dedupes ACTIVE rows
  (pending/claimed partial unique index); GREATEST priority wins, never
  demotes; rejects anything where `NormalizeEVM(addr) != addr`.
- Drain: `internal/warmindex/drain.go` claims `priority DESC, id` under
  provider lanes + `warm_quota_ledger` daily cap. Correct — untouched.
- Flag: `WARM_INDEX_ENABLED` defaults false → `Queue.Enqueue` nil no-op;
  warmdrain exits 0 (`cmd/worker/main.go:217-220`). warmseed mirrors it.
- `warm_queue.address` has `CHECK (address ~ '^0x[0-9a-f]{40}$')` —
  non-canonical rows cannot even be inserted.
- `blockchain_networks`: `namespace` (`eip155` = EVM), `is_active`.
  TRON + Solana rows exist with `is_active=FALSE` and MUST stay that way.
  `network.AddressFormatForNamespace(ns)` maps namespace → family.
- `sanctions_wallets`: `normalized_address` (may hold non-EVM TRON
  Base58), `network_id` NULLABLE (set only when evidenced), `revoked_at`.
- `wallet_labels`: `wallet_id`/`category_id`/`source_id` FKs, `label`,
  `revoked_at`; category resolves via `risk_categories.code`.
- `wallet_index_state`: PK `(network_id, wallet_id, source_id)` —
  per-source rows; `status` ∈ not_indexed/indexing/fresh/partial/failed/
  disabled; `history_truncated` flag; joins `wallets` for the address.
- `wallets`: `network_id` + `normalized_address` + `address_type`.
- Test harness: `testutil.RequireTestPoolDSN` enforces `*_test` DBs,
  migrates, truncates domain tables (keeps seeded networks/categories/
  sources). warmindex tests use `check_wallet_warmindex_test`.

## Seed Sources & Classification

| # | Source | Eligibility | Priority | Reason |
|---|--------|-------------|----------|--------|
| 1 | `sanctions_wallets` | `revoked_at IS NULL`; active EVM network | 20 | `seed:ofac_list` |
| 2 | `wallet_labels` | `revoked_at IS NULL`; `risk_categories.code` ∈ {SCAM,PHISHING,HACK,RANSOMWARE,MIXER}; active EVM net | 10 | `seed:risky_label` |
| 3 | `wallet_index_state` | `status ∈ {partial,failed}` OR `history_truncated`; active EVM net | 5 | `seed:refresh_stale` |

Per-row classification order (every source): network eligibility first
(NULL/inactive/non-`eip155` namespace → `skipped_inactive_net`), then
address shape (`!wallet.IsEVMAddress` → `skipped_non_evm`), then a
canonical pre-check (`NormalizeEVM(addr) == addr` → else `errors++`),
then enqueue. For source 3 the coverage check runs first (non-stale →
`skipped_fresh`). `NormalizeEVM` is invoked only on syntactically-EVM
input — non-EVM strings are filtered earlier and never reach it.

Counters logged per source at end of run: `scanned`, `enqueued`,
`skipped_non_evm`, `skipped_inactive_net`, `skipped_fresh`, `errors`.

## Phase Roadmap

| Phase | Title | Files | Depends |
|-------|-------|-------|---------|
| 1 | Seed queries + batch-size config | `queries/warm_seed.sql`, `internal/db/warm_seed.sql.go` (gen), `internal/config/config.go` | — |
| 2 | Seeder core + tests (TDD) | `internal/warmindex/seed.go`, `internal/warmindex/seed_test.go` | 1 |
| 3 | Worker mode wiring + gates | `cmd/worker/main.go` | 2 |

## Behavior Contract

- One-shot batch: scan 3 sources → `Queue.Enqueue` per eligible row →
  summary counters → exit 0. No loop/daemon.
- `WARM_INDEX_ENABLED=0` → log "warm index disabled" + exit 0, zero work.
- Idempotent replay: upsert dedup is the ONLY dedup; no run-tracking.
- Per-row fail-open: bad row → log + `errors++`, never aborts scan.
  DB-level failure (query/connection error) → abort, non-zero exit.
- Bounded batches: keyset pagination on stable ORDER BY; batch size
  `WARM_SEED_BATCH_SIZE` (default 500), `<1` → `Seeder.Run` error →
  non-zero exit.
- Honor `ctx` end-to-end; each batch query runs under a bounded
  `seedBatchTimeout` (30 s) child context.

## Risk Matrix

| Risk | Mitigation |
|------|------------|
| TRON/non-EVM rows reach `NormalizeEVM` | Address-shape gate (`IsEVMAddress`) + network-namespace gate BEFORE Enqueue; TRON rows also double-blocked by `is_active=FALSE` |
| Seed floods queue ahead of interactive signals | Priorities 20/10/5 < 50 floor; test asserts `< PriorityRiskyLabel` |
| `wallet_index_state` has N rows per wallet (per source_id) | Dedup via upsert; counters count attempted enqueues honestly |
| `SELECT *` memory blowup on large tables | Keyset/LIMIT batches of `WarmSeedBatchSize`; per-batch 30 s timeout |
| NULL `network_id` on sanctions rows silently dropped | LEFT JOIN keeps rows visible → counted `skipped_inactive_net` |
| Revoked/terminal rows re-seeded | `revoked_at IS NULL` predicates; queue terminal rows never block re-enqueue |
| Crash mid-scan | Re-run is safe: upsert-only, no run state |
| Batch size 0 → silent no-op | `envInt` parse + seeder rejects `<1` |

## Forbidden Actions (from source prompt — binding)

- NO new migrations / schema changes; NO edits to `drain.go`,
  `queries/warm_index.sql`, `tracing/warm.go`, or 50/100 priorities.
- NO `internal/provider/trongrid` import; NO TRON activation.
- NO new API endpoints / `api/openapi.yaml` changes; NO commits; NO
  `.env` changes; NO new external dependencies.
- Do NOT weaken `NormalizeEVM` — filter ineligible rows before Enqueue.

## Stage 1 Debate Summary

- **Devin Critic session**: `observant-talk` (round 1 output:
  `reports/critic-round-1-output.txt`, `reports/critic-round-1b-output.txt`).
- **Verdict round 1**: consensus withheld — no phase REJECTED, all
  grounding claims verified; 8 required + 2 recommended amendments.
- **Concessions adopted (all A1–A10)**:
  - A1: phase-3 wiring sketch fixed (`config.Load`/`Connect` error
    handling, defers).
  - A2: `seed_test.go` must REUSE `queue_test.go`/`drain_test.go`
    package helpers (redeclaration = compile blocker).
  - A3: fixture checklist pins `fingerprint` UNIQUE, `identifier_type`,
    `listed_address`, distinct `uq_sanctions_wallet_identifier` tuples,
    seeded `wallet_attribution` source.
  - A4: error taxonomy redesigned — **canonical pre-check**
    (`NormalizeEVM(addr) == addr`, reached only after `IsEVMAddress`
    passes) makes pre-check failures the only `errors++` domain path;
    post-check `Enqueue` failures are infra → abort. Zero error-string
    inspection. Better than the critic's SQLSTATE suggestion.
  - A5: 30 s batch ctx covers SELECT + that batch's enqueue iteration;
    ctx death → abort.
  - A6: `MaskAddr` on all per-row logs.
  - A7: `batchSize < 1` rejected by `Seeder.Run` (→ non-zero exit).
  - A8: counter semantics documented in-code (enqueued = accepted
    attempts; NULL net → `skipped_inactive_net`; `indexing`/`disabled` →
    `skipped_fresh`; all six keys per source line).
  - A9: `sqlc.narg`/`sqlc.arg` convention + `make tools` version pin +
    scoped `internal/db` diff check.
  - A10: unbounded-run rationale documented; `WARM_INDEX_ENABLED=1`
    added to smoke env.
- **Clarifications resolved**: `skipped_non_evm` fixture must pair a
  non-EVM address with an ACTIVE EVM net (net-gate runs first);
  re-run idempotency asserted on `status IN ('pending','claimed')`
  uniqueness; NULL-`network_id` EVM rows are an intentional gap under
  the anti-cross-listing invariant.

## Done When

`go build ./...`, `go vet ./...`, `gofmt -l .` (empty), new seed tests
green against `check_wallet_warmindex_test`, zero drift in existing
tests. Report: files changed, counter names, exact enqueue-path call
graph.
