Codex rebuttal to your round-1 review — I concede A1–A3 as real defects and have applied all amendments. Revised artifacts are on disk; please re-verify and emit PLAN_CONSENSUS_REACHED if satisfied.

## Concessions

- **A1 (blocking)** — you were right; `tracing.Service` runs only inside cmd/api and nothing called `SetEnqueuer`. Phase-04 now explicitly owns producer wiring in `cmd/api/main.go::run()` after `buildTransactions`: `warmindex.NewQueue(queries, cfg.WarmIndexEnabled, log)` + `tracingSvc.SetEnqueuer(queue, log)` — always wired; the Queue's enabled flag makes it a no-op (one code path). Roadmap file list updated.
- **A2 (blocking)** — deepening defect confirmed: `ensure():265` serves the just-indexed `partial`+fresh row (the exact warm-queue input) and the item would complete with zero deepening. New **D3a**: every drain calls `EnsureOptions{Refresh: true, Budget: NewBudget(itemBudget), MaxPages: maxPages}` — Refresh bypasses freshness only (FAILED NegativeTTL cooldown holds) and re-scan is watermark-incremental `[wm−tail+1,head]`. Phase-02 adds the regression test: seeded fresh `partial` row → drain must make real `LookupTransfers` calls.
- **A3** — your `Fetched`-ambiguity point is sharper than your own proposed fix: `Fetched=false,status=failed` covers BOTH cooldown-served rows (0 calls) AND head-block fetch failure (1 call spent). I replaced `Fetched` with `ProviderCalls` as the discriminator — it's incremented on the HeadBlock attempt itself, so `pc>0` ≡ "attempted upstream call", aligning the attempts-bump with the ledger increment exactly. The full decision table is now in phase-02 (evaluation order pinned).

## Decisions on your open questions (A4–A6)

- Unresolvable `network_id` → `dead` + error log (permanently unprocessable).
- Lease reset **bumps attempts** (new D10): `ResetExpiredWarmClaims` sets `attempts+1` with the same `dead`-at-≥3 CASE — a poison pill reaches `dead` in 3 strikes instead of re-claiming forever. Graceful SIGTERM releases *without* bump via `ReleaseWarmClaim`; only true crashes leak. A legit drain (≈8 calls) can't hit the 15m lease.
- Claim release on ctx-cancel and enqueue both use `context.WithoutCancel` + small timeout.
- `ProviderLane.Code = Adapter.Name()` (your (e) — identity chain honesty).
- Enqueue: `WithoutCancel`+2s (your #6); sentinel skip via `sentinels[cp]` (your #8); foreign `label` sigs with risky Category DO enqueue with reason `"label:<CAT>@<net>"` — the cp is a real on-network counterparty; foreign evidence is prioritization input, never a direct-listing claim (your #7, recommended side); `screeningID = nil` everywhere.
- Narrowed risky set (DRAINER/MONEY_LAUNDERING/TERRORISM/DARKNET/EXTREMISM/FROZEN excluded) is contract-mandated — now written down as a decision.
- warmdrain builds its own `signal.NotifyContext` (never `run()`'s OFAC+30m timeout); minimal deps — no Redis/fetchers; `envBool` = `strconv.ParseBool` vocabulary with boot-error on garbage; `ReorgTail`/`WindowCap` copied verbatim incl. BSC-only asymmetry; `selectLane`-miss logs at info.
- `todayUTC` computed once per RunOnce; `min(itemBudget, laneBudget)` clamp documented; BSC `WindowCap` honesty note + "one deeper pass per enqueue, not complete history" added to risk section.
- `CHECK (address ~ '^0x[0-9a-f]{40}$')` added to `warm_queue.address` (your nit — invariant at DB boundary).
- `make sqlc` is now a phase-01 success gate; decided fallback for the arbiter grammar is UPDATE-then-INSERT in a tx.

Re-verify the amended files: `plans/20261105-2205-warm-index-queue/plan.md` (D1–D10), `phase-01` … `phase-04`. If any amendment introduces a NEW defect, say so; otherwise emit `PLAN_CONSENSUS_REACHED` plus any remaining nits. Read-only mode — do not write files; emit your verdict as reply text.

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
