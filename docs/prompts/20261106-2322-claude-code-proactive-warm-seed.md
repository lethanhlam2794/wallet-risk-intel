# Prompt: claude-code — proactive-warm-seed

> Created: 20261106-2322 | Target: claude-code | Template: M (agentic)

## Prompt

```text
## Context (carry forward)
- Repo: check-wallet — Go + PostgreSQL modular monolith; module root is
  `apps/api/` (all go commands run there). Read `AGENTS.md` at repo root
  first — it is binding.
- The warm-index subsystem EXISTS but is purely reactive:
  - Producer: `internal/tracing/warm.go` enqueues signal-bearing hop-1
    counterparties during interactive BFS only (priorities 100 =
    sanctions_direct_match, 50 = risky label in {SCAM, PHISHING, HACK,
    RANSOMWARE, MIXER}).
  - Queue: `internal/warmindex/queue.go` Enqueue = upsert, dedupes ACTIVE
    rows, never demotes; validates canonical EVM via `wallet.NormalizeEVM`.
  - Consumer: `internal/warmindex/drain.go` (`go run ./cmd/worker -mode
    warmdrain`) claims `priority DESC, id`, runs `EnsureIndexed` under
    provider lanes + `warm_quota_ledger` daily cap. Drain is CORRECT —
    do not touch it.
  - `WARM_INDEX_ENABLED` defaults false → Enqueue is a nil no-op; drain
    exits 0 immediately (contract at `cmd/worker/main.go:209`).
- TRON network row exists with `is_active=FALSE` and MUST stay that way.
  OFAC data contains non-EVM (TRON Base58) addresses — they must be
  filtered out BEFORE reaching `NormalizeEVM`, never passed through.

## Task
Add a new one-shot worker mode `warmseed` that proactively seeds
`warm_queue` from DB-resident signal sources, so the risk graph around
known-bad actors is warmed BEFORE any user query.

Seed sources (3 SELECTs — add to a NEW file `queries/warm_seed.sql`, then
regenerate sqlc):
1. `sanctions_wallets` WHERE `revoked_at IS NULL`, joined to
   `blockchain_networks` — only rows whose network `is_active=TRUE` AND
   is EVM → priority 20, reason `seed:ofac_list`.
2. `wallet_labels` WHERE category IN
   ('SCAM','PHISHING','HACK','RANSOMWARE','MIXER'), joined to active EVM
   networks → priority 10, reason `seed:risky_label`.
3. Refresh: `wallet_index_state` rows WHERE `status IN
   ('partial','failed')` OR `history_truncated = TRUE` → priority 5,
   reason `seed:refresh_stale`. (`fresh`/`not_indexed` rows are skipped —
   fresh coverage must not burn drain quota.)

Seed priorities MUST stay below the signal floor of 50 — interactive
investigations always win the claim order. This is a hard invariant.

## Behavior contract
- One-shot batch: scan all 3 sources → enqueue each eligible row via the
  existing `Queue.Enqueue` → log summary counters → exit 0. Ops schedules
  it via cron; do NOT add a loop/daemon.
- `WARM_INDEX_ENABLED=0` → log "warm index disabled" + exit 0, zero work.
- Idempotent replay: a crash mid-scan is safe to re-run; the upsert dedup
  is the ONLY dedup mechanism — do NOT add run-tracking state.
- Per-row fail-open: a bad row logs + increments `errors`, never aborts
  the scan. DB-level failure (connection lost) → abort with non-zero exit.
- Stream rows in bounded batches (keyset or LIMIT/OFFSET on a stable
  ORDER BY) — never `SELECT *` the whole table into memory. Batch size
  configurable via `WARM_SEED_BATCH_SIZE` (default 500) in
  `internal/config/config.go`.
- End-of-run log line: `scanned`, `enqueued`, `skipped_non_evm`,
  `skipped_inactive_net`, `skipped_fresh`, `errors` per source.
- Honor `ctx` end-to-end; per-batch queries get bounded timeouts.

## File scope
- NEW `internal/warmindex/seed.go` — seeder implementation.
- NEW `internal/warmindex/seed_test.go` — tests below.
- NEW `queries/warm_seed.sql` + `sqlc generate` output only.
- EDIT `cmd/worker/main.go` — register `-mode warmseed`.
- EDIT `internal/config/config.go` — `WarmSeedBatchSize` knob only.

Read these BEFORE writing any code: `AGENTS.md`, `cmd/worker/main.go`,
`internal/warmindex/queue.go`, `internal/warmindex/drain.go`,
`queries/warm_index.sql`, `migrations/000023_warm_index_queue.up.sql`,
`migrations/000008_transactions.up.sql`, `internal/tracing/warm.go`,
`internal/config/config.go` warm section, the `sanctions_wallets` and
`wallet_labels` migration files.

## Forbidden actions
- NO new migration files, NO schema changes — existing tables suffice.
- NO edits to `drain.go`, `queries/warm_index.sql`, `tracing/warm.go`, or
  the 50/100 signal priorities.
- NO imports of `internal/provider/trongrid`, NO TRON activation.
- NO new API endpoints, NO `api/openapi.yaml` changes.
- NO commits, NO `.env` changes, NO new external dependencies.
- Do NOT weaken `NormalizeEVM` — filter ineligible rows before Enqueue.
- Only make changes directly requested. Do not add features or refactor
  beyond what was asked.

## Tests required (all against a dedicated *_test DB — AGENTS.md §13)
Table-driven cases: eligible OFAC row enqueues at p20; risky-label row at
p10; stale/truncated index state at p5; revoked OFAC row skipped; non-EVM
address skipped+counted; `network_id` on inactive network skipped;
EXCHANGE/GAMBLING labels skipped; `fresh`/`not_indexed` state skipped;
re-run produces zero duplicate active rows; one failing row does not stop
the batch; `WARM_INDEX_ENABLED=0` produces zero enqueues and exit 0.

## Stop conditions — STOP and ask before:
- Touching `warm_index.sql`/`drain.go`/`warm.go` or signal priorities.
- Any schema/migration change, any flag default change, TRON activation.
- If a seed source requires a JOIN that contradicts the schema — report
  the conflict, do not invent columns.

## Done when
`go build ./...`, `go vet ./...`, `gofmt -l .` (empty), new tests green,
zero drift in existing tests. Report: files changed, counter names, and
the exact enqueue-path call graph you used.
```

## Strategy

Agentic contract: grounded context (real claim order/upsert/EVM-gate facts) front-loaded, seed priorities pinned below the 50-point signal floor as an invariant, refresh-staleness gate prevents quota burn on `fresh` coverage, forbidden list locks TRON inactive and freezes the correct drain path.

## Notes

Paste into Claude Code at the repo root (`check-wallet/`). Requires `make deps-up` stack running for the `*_test` DB integration tests; sqlc must be installed for `sqlc generate`.
