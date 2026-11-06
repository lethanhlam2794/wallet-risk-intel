Devin Critic — Codex rebuttal & amendment report (round 2).

All 10 amendments accepted; plan files updated on disk (phase-01, phase-02, phase-03 — please re-read them). Point-by-point:

- A1 CONCEDED — phase-3 sketch rewritten with correct signatures (`cfg, err := config.Load()`, `database.Migrate`/`Connect` error checks, `defer pool.Close()`/`defer stop()`).
- A2 CONCEDED — phase-2 now mandates reuse of package helpers from `queue_test.go`/`drain_test.go` (`testDSN`, `netByCode`, `warmRow*`, `insertWarmItem`, `addrW*`, `stubTx`, `testCfg`, `checkerFunc`); only seed-specific fixture helpers are added. Compile-blocker noted.
- A3 CONCEDED — fixture checklist added: `fingerprint` UNIQUE, `identifier_type`, `listed_address`, distinct `uq_sanctions_wallet_identifier` tuples, seeded `wallet_attribution` source for labels; `sanctions_snapshots` fixture explicitly excluded.
- A4 CONCEDED — and improved. Instead of SQLSTATE inspection, the seeder runs a canonical pre-check: after `IsEVMAddress` passes, `wallet.NormalizeEVM(addr)` must succeed AND return the input unchanged, else `errors++` (bad row, batch continues). Once pre-check passes, ANY `Enqueue` failure is infra by construction → abort run. `ctx`/`bctx` death → abort. Clean discriminator, zero error-string inspection, and it keeps non-EVM input from ever reaching `NormalizeEVM` (prompt invariant).
- A5 CONCEDED — the 30s child ctx covers the SELECT plus that batch's enqueue iteration; ctx-done during the batch aborts (infra), never `errors++`.
- A6 CONCEDED — `MaskAddr` on all per-row log lines (repo convention).
- A7 CONCEDED — `NewSeeder` stays single-return; `Run()` returns an error on `batchSize < 1` → non-zero exit.
- A8 CONCEDED — counter semantics documented in-code: `enqueued` = accepted upsert attempts (deduped re-enqueues count; signature is frozen); `skipped_inactive_net` includes NULL `network_id` (intentional gap per anti-cross-listing invariant); `skipped_fresh` includes `indexing`/`disabled`; all six keys emitted per source line. Fidelity note recorded: `indexing`/`disabled` + `history_truncated` seeds at p5 per literal OR-logic — drain releases at pc=0, harmless.
- A9 CONCEDED — queries rewritten to `sqlc.narg('cursor_*')::bigint` + `LIMIT sqlc.arg('lim')::bigint` matching `queries/transactions.sql`; `make tools` pins sqlc v1.30.0; `internal/db` diff must be scoped to `warm_seed.sql.go` only.
- A10 CONCEDED — run intentionally unbounded (per-batch bounds only; cron wraps it); smoke env exports BOTH `DATABASE_URL` and `WARM_INDEX_ENABLED=1`.

Blind-spot dispositions:
- #4: agreed — `skipped_non_evm` fixture pairs a non-EVM address with an ACTIVE EVM net; a Base58 addr on inactive TRON counts `skipped_inactive_net` (net-gate first). Both asserted.
- #9: agreed — idempotent-replay asserts unique ACTIVE rows (`status IN ('pending','claimed')`), since done/dead legitimately re-enqueue.
- #6: agreed — NULL-network_id skip is intentional and now commented as such.
- Others (1,2,3,5,7,8,10): folded into the amendments above.

Please re-verify the amended plan files and deliver your verdict: PLAN_CONSENSUS_REACHED or remaining blockers.
