# Prompt: codex — warm-index-queue

> Created: 20261105-2155 | Target: codex | Template: M (agentic)

## Prompt

```
You are working in /home/lethanhlam/Desktop/Profile/check-wallet (Go + Postgres monolith, rules in AGENTS.md, spec docs/tailieu.md). All Go commands run from apps/api/.

## Context (carry forward)
- Transaction indexing is lazy-on-read: transaction.Service.EnsureIndexed(ctx, net, normalized, EnsureOptions{Refresh, Budget, MaxPages}) in internal/transaction/service.go indexes a wallet only when requested, using wallet_index_state checkpoints, singleflight, provider budgets, failover, and a DB-backed killswitch.
- tracing.Service (internal/tracing/service.go) already calls EnsureIndexed for the root wallet and 1-hop BFS neighbors during screening.
- Provider adapters live in internal/provider/<name>/; provider_configs + provider status live in DB; ingestion workers live under internal/ingestion/ and cmd/worker/.
- Free-tier provider quota expires unused (ankr ~200M credits/month, blockscout_pro ~100K/day, blockscout public is keyless). Goal: spend leftover quota on signal-prioritized background indexing without ever starving interactive requests.

## Task
Implement "warm index queue" v1: a bounded background path that deep-indexes wallets which already showed risk signals, drained by a quota-aware worker.

## Design contract (v1 — keep it minimal)
1. Migration 000023 creates two tables:
   - warm_queue: id bigserial pk, network_id, address (normalized lowercase hex), priority int, reason text, source_screening_id nullable, status text CHECK in ('pending','claimed','done','dead'), attempts int default 0, claimed_at timestamptz nullable, created_at, updated_at. Partial unique index on (network_id, address) WHERE status IN ('pending','claimed') for dedupe.
   - warm_quota_ledger: provider_code text, period_start date, calls int, PRIMARY KEY (provider_code, period_start).
   Down migration drops both. Additive only — no existing-table changes.
2. internal/warmindex package: Enqueue(ctx, netID, addr, priority, reason, screeningID) — idempotent upsert (ON CONFLICT bump priority if new priority higher, never duplicate). Drain loop: claim ONE pending item by highest priority (UPDATE ... SET status='claimed', claimed_at=now() WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED)), check quota ledger for the eligible provider < daily cap, then call EnsureIndexed with a background Budget and extended MaxPages. Mark done/dead.
3. Enqueue hook: in tracing.Service, when a 1-hop neighbor produces a direct sanctions match or a risky label (SCAM/PHISHING/HACK/RANSOMWARE/MIXER), enqueue that neighbor for deeper indexing. GAMBLING/EXCHANGE labels MUST NOT enqueue. Enqueue is fail-open: enqueue errors are logged and never fail the screening request.
4. Drain loop runs in cmd/worker (follow existing ingestion worker patterns in internal/ingestion/): poll interval configurable via env (default 60s), one item per cycle, graceful SIGTERM shutdown.
5. Quota ledger: keyed (provider_code, period_start=current UTC date). Before each drain, select the first enabled provider for the wallet's network from a fixed warm-pool allowlist (config env WARM_INDEX_PROVIDERS, default "blockscout,blockscout_pro"); increment ledger only on an attempted upstream call. Daily cap via env WARM_INDEX_DAILY_CAP (default 200). If no provider under cap: skip cycle (do not touch keyed providers outside the allowlist — interactive lane keeps its providers).
6. Crash recovery: items claimed >15min ago are reset to pending on next claim pass (lease expiry). attempts >= 3 → status 'dead'.
7. Feature flag: WARM_INDEX_ENABLED env (default false). Disabled → drain loop exits immediately, enqueue hook becomes no-op.

## MUST NOT
- Do not crawl arbitrary addresses or whole chains — enqueue only from the tracing risk-signal hook above.
- Do not modify EnsureIndexed, singleflight, budget, or killswitch behavior; the interactive path MUST be byte-identical in behavior.
- Do not consume providers outside WARM_INDEX_PROVIDERS; do not add paid providers.
- Do not store secrets or keys; do not log api keys or full env values.
- Do not convert upstream/provider failure into success: failed drains stay 'pending'/'dead' with attempts incremented, never 'done'.
- Do not create abstractions beyond the contract above; no event bus, no external queue library (KISS — Postgres SKIP LOCKED is the queue).

## Failure/scenario requirements (acceptance criteria)
- Two concurrent drain passes MUST NOT claim the same item (SKIP LOCKED test).
- Enqueueing the same (net,addr) twice produces one row (dedupe test).
- Ledger at cap → drain skips, zero provider calls (test with ledger seeded at cap).
- Provider killswitch/status inactive → item is not attempted, stays pending (test).
- Worker crash mid-claim → item returns to pending after lease expiry (test with claimed_at backdated).
- EnsureIndexed returning error → attempts+1, error logged with provider_code + network + masked addr, no panic.
- GAMBLING/EXCHANGE neighbor → no enqueue (test).
- Enqueue failure during screening → screening still succeeds (fail-open test).

## Tests
Table-driven unit tests beside source for enqueue dedupe/priority, claim ordering, lease expiry, dead-letter, quota-cap skip, killswitch skip, label filter. Reuse existing test helpers/patterns from internal/transaction/*_test.go and internal/ingestion tests.

## Verification gates (run all, report output)
cd apps/api && gofmt -l . && go build ./... && go vet ./... && go test ./...

## Stop conditions — halt and report instead of guessing
- Any conflict with AGENTS.md domain invariants or docs/tailieu.md.
- Migration numbering conflict or schema decision not covered above.
- A requirement that would modify the interactive indexing path.

After each major step output a one-line checkpoint. When done, report: files changed, migration applied status, test results, and any deviation with reason.
```

## Strategy

Agentic task format — starting state + design contract + scope locks + scenario-derived acceptance criteria; Scenario Gate findings (interactive starvation, cross-process claim races, crash-resume lease, killswitch mid-run, quota reset semantics) encoded as hard tests instead of prose.

## Notes

Review scope locks, forbidden actions, and stop conditions before pasting — Codex has real system access. Feature flag defaults to OFF, so merging is safe before ops decides to enable the drain loop.
