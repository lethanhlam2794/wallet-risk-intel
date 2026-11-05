# Warm Index Queue — Operational Guidebook & Feature Map

**Feature**: `warm-index-queue v1` — signal-prioritized *background* deep-indexing of
wallets already proven to carry meaningful risk signals, spending otherwise-expiring
free provider quota **without starving interactive screening/indexing requests**.

- Plan: `plans/20261105-2205-warm-index-queue/plan.md` (+ `phase-01..04-*.md`)
- Pipeline evidence: `apps/api/docs/pipeline-log.md` (Stages 1–5 logged)
- Screenshots: **N/A — backend/CLI-only feature, zero UI surface** (Stage 5 gate: `SKIPPED-NO-UI`). All evidence is live terminal output and executable SQL.

---

## 1. What It Does (60-second mental model)

```
screening/tracing (interactive, unchanged)
        │  hop-1 neighbor produces a signal
        │  sanctions_direct  → priority 100   reason "sanctions_direct_match"
        │  label SCAM|PHISHING|HACK|RANSOMWARE|MIXER → priority 50  reason "label:<CAT>[@<net>]"
        │  (GAMBLING / EXCHANGE / listing-only / sentinel → never enqueued)
        ▼  fail-open, detached 2s ctx, masked-addr logs only
   warm_queue  (Postgres, dedupe: one ACTIVE row per (network_id,address))
        ▲
        │  cmd/worker -mode warmdrain   (WARM_INDEX_ENABLED=true)
        │  every WARM_INDEX_POLL_INTERVAL (default 60s):
        │    sweep expired claims (>15m lease → back to pending, attempts+1)
        │    claim ONE item: priority DESC, id ASC, FOR UPDATE SKIP LOCKED
        │    pick first lane that is: Enabled(net) ∧ killswitch-active ∧ under daily cap
        │    EnsureIndexed(Refresh:true, per-item Budget, MaxPages)
        │    settle: done | fail(attempts+1, dead@3) | release(no bump, pc=0)
        │    ledger += ProviderCalls  (warm_quota_ledger, per provider per UTC day)
```

Design contract highlights (source: `docs/prompts/20261105-2155-codex-warm-index-queue.md`,
decisions D1–D10 in the plan):

- **One item per cycle** — the drain is deliberately slow; interactive traffic always wins.
- **No provider outside `WARM_INDEX_PROVIDERS`** is ever consumed; each provider gets a
  dedicated single-member `transaction.Service` lane so quota is attributed exactly.
- **`Refresh: true` on every drain** — queue inputs are by construction fresh `partial`
  rows; without refresh the freshness gate would serve them and nothing would deepen.
  (The `FAILED` cooldown still applies — a cool-down item releases without traffic.)
- **`ProviderCalls == 0` ⇒ never an attempt bump, never ledger spend.** Served-fresh,
  cooldown-skipped, disabled, budget-exhausted and live-`indexing` outcomes all release
  the claim untouched.
- **`attempts` reaches 3 ⇒ `dead`.** Lease-expired claims count as strikes (a crash
  mid-drain is an unverifiable attempt); graceful releases never bump.
- **Enqueue never fails screening.** Errors are logged (masked address) and dropped.

## 2. Configuration

| Env var | Default | Semantics |
|---|---|---|
| `WARM_INDEX_ENABLED` | `false` | Master flag. Off ⇒ producer no-ops, drain mode logs and exits 0 (safe to run always-on). |
| `WARM_INDEX_PROVIDERS` | `blockscout,blockscout_pro` | Comma-separated allowlist **in selection order**. Unknown code ⇒ worker exits 1 at boot. |
| `WARM_INDEX_DAILY_CAP` | `200` | Per-provider upstream-call ceiling per UTC day. `≤0` ⇒ all lanes ineligible (fail-safe, drains nothing). |
| `WARM_INDEX_POLL_INTERVAL` | `60s` | Cycle cadence. `≤0` ⇒ 60s fallback. |
| `WARM_INDEX_ITEM_BUDGET` | `8` | Provider-call pool per item (clamped further by the lane's `TXINDEX_BUDGET_*`). `≤0` ⇒ unlimited within MaxPages/lane-budget. |
| `WARM_INDEX_MAX_PAGES` | `10` | Stream-pair pagination steps per drain (vs interactive `MaxPagesHopN=2`). |

Killswitch is identical to the interactive path: `provider_configs.status` AND
`data_sources.is_active`, TTL-cached (~30s) via `providerconfig.StatusCache`.

## 3. Step-by-Step Runbook

### 3.1 Migrate

```bash
cd apps/api
make deps-up                       # postgres + redis dev stack
DATABASE_URL='postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable' \
  go run ./cmd/migrate             # applies 000023 (idempotent)
```

### 3.2 Enable + run the drain loop

```bash
cd apps/api
go build -o /tmp/warmworker ./cmd/worker        # use a REAL binary for signal tests —
                                                # `go run` swallows the child's signals
DATABASE_URL='postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable' \
WARM_INDEX_ENABLED=true \
  /tmp/warmworker -mode warmdrain
```

Verified startup output (captured 2026-11-05, real binary + live Postgres):

```text
{"level":"INFO","msg":"provider config resolved","provider":"blockscout",...,"status":"active"}
{"level":"INFO","msg":"provider config resolved","provider":"blockscout_pro",...,"status":"active"}
{"level":"INFO","msg":"warm drain loop started","poll_interval":60000000000,"lanes":2,"daily_cap":200}
```

- `SIGTERM`/`SIGINT` ⇒ the poll loop exits between cycles; an in-flight drain releases
  its claim under a detached 5s context (item returns to `pending`, **no attempt bump**).
- Disabled flag ⇒ `warm index disabled (WARM_INDEX_ENABLED); exiting`, exit 0.
- Unknown provider ⇒ `worker: warm index: provider "bogus_provider" has no resolved config`, exit 1.
- `cmd/worker -mode ingest` (the original one-shot mode) is unchanged.

### 3.3 Producer wiring (already done in `cmd/api`)

`cmd/api/main.go` always calls `tracingSvc.SetEnqueuer(warmindex.NewQueue(queries,
cfg.WarmIndexEnabled, log), log)` — the Queue no-ops when disabled, so the interactive
path is byte-identical with the feature dark.

## 4. Database Specification

### `warm_queue` (migration `000023`)

| Column | Type | Notes |
|---|---|---|
| `id` | `bigserial` PK | |
| `network_id` | `bigint` FK → `blockchain_networks(id)` | |
| `address` | `text` | CHECK `^0x[0-9a-f]{40}$` — canonical normalized EVM only |
| `priority` | `int` default 0 | 100 sanctions-direct, 50 risky label |
| `reason` | `text` | `sanctions_direct_match`, `label:<CAT>[@<net>]` |
| `source_screening_id` | `uuid` NULL | provenance |
| `status` | `text` | CHECK ∈ `pending`,`claimed`,`done`,`dead` |
| `attempts` | `int` default 0 | `dead` at 3 |
| `claimed_at` | `timestamptz` NULL | lease starts; >15 min ⇒ sweep-eligible |
| `created_at`/`updated_at` | `timestamptz` default `now()` | |

Indexes (verified `\d+` output):

- `uq_warm_queue_active` UNIQUE `(network_id, address) WHERE status IN ('pending','claimed')`
  — dedupes only the *active* set; `done`/`dead` rows are history and never block re-enqueue.
- `idx_warm_queue_pending_order` `(priority DESC, id) WHERE status='pending'` — claim order.

### `warm_quota_ledger` (migration `000023`)

PK `(provider_code, period_start)`; `period_start` = current UTC date; `calls` int.
`AddWarmQuotaCalls` upserts `calls += EXCLUDED.calls`.

## 5. Executable Verification Queries (verified outputs)

Run against the dedicated test DB `check_wallet_warmindex_test`
(dev DB works identically — same schema). Verified 2026-11-05:

```sql
-- Enqueue (verbatim EnqueueWarmItem); second call at higher priority
-- dedupes to the SAME row: id 1, priority 50→100, reason swapped.
 id | network_id |                  address                   | priority |         reason         | status  | attempts
----+------------+--------------------------------------------+----------+------------------------+---------+----------
  1 |          1 | 0x0000000000000000000000000000000000000a01 |      100 | sanctions_direct_match | pending |        0

-- ClaimNextWarmItem → status flips to claimed (SKIP LOCKED = concurrent-drainer safe)
 id | status  | attempts
----+---------+----------
  1 | claimed |        0

-- CompleteWarmItem (claim-gated: AND status='claimed') → done
 id | priority |         reason         | status | attempts
----+----------+------------------------+--------+----------
  1 |      100 | sanctions_direct_match | done   |        0

-- warm_quota_ledger after a 2-call drain
 provider_code | period_start | calls
---------------+--------------+-------
 blockscout    | 2026-11-05   |     2
```

Operational queries for day-to-day checks:

```sql
-- Queue health snapshot
SELECT status, count(*) FROM warm_queue GROUP BY status ORDER BY status;

-- Stuck-claim audit (lease = 15 min; sweep also bumps attempts)
SELECT id, address, claimed_at FROM warm_queue
WHERE status='claimed' AND claimed_at < NOW() - interval '15 minutes';

-- Today's quota spend vs cap (cap = WARM_INDEX_DAILY_CAP, default 200)
SELECT provider_code, calls FROM warm_quota_ledger
WHERE period_start = CURRENT_DATE;

-- Dead-letter audit (what the poison pills were)
SELECT address, reason, attempts, updated_at FROM warm_queue
WHERE status='dead' ORDER BY updated_at DESC;

-- Priority histogram of pending work
SELECT priority, reason, count(*) FROM warm_queue
WHERE status='pending' GROUP BY 1,2 ORDER BY 1 DESC;
```

## 6. Failure Semantics Cheat-Sheet

| Situation | Result |
|---|---|
| Two enqueues, same `(net,addr)` | One row; higher priority wins, reason follows the winner |
| Enqueue fails (DB down) | Logged masked-addr, screening unaffected (fail-open) |
| No lane eligible (disabled/killswitch/at-cap) | Item → `pending`, zero upstream calls, no attempt bump |
| `EnsureIndexed` infra error | `attempts+1` → `dead` at 3; ledger untouched (pc not attributable) |
| Fetch outcome `failed` with `pc>0` | `attempts+1` → `dead` at 3; `ledger += pc` |
| Outcome `fresh`/`partial` with `pc>0` | `done`; `ledger += pc` |
| `pc=0` outcomes (served/cooldown/disabled/budget/live-indexing) | release → `pending`, no bump |
| SIGTERM mid-drain | detached-ctx release → `pending`, no bump |
| Crash mid-drain (claim orphaned) | sweep after 15 min → `pending`, `attempts+1` (poison-pill dies at 3) |
| `selectLane` DB read fails after claim | best-effort detached release → `pending`, no bump |
| `network_id` unresolvable | `dead` immediately (permanently unprocessable) |

## 7. Technical Feature Map

### Source paths

| Concern | Path |
|---|---|
| Schema (both tables) | `apps/api/migrations/000023_warm_index_queue.{up,down}.sql` |
| sqlc statements | `apps/api/queries/warm_index.sql` → `internal/db/warm_index.sql.go` |
| Producer (Enqueue) | `apps/api/internal/warmindex/queue.go` (`Queue`, `NewQueue`, `Enqueue`, `MaskAddr`) |
| Consumer (drain loop) | `apps/api/internal/warmindex/drain.go` (`Drainer`, `Run`, `RunOnce`, `settle`, `selectLane`) |
| Constants/types | `apps/api/internal/warmindex/warmindex.go` (`Config`, `ProviderLane`, priorities, `LeaseDuration=15m`, `MaxAttempts=3`, `DrainTimeout=5m`) |
| Tracing hook | `apps/api/internal/tracing/warm.go` (`Enqueuer` iface, `SetEnqueuer`, `warmEnqueuePlan`, `warmEnqueue`); call site `internal/tracing/service.go` hop-1 loop (sentinel-guarded) |
| Worker mode | `apps/api/cmd/worker/main.go` (`-mode warmdrain`, `runWarmDrain`) |
| Lane construction | `apps/api/cmd/worker/warmproviders.go` (`buildWarmLanes` — mirrors `buildTransactions`, single-member chains) |
| API wiring | `apps/api/cmd/api/main.go` (`SetEnqueuer` after `buildTransactions`) |
| Config | `apps/api/internal/config/config.go` (`WarmIndex*` fields, `envBool`) + `.env.example` |

### Test map (all on dedicated `check_wallet_warmindex_test` DB)

| Contract item | Test |
|---|---|
| Dedupe + monotone priority + reason takeover | `TestQueueEnqueue_DedupeMonotone` |
| done/dead history doesn't block re-enqueue | `TestQueueEnqueue_ReenqueueAfterTerminal` |
| screening_id passthrough / normalized-addr gate / flag-off no-op | `TestQueueEnqueue_{ScreeningID,InvalidAddress,DisabledNoop}` |
| Claim ordering (priority DESC, id ASC) | `TestDrainer_ClaimOrdering` |
| SKIP LOCKED disjointness under concurrency | `TestDrainer_ConcurrentClaimsDisjoint` |
| Lease expiry → pending + attempt strike; strike-3 → dead | `TestDrainer_LeaseExpiry` |
| Fetch-failure dead-letter + ledger spend | `TestDrainer_FetchFailureDeadLetter` |
| Infra error (nil,err) → bump, no ledger | `TestDrainer_InfraFailure` |
| Daily cap → pending, zero traffic | `TestDrainer_LedgerCapSkip` |
| Killswitch / disabled adapter → pending, zero traffic | `TestDrainer_{KillswitchSkip,DisabledAdapterSkip}` |
| Allowlist order + capped-lane fallthrough | `TestDrainer_LaneOrderAndCapFallthrough` |
| Refresh deepens fresh-partial (A2 regression) | `TestDrainer_RefreshDeepensFreshPartial` |
| FAILED-cooldown serve → release, no bump (pc=0) | `TestDrainer_CooldownFailedNoTraffic` |
| Unresolvable network → dead | `TestDrainer_UnresolvableNetworkDead` |
| SIGTERM mid-drain → detached release, no bump | `TestDrainer_ShutdownReleasesClaim` |
| Disabled Run exits immediately | `TestDrainer_DisabledRunExits` |
| Hop-1-only, sentinel skip, category allowlist, foreign label, fail-open | `internal/tracing/warm_test.go` (`TestExposures_WarmEnqueue*`, `TestWarmEnqueuePlan`) |
| Env parsing/defaults | `internal/config/config_test.go` |

### Invariants checklist (from AGENTS.md §5 + contract MUST NOTs)

- `internal/transaction/*` has **zero diff** — `EnsureIndexed`, singleflight, budgets,
  killswitch semantics untouched; the interactive path is byte-identical.
- No crawling: only hop-1 neighbors that *already produced* a signal are enqueued.
- `GAMBLING`/`EXCHANGE` (classifications, not crimes) and listing-only signals never enqueue.
- Address logs are masked (`0x1234…cdef`); no secrets/keys in code, logs, or `.env.example`.
- Provider failure never becomes `done` — failures bump attempts toward `dead`.
- Parameterized sqlc queries only; claim-gated outcome writes (`AND status='claimed'`).

## 8. Known v1 Limitations (accepted in plan)

- Under-count window between `EnsureIndexed` return and ledger write on crash
  (bounded by `WARM_INDEX_ITEM_BUDGET`).
- Soft daily cap under concurrent drains (claim-then-spend, no reservation).
- `done` = "one deeper pass completed", not "full history".
- Single-node worker assumed for the 60s default cadence; multi-instance is safe
  (SKIP LOCKED) but multiplies effective spend — raise `POLL_INTERVAL` if desired.

## 9. Test-Harness Caution (learned during Stage-4 saturation)

Do **not** run two `go test` binaries against the same dedicated `*_test` database
concurrently (e.g. a `-race` run alongside a `-count=10` loop). Cross-binary
`TruncateAll` pollutes assertions. Package-private DBs isolate *within* one
`go test` invocation only. Always saturate sequentially.
