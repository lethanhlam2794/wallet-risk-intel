# Solana TxProvider Decision

Status: **decided — keyless public Solana JSON-RPC**
(`https://api.mainnet-beta.solana.com`; dormant adapter target; still
§15-gated for enablement). Verified 2026-11-07 via live keyless probes
(transcribed in
`plans/20261107-0042-solana-enablement-prep/reports/probe-solana-rpc.md`)
plus current vendor docs. Every claim cites a URL + retrieval date or a
verbatim probe result.

## Decision

| # | Requirement | Public Solana JSON-RPC verdict | Evidence |
|---|---|---|---|
| 1 | Per-address native + token history | **PARTIAL-PASS with a declared blind spot** | `getSignaturesForAddress` + `getTransaction` (jsonParsed) enumerate every tx where the owner is in `accountKeys`, incl. SOL transfers and SPL `transfer`/`transferChecked` (outer+inner) — live probes §3,§5. **Proven gap (problem h):** incoming SPL transfers to a wallet's ATA are unindexable on the owner feed — deposit tx `4UsiwZTYEBq…` (slot 359509488, 1.0 USDC into Binance ATA `3hNzYSQs…`) is on the ATA feed while owner `9WzD…` is absent from `accountKeys` (probe §7). Per-ATA enumeration costs `getTokenAccountsByOwner`=2834 accounts on one busy wallet and cannot see closed ATAs. → SPL stream is scoped to "txs where the owner is a tx account" (semantics doc §h) — an honestly declared coverage limit, not a silent gap |
| 2 | Deterministic ordering for watermarks | PASS | Feed is globally desc by `(slot, transactionIndex)` — 1000-row page over 5 slots, every multi-tx slot strictly desc, zero violations (probe §3). Ascending emission = reverse of receipt order |
| 3 | Typed finalized head/slot endpoint | PASS | `getSlot{commitment:"finalized"}` → 466200957 (probe §1). `commitment:"processed"` rejected `-32602` on history endpoints; `confirmed`/default return finalized rows (probe §8) |
| 4 | PageToken-compatible pagination | PASS (with bridge) | `before`/`until` cursors, both EXCLUSIVE (probe §4); page cap 1000 (`limit>1000` → `-32602`). Desc-only — the slot-window bridge is the adapter's core design problem (semantics §b). **`before` accepts foreign sigs as global positional seeds** (probe §4) — `getBlock(T,transactionDetails:"signatures")` anchors collection below `fetchTo` |
| 5 | Free tier without a credit card | PASS (keyless, rate-limited) | No key, no account, no card. Documented limits (solana.com/docs/references/clusters, retrieved 2026-11-07): **100 req/10s/IP total; 40 req/10s/IP per single method; 40 concurrent; 40 conn-rate/10s; 100MB/30s**. Exceeding → 429 `Connection rate limits exceeded` (probed). Calls-per-ensure model below fits under adapter-side pacing |
| 6 | Terms permit our use case | PASS (with caveat) | Official note (same doc): "The public RPC endpoints are **not intended for production applications**"; high-traffic sites "may be blocked without prior notice" (403). Our use = on-demand lazy indexing at bounded pace — permitted technically, but the §15 reviewer MUST weigh the non-production posture; Helius keyed-free is the documented upgrade path (below) |

### Sources (retrieved 2026-11-07)

- Rate limits + production-posture:
  https://solana.com/docs/references/clusters
- JSON-RPC method docs (cursor semantics, commitment, shapes):
  https://solana.com/docs/rpc (method set cross-checked against live
  responses — `getBlockSignatures` exists in docs but returns `-32601` on
  this endpoint; `getBlock transactionDetails:"signatures"` is the
  working seed source — probe §8,§2)
- Helius free tier (keyed fallback — NOT chosen): 1M credits/mo, 10 RPS
  RPC, 2 RPS Enhanced APIs, **no card required** —
  https://www.helius.dev/docs/billing/plans +
  https://www.helius.dev/docs/billing/llms.txt; Enhanced Transactions
  = 100 credits/req and **in maintenance mode** (successor: Parsed
  Events, 10 credits/req) — https://www.helius.dev/docs/billing/credits
- Helius ToS: https://www.helius.dev/terms (Cloud Services Agreement,
  retrieved 2026-11-07 — no card at signup per billing docs; commercial
  terms unblocked on review)

### Calls-per-ensure worst case

Under the Phase-3 design (`docs/solana-indexer-semantics.md`) with
`MaxPagesHop0 = 5` (`internal/transaction/service.go:49-57`):

```
1  head           getSlot@finalized
≤2 seed           getBlock(T, signatures-only) — first step only, only
                  when fetchTo < head (walks ≤ skipped-slot gaps)
M× (P desc pages  getSignaturesForAddress desc collection,
   + F fan-out)   getTransaction per emitted sig — JSON-RPC batched
                  (1 HTTP request carries ≤50 logical calls)
```

Pinned bounds (Phase 3 §b): `maxSigPagesPerStep=10`, `emitCap=100
transfers/step`, `batchSize=50` ⇒ per step ≤ 10 sig calls + ≤2 batch
calls + ≤2 seed calls = **≤14 HTTP requests**; ≤100 logical
`getTransaction` calls inside batches.

- **HTTP-request accounting**: `UpstreamCalls` counts real outbound
  HTTP requests (contract: "the real number of outbound requests",
  `provider.go:266`) — a batch = 1. Worst case/ensure = `1 + 5×14`
  = **71 HTTP requests**, inside the documented 100/10s aggregate and
  40/10s per-method caps under a ~4 RPS limiter (~18 s saturated).
- **Logical-call accounting**: upstream method pressure = ≤50
  `getSignaturesForAddress` + ≤250 `getTransaction` per ensure — the
  limiter paces per-method load to ≤4 RPS regardless of batching, so the
  upstream quota is honored by construction.
- Budget note: `settle` is post-hoc (`service.go:339-344`) — per-step
  constants are the only bound; `Budget["solana-rpc"]` must tolerate
  ~14 calls in a single step.
- PublicNode mirror (`solana-rpc.publicnode.com`) verified keyless-live
  — same shapes — but `getFirstAvailableBlock`≈465.6M ≈ **~3 days
  retention** (probe §9): usable only inside its retained window; NOT
  the primary.

## Rejected candidates

| Candidate | Failing req | Reason (verified 2026-11-07) |
|---|---|---|
| **Helius Enhanced Transactions** | — (reportable, not rejected) | Keyed FREE tier, **no card**: 1M credits/mo, 10 RPS RPC, 2 RPS Enhanced (docs above). Parsed feed folds transfers into one call — solves the SPL-discovery gap via provider-level owner indexing — BUT Enhanced Transactions is **in maintenance mode** at 100 credits/req, and keyed selection needs user approval per stop conditions. Not chosen: a viable keyless path exists (this doc). Documented as the upgrade path if SPL-incoming coverage becomes a requirement |
| **Ankr** | 1 | Advanced API chain list (https://www.ankr.com/docs/advanced-api/overview/, retrieved 2026-11-07): 16 EVM chains + Stellar — **no Solana indexed history**. Solana = standard JSON-RPC only with ~16h ledger retention (https://www.ankr.com/docs/rpc-service/chains/chains-api/solana/) — keyed AND truncated |
| **Covalent / GoldRush** | 1 | Solana Foundational REST explicitly **does not serve transactions or SPL transfer history** — "Solana transactions, NFTs… are NOT supported on Solana today"; SPL history only via paid warehouse normalizers (https://goldrush.dev/docs/skills/goldrush-solana/SKILL/, retrieved 2026-11-07) |
| **Tatum** | 5 | Free plan = 100K **lifetime** credits (https://docs.tatum.io/docs/plans-limits, verified for TRON eval 2026-11-06) — quota model fails identically for Solana |
| **QuickNode** | 5 | "Free" = **1-month trial** (10M API credits, 15 RPS, no card) then deactivation — not a durable free tier (https://www.quicknode.com/pricing + https://support.quicknode.com/articles/5387074982, retrieved 2026-11-07). Solana call ≈30 credits → ~333K calls total, one month only |
| **Solscan Pro API** | 1, 5 | Account transaction history (`/v2.0/account/transactions`, `account/transfer`) is a **PRO endpoint** — paid tiers from $49/mo; the free (Level-1) key covers core/network endpoints only (https://pro-api.solscan.io/pro-api-docs/v2.0/docs, retrieved 2026-11-07) |

## Out-of-table exclusions (named)

| Candidate | Why not evaluated as a TxProvider |
|---|---|
| **PublicNode / dRPC public** | Evaluated + live-verified as a **mirror**, not primary: real ledger floor ≈600K slots (probe §9) truncates history silently below it |
| **Alchemy Solana** | Keyed tiers only; no keyless surface — same approval gate as Helius with no advantage over it |
| **self-hosted RPC / Agave node** | Out of MVP scope — infra cost + ops; the public-RPC adapter's wire format carries over if one is later run |

## `Enabled()` recommendation for Phase 4

**Keyless**: `Enabled(net) = chains[lowercase(net.Code)]` where `chains`
is the env-configured set (empty ⇒ disabled) — blockscout parity
(`internal/provider/blockscout/adapter.go:182-184`).

Rationale: (a) dormancy is already triple-gated — package imported by no
binary, network `is_active=FALSE`, and no `chains` entry; (b) the keyless
rate surface is shared per-IP and aggressive, but every call is behind
the resilience client (limiter ≤4 RPS global, bounded retry honoring
429/degraded as typed errors — never fabricated results); (c) the public
endpoint's non-production posture is a §15 review input, not a code gate.
`network.Network` carries no `IsActive` (`resolver.go:12-18`) —
activeness is the resolver's gate, not the adapter's.
