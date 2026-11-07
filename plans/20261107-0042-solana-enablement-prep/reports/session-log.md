# Session Log — Solana enablement-prep (Devin Cook execution)

Working dir: `/home/lethanhlam/Desktop/Profile/check-wallet`
(Go module root `apps/api/`). Executed phases 1→5 in order.

## Phase 1 — audit drift check

Read all compliance inputs (AGENTS.md, engineering-standard, learning
ledger, plan + 5 phase files, binding prompt). Verified all 13 planned
claims against current code — `GetOrCreate` address_type stamping, OFAC
SOL/TRX fallthrough, identity EVM-only normalize surface incl. the
`identity.go:116` `strings.ToLower` base58-corruption landmine, warmindex
EVM gate, `wallet.Normalize` dispatch, migration 000026 inactive state +
down-migration guard, TxProvider contract & ascending-order rule,
indexer stream/watermark/partial mechanics, active-only resolver +
wattrFiles, resilience toolkit, NULL-ts keyset behavior, slot-based
confirmation semantics. **Zero drift** — every claim VERIFIED with
file:line evidence.

Output: `reports/audit-drift-check.md`; phase-01 frontmatter → completed.

## Phase 2 — provider evaluation (live keyless probes)

Keyless curl probes of `https://api.mainnet-beta.solana.com` (transcripts
in `reports/probe-solana-rpc.md`):

- `getSlot{finalized}` → 466200957; `getFirstAvailableBlock` → 0
  (archival/BigTable); `minimumLedgerSlot` → 466111843 (local RocksDB
  floor — does NOT bound the sig feed).
- `getBlock(1000)` → valid data, `blockTime: null` — NULL timestamps on
  finalized entries proven.
- `getSignaturesForAddress`: limit max 1000 (`>1000` → -32602); global
  desc order by `(slot,transactionIndex)` verified over a 1000-row page
  spanning 5 slots on the token-program feed; `before` and `until` both
  exclusive; `before` works as a **global positional seed** (foreign sig
  anchored the feed correctly) enabling `getBlock(T+1).signatures[0]`
  seeding; failed txs (err≠null) appear in the feed.
- `getTransaction{jsonParsed,maxSupportedTransactionVersion:0}`:
  parsed SPL ixs carry source/destination token accounts + mint;
  tokenBalances carries `accountIndex→{mint,owner}` (the owner-attribution
  path); version flag mandatory (-32015 without it).
- JSON-RPC batching works (out-of-order responses keyed by id).
- `getBlockSignatures` disabled (-32601); `processed` commitment rejected
  (-32602); `confirmed` probed but never relied on; observed HTTP 429s
  (documented public quota: 100 req/10s/IP, 40/10s per-method).
- **SPL owner-feed discovery gap PROVEN**: a plain USDC deposit into a
  Binance ATA is invisible on the owner feed (owner absent from
  accountKeys); `getTokenAccountsByOwner` returned 2834 accounts on one
  wallet — per-ATA enumeration unbounded; closed ATAs unenumerable.
- PublicNode keyless endpoint probed: `getFirstAvailableBlock` ≈465.6M —
  a real ~3-day retention floor (contrast case for §f semantics).

Vendor docs (URL + retrieval date recorded): Solana public-RPC limits,
Helius plans/credits/rate-limits/terms (keyed free tier: 1M credits/mo,
10 RPC RPS — Enhanced Transactions in maintenance), QuickNode (trial
only), GoldRush, Ankr, Solscan Pro.

Adjudication: **keyless public Solana JSON-RPC chosen** for the dormant
adapter; Helius recorded as the approval-gated keyed upgrade path closing
the SPL discovery gap; Ankr/GoldRush/Tatum/QuickNode/Solscan rejected
with cited requirement failures. All UNCONFIRMED rows in
`docs/non-evm-provider-discovery.md` flipped to VERIFIED/REJECTED.

Output: `reports/probe-solana-rpc.md`, `docs/solana-provider-decision.md`,
updated `docs/non-evm-provider-discovery.md`; phase-02 → completed.
Commit `3877620`.

## Phase 3 — indexer semantics doc

`docs/solana-indexer-semantics.md` resolves (a)–(h) with probe/code
evidence: slot→block_number + transactionIndex order; two-phase
collect-then-emit bridge enforcing the contiguous-ascending-prefix
invariant (foreign-sig `before` seeds, pinned bounds, token-carried
state); finalized-only head/finality; verbatim `{n|t}:{hash16}.{k}`
identity + owner-attributed SPL endpoints; instruction-level SPL
extraction (Token + Token-2022, failed-tx exclusion, fee never a
transfer, mint via ix or tokenBalances); ledger-floor truncation →
`partial`+`history_truncated` composition; Kinds mapping + NULL-ts
honesty + slot-distance Confirmations; scoped SPL discovery declaration
(honest blind spot, Helius upgrade path). Binding invariants INV-S1..S5,
named non-options, enablement-blockers table, calls-per-ensure
reconciliation (≤67 HTTP/ensure worst case).

Output: phase-03 → completed. Commit `8535521`.

## Phase 4 — dormant adapter (TDD)

`internal/provider/solanarpc/` — `adapter.go` (state machine, JSON-RPC
plumbing, page token, resilience client ~2 req/s keyless pacing, SSRF
allowlist init), `mapper.go` (jsonParsed extraction, accountKeys decode,
tokenBalances owner/mint attribution), `adapter_test.go` (23 named-fixture
tests), `adapter_blind_test.go` (randomized property suite).

Green run: `ok … 19.581s` unit suite, then `ok … 51.124s` incl. blind
trials. `go list -deps ./cmd/api ./cmd/worker | grep -i solana` → empty:
**zero production imports — dormant confirmed.**

Output: phase-04 → completed. Commit `dec9300`.

## Phase 5 — gates + docs + reports

- `gofmt -l .` empty; `go vet ./...` clean; `go build ./...` clean;
  `go test ./...` all ok.
- `docs/non-evm-audit.md` updated: dormant solanarpc adapter + semantics
  docs recorded as shipped; §15-gated enablement blockers remain
  enumerated (identity normalize+ToLower, 'evm' SQL literal, list asset
  normalization, selector/config/data_sources wiring, wattrFiles,
  OpenAPI pattern, OFAC SOL mapping, warmindex gates, is_active flip,
  NULL-ts keyset consequence).
- Verified: solana `is_active=FALSE` unchanged; no solana.csv; wattrFiles
  untouched; `/networks` still BSC+Ethereum only (resolver active-only,
  unchanged code path); zero migration/query diffs.

Output: `reports/gate-evidence.md`, `reports/final-report.md`, this log;
phase-05 → completed.
