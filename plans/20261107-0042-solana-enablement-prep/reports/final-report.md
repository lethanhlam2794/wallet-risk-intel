# Final Report — Solana Enablement Prep ("code-complete but INACTIVE")

Plan: `plans/20261107-0042-solana-enablement-prep/plan.md`
Executed by Devin Cook, phases 1→5 in order, all completed.
Objective met: Solana support is **code-complete but still INACTIVE** —
a verified provider decision, a dormant `provider.TxProvider` adapter,
evidence-backed indexer semantics, and zero runtime enablement.

## Phase checkpoints

| Phase | Outcome | Evidence |
|---|---|---|
| 1 Audit drift check | All 13 claims VERIFIED, zero drift | `reports/audit-drift-check.md` |
| 2 Provider eval | Keyless public Solana JSON-RPC chosen; all discovery rows adjudicated | `reports/probe-solana-rpc.md`, `docs/solana-provider-decision.md`, `docs/non-evm-provider-discovery.md` |
| 3 Indexer semantics | (a)–(h) resolved; INV-S1..S5 pinned | `docs/solana-indexer-semantics.md` |
| 4 Dormant adapter | `internal/provider/solanarpc/` — 32 tests green (28 named + 4 blind), zero prod imports | `apps/api/internal/provider/solanarpc/` |
| 5 Gates + docs | gofmt/vet/build/test clean; audit updated | `reports/gate-evidence.md`, `docs/non-evm-audit.md` |

## What was decided (and why)

- **Provider**: keyless public Solana JSON-RPC (`api.mainnet-beta.solana.com`).
  Keyless ⇒ no approval needed; satisfies the six contract requirements
  with one declared limitation (SPL owner-feed discovery gap — see below).
  Probed live: finalized head, 1000-sig page cap, exclusive before/until,
  global positional `before` seeds, `transactionIndex` intra-slot order,
  jsonParsed tx shape, batching, ledger floor signals.
- **Helius** = documented keyed-free upgrade path closing the SPL gap —
  NOT selected (a keyed adapter requires explicit user approval per the
  task's own rule).
- **Adapter**: `internal/provider/solanarpc/` implements the two-phase
  protocol — bounded desc signature collection emits **zero rows until
  the frontier passes below the observable window bottom**
  (INV-S1, contiguous-ascending-prefix), then emits ascending
  `(slot, transactionIndex)` slices via batched `getTransaction`.
  Finalized-only reads; `getFirstAvailableBlock` + feed exhaustion as the
  truncation signal (floor > F ⇒ honest data emitted, stream never
  finished ⇒ `partial` + `history_truncated`).
- **SPL coverage**: honestly scoped to "txs where the queried owner is a
  tx account" — `spl.Finished` is truthful under it; the ATA blind spot
  (empirically proven on the Binance USDC deposit) is declared, never
  silently claimed.
- **Identity**: `{n|t}:{hash16}.{k}` verbatim reuse; base58 sigs carried
  verbatim — the `identity.go:116` `strings.ToLower` corruption is a
  documented §15 blocker, not fixed here per scope.

## Calls-per-ensure recap

Worst case under `MaxPagesHop0=5` with pinned bounds
(sigPageLimit=1000, maxSigPagesPerStep=10, emitCap=100 sigs,
batchSize=50, maxCollectSigs=20000, maxSeedWalk=5):

`1 head + 1 floor + ≤5 seed + ≤50 sig-pages + ≤10 batch HTTP` =
**≤67 HTTP requests/ensure** (UpstreamCalls counts real requests; a
JSON-RPC batch = 1), plus ≤500 logical getTransaction calls. ~2 req/s
limiter paces within the documented public quota (100 req/10s/IP total,
40/10s per-method). Reconciled in `docs/solana-provider-decision.md` and
`solana-indexer-semantics.md`.

## Stop-condition ledger — every rule honored

| Rule | Status |
|---|---|
| Never flip `is_active` | ✅ solana row untouched, still FALSE |
| No transactions/wallet_index_state DDL | ✅ zero migration/query diffs |
| No selector/providerconfig/data_sources/cmd wiring | ✅ adapter imported by nothing (`go list -deps` verified) |
| No solana.csv / wattrFiles | ✅ neither exists/touched |
| No OpenAPI Address generalization | ✅ pattern unchanged |
| No warmindex edits | ✅ untouched |
| No file deletions / no EVM-TRON behavior change | ✅ all changes additive; `go test ./...` all green |
| No embedded/paid API key | ✅ keyless adapter; `New(baseURL, chains)` has no key param |
| Card-required provider ⇒ BLOCKED / keyed-only ⇒ DONE_WITH_CONCERNS | n/a — a viable **keyless** provider was found |
| Adapter dormant: no prod import, init() only allowlist, Enabled() gated | ✅ all three asserted in tests |

## Remaining §15-gated enablement blockers (by design)

Identity `NormalizeEVM`→`wallet.Normalize` switch + `identity.go:116`
`ToLower` base58 fix; `'evm'` literal in `queries/transactions.sql:30`;
list asset normalization; provider selector/config/data_sources/env
wiring; `wattrFiles`/`solana.csv` artifact; OpenAPI `Address` pattern;
OFAC SOL mapping decision; warmindex EVM gates; NULL-`block_timestamp`
keyset consequence; SPL coverage decision (accept owner-feed scope or
approve Helius); the `is_active` flip itself.

## Files added/changed

- `apps/api/internal/provider/solanarpc/{adapter,mapper,adapter_test,adapter_blind_test}.go` (new)
- `docs/solana-indexer-semantics.md`, `docs/solana-provider-decision.md` (new)
- `docs/non-evm-provider-discovery.md`, `docs/non-evm-audit.md` (updated)
- `plans/20261107-0042-solana-enablement-prep/reports/{audit-drift-check,probe-solana-rpc,gate-evidence,session-log,final-report}.md`
- Phase frontmatter/status across `phase-01..05`.
- Commits: `3877620` (plan+audit+decision), `8535521` (semantics), `dec9300` (adapter), Phase-5 commit.

## Known limitations (declared, not hidden)

1. SPL owner-feed discovery gap — incoming ATA transfers where the owner
   is absent from accountKeys are unindexable until a provider-level
   owner-indexed API (Helius) is approved.
2. Public-RPC quota is dev-grade (documented "not intended for
   production"); resilience pacing + retries are built-in but sustained
   throughput needs the keyed path at enablement.
3. `maxCollectSigs=20000` bounds token-held sigs; extreme windows beyond
   it degrade to honest `partial`+`history_truncated` (rare for real
   wallets; documented in semantics §b).
