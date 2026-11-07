# Feature Map & Guidebook — Solana Enablement Prep (code-complete, INACTIVE)

**Scope**: Solana groundwork shipped under the §15 gate — a dormant
`solanarpc` `TxProvider` adapter over the keyless public Solana JSON-RPC,
plus the probe-verified provider decision and indexer semantics a §15
reviewer needs. **Solana is NOT enabled**: registry row `is_active=false`,
adapter unwired (nothing imports it), no `data_sources` row, no env
surface. Nothing in this doc activates anything.

- Plan: `plans/20261107-0042-solana-enablement-prep/`
- Semantics decisions: `docs/solana-indexer-semantics.md`
- Provider decision: `docs/solana-provider-decision.md`
- Live probe transcript: `plans/20261107-0042-solana-enablement-prep/reports/probe-solana-rpc.md`
- Independent review: `plans/20261107-0042-solana-enablement-prep/reports/review-round-1.md`
- Audit refresh: `docs/non-evm-audit.md`, `docs/non-evm-provider-discovery.md`

---

## 1. What was delivered

| Deliverable | Where | State |
|---|---|---|
| `solanarpc` `TxProvider` adapter (`sol` native + `spl` token streams) | `internal/provider/solanarpc/` | **dormant** — compiles, 32 tests green, zero production imports |
| Two-phase collect-then-emit protocol (desc feed → ascending-prefix contract) | `adapter.go` | INV-S1 enforced by construction: zero emission until collection passes `max(FromBlock, ledgerFloor)` |
| Owner-attributed SPL extraction (`transfer`/`transferChecked`, Token + Token-2022) | `mapper.go` | ATA→owner via `tokenBalances`; unattributed endpoints keep the raw ATA verbatim |
| Solana indexer semantics (a)–(h) + INV-S1..S5 | `docs/solana-indexer-semantics.md` | written for §15 review; post-review `max(FromBlock, floor)` bound corrected |
| Provider decision + live keyless probes | `docs/solana-provider-decision.md`, `reports/probe-solana-rpc.md` | keyless public RPC chosen; Helius keyed-free recorded as the approval-gated upgrade for the SPL owner-feed gap |
| Independent code review (6 Minor + 11 Nit → remediated) | `reports/review-round-1.md` | verdict APPROVED; remediation commit on top |

### Deliberately NOT done (§15 blockers, documented)

- No `is_active` flip; no migration; no `data_sources`/`providerconfig` row.
- No selector wiring — `solanarpc` is imported by nothing outside its package.
- `identity.go:116` `strings.ToLower` on `TxHash` (corrupts case-sensitive
  base58 sigs), `identity.go` `NormalizeEVM` sites, `'evm'` SQL literal in
  `transactions.sql`, `list.go` asset normalization — all enablement tasks.
- `wattrFiles`/`solana.csv`, OpenAPI `Address` pattern, OFAC `SOL` mapping,
  warmindex EVM gates — untouched by design.
- SPL owner-feed discovery gap is a **declared coverage limit**: transfers
  to ATAs whose owner isn't in `accountKeys` are unindexable on the owner
  feed (empirically proven — Binance USDC deposit probe §7); full coverage
  needs the approval-gated indexed-provider path (Helius Parsed Events or
  equivalent).

## 2. Source paths

```
apps/api/internal/provider/solanarpc/
  adapter.go              Adapter (TxProvider impl): URL/host validation,
                          Enabled() two-level gate, finalized-only HeadBlock,
                          seed walk (getBlock signatures), bounded desc
                          collection, feed-order guard, bounded ascending
                          emission, opaque page token, JSON-RPC + batch
                          plumbing, typed error mapping (429→rate_limited)
  mapper.go               getTxResult → provider.Transfer: system
                          transfer/transferWithSeed, SPL transfer/
                          transferChecked, Token-2022, accountKeys decode,
                          tokenBalances mint/owner attribution, inner-
                          instruction dedup/range checks, NULL blockTime
  adapter_test.go         28 named-fixture contract tests (httptest double)
  adapter_blind_test.go   4 randomized property tests (BLIND_SEED env)
```

## 3. Test IDs (Go test anchors — the `data-testid` equivalent)

| Behavior | Anchor |
|---|---|
| Zero wire traffic when disabled | `TestDisabled_ZeroTraffic`, `TestBlind_DisabledNeverHitsUpstream` |
| Enabled() gating matrix | `TestEnabled_Gates` |
| Finalized-only head | `TestHeadBlock_Finalized`, `TestHeadBlock_Malformed` |
| Desc feed → ascending emission | `TestLookup_DescFeedAscendingEmission`, `TestLookup_CrossStepGlobalAscending` |
| Same-slot `transactionIndex` order | `TestLookup_SameSlotIndexOrder` |
| INV-S1 zero mid-collection emission | `TestLookup_MidCollectionZeroEmission`, `TestLookup_CollectCapZeroEmission` |
| Feed-order guard → degraded | `TestLookup_MisorderedFeedDegraded` |
| `before` seed when `fetchTo < head` | `TestLookup_BeforeSeedWhenToBelowHead`, `TestLookup_SeedFaultPropagates` |
| Ledger-floor truncation honesty | `TestLookup_FloorAboveFromBlocksFinish`, `TestLookup_EmptyFeedAboveFloorUnfinished` |
| Failed tx excluded / fee never emitted | `TestLookup_FailedTxExcludedAndFeeNeverEmitted` |
| SPL mint/owner attribution | `TestLookup_SPLExtraction`, `TestLookup_SPLBareTransferMintResolution` |
| NULL blockTime honesty | `TestLookup_NullBlockTime` |
| Typed error mapping | `TestLookup_RPCErrorTyped`, `TestLookup_RPC429RateLimited`, `TestLookup_Malformed`, `TestLookup_NullTxResultNoAdvance`, `TestLookup_TxSlotMismatchDegraded` |
| Batch = 1 HTTP request | `TestLookup_BatchUpstreamCalls` |
| Invariants under random fixtures | `TestBlind_StreamInvariants`, `TestBlind_PageTokenGarbage`, `TestBlind_MalformedNeverPanic`, `TestBlind_ReplayIdentity` |

## 4. Verification (reproduce it)

```bash
cd apps/api
gofmt -l .                                   # empty
go vet ./...                                 # clean
go build ./...                               # clean
go test -count=1 ./internal/provider/solanarpc/   # 32 tests, ~45s
BLIND_SEED=<n> go test -count=1 ./internal/provider/solanarpc/  # any seed
go list -deps ./cmd/api ./cmd/worker | grep -i solana   # empty = dormant
```

Blind saturation: seeds `101,1302,2503,3704,4905,6106,7307,8508,9709,10910`
→ **10/10 PASS** (`SATURATION_SCORE=10/10`, Stage-4 evidence).

## 5. Live DB state (dev stack :5433, verified 2026-11-07)

```sql
SELECT code, is_active, name FROM blockchain_networks ORDER BY code;
-- bsc t / ethereum t / solana f / tron f        ← solana stays inactive
SELECT count(*) FROM data_sources WHERE code='solana-rpc';   -- 0
```

`/networks` returns `WHERE is_active = TRUE` (`queries/networks.sql`) —
solana can never surface on the API; the dormant adapter can never emit
traffic (resolver gate + `Enabled()` + zero production imports).

## 6. §15 enablement checklist (the flip, when approved)

1. Review `docs/solana-indexer-semantics.md` blockers table +
   `docs/solana-provider-decision.md` quota limits (public RPC is
   "not intended for production" — keyed endpoint strongly advised).
2. Fix `identity.go:116` ToLower + `NormalizeEVM` call sites → per-network
   `wallet.Normalize`; `'evm'` literal in `transactions.sql`; `list.go`
   asset normalization; NULL-`block_timestamp` keyset consequence.
3. `data_sources` row + providerconfig descriptor + selector wiring +
   `TX_PROVIDER_SOLANA` env surface.
4. Decide SPL coverage policy: accept the documented owner-feed gap or
   adopt the approval-gated indexed provider (Helius free tier is the
   recorded candidate — keyed, needs approval).
5. `solana.csv` + `wattrFiles` entry; OpenAPI `Address` pattern
   generalization; OFAC `SOL` mapping; warmindex gates.
6. Then — and only then — `UPDATE blockchain_networks SET is_active=TRUE
   WHERE code='solana'`.
