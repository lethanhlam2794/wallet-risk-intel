# Cook Report — tx-provider expansion (phases 01–12)

Status: **phases 01–12 implemented, tested, and committed.** The first
Cook session checkpointed after phase-05 (context budget); a fresh
continuation session completed phases 06–12 — see the continuation
section below for outcomes, commits, and findings. The phase table and
commit list under "phases 01–05 session" reflect that first checkpoint;
the continuation table is authoritative for phases 06–12.

## Per-phase outcomes (phases 01–05 session checkpoint)

| Phase | Adapter | Outcome | Live-verify |
|---|---|---|---|
| 01 | alchemy (eth+bsc) | implemented | BLOCKED (no ALCHEMY_API_KEY) |
| 02 | blockscout (eth, keyless) | implemented | **VERIFIED** |
| 03 | ankr (eth+bsc) | implemented | BLOCKED (no ANKR_API_KEY) |
| 04 | covalent (bsc) | implemented | BLOCKED (no COVALENT_API_KEY) |
| 05 | moralis (bsc) | implemented | BLOCKED (no MORALIS_API_KEY) |
| 06–12 | see continuation section | all implemented & committed | — |

## Commits (phase order)

- `b9d3a7c` feat(provider): add alchemy transaction-history adapter
- `(02)` feat(provider): add blockscout transaction-history adapter
- `(03)` feat(provider): add ankr transaction-history adapter
- `(04)` feat(provider): add covalent transaction-history adapter
- `(05)` feat(provider): add moralis transaction-history adapter

## Test counts

- alchemy: 9 tests; blockscout: 11; ankr: 7; covalent: 6; moralis: 4 —
  all green at commit time (package suites + transaction/config touched
  packages; `go build ./...`, `go vet ./...`, `gofmt -l .` clean).
- Full `go test ./...` last run green through phase-02 milestone; per-phase
  package gates green at every commit.

## Live-verify evidence

- **blockscout (REQUIRED, PASSED)**: upstream `HeadBlock=26340027`;
  upstream `LookupTransfers` returned real rows; booted API
  `GET /api/v1/wallets/ethereum/0x000…12345/transactions?refresh=1` →
  `provider_status:"ok"`, `index_status:"fresh"`,
  `indexed_until_block:26340033`, `history_truncated:false`,
  `rows_indexed:7` (real ERC-20 rows incl. 2017-era history).
- All keyed adapters: honestly `live-verify: BLOCKED (no key)` and kept
  out of every default `TX_PROVIDER_*` selector (explicit operator
  override still accepted).

## Cross-cutting work landed

- `TX_PROVIDER_ETH`/`TX_PROVIDER_BSC` selectors: first entry serves;
  extras validated-then-warned (phase-12 failover pending); unknown
  names fail boot; defaults unchanged (chainscan/nodereal).
- Boot checks in `cmd/api/main.go`: every registered provider must
  resolve a `data_sources` row (`GetSourceByCode` — pins `Name()` to
  `data_sources.code`) and carry `Budget[Name()] > 0`.
- `internal/transaction/identity.go`: `block_timestamp` now `*time.Time`
  → unknown upstream timestamps persist NULL, never year-1/epoch.
- Migrations 000009–000013 (alchemy, blockscout, ankr, covalent,
  moralis `data_sources` rows; up+down verified via `make migrate-test`).

## Docs-first findings (conflicts surfaced)

- **Alchemy**: `withMetadata.blockTimestamp` documented ETH/L2-only → BSC
  requests omit it; BSC rows carry zero timestamp → NULL in DB.
- **Ankr**: `params` is an OBJECT, not positional array (pinned in test);
  unified list → single `tx` stream; `outOfSync` → `degraded`.
- **Blockscout**: newest-first, no block-range params → pinned
  drain/buffer model implemented verbatim (deterministic key, 10
  pages/call, `floor < fetchFrom` coverage, head catch-up, 30m TTL +
  LRU + 20k-row cap → `internal` error kind on overflow).
- **Covalent**: earliest-first pages → honest ascending prefix;
  process-local page cursor preserves cross-refresh progress.
- **Moralis**: two bounded endpoints (native + erc20/transfers) with
  server-side `from_block`/`to_block` → kind-partitioned streams.

## Deviations

- Phase-01 commit `b9d3a7c` also swept previously-untracked plan
  workspace artifacts (prompt-critic reports) into the same commit —
  content is all plan-scoped; noted for honesty, not rewritten.
- covalent/moralis/etc. share the resilience template; covalent adds a
  process-local ascending scan cursor (memory-only, LRU+TTL) — a
  bounded variant of blockscout's pinned model, documented in-package.

## Residual risks / pending work

- Phases 06–12 outstanding: tatum adapter, 5 tier-2 viability probes,
  the phase-12 design doc (D1–D5) + schema re-key
  `(network_id, wallet_id, source_id)` + ordered failover chain.
- Blocked adapters remain inert until keys arrive; post-key checklist in
  plan.md tracking table.
- Blockscout deep-history wallets progress in bounded drains per the
  phase-02 known-limitation note (honest permanent PARTIAL possible).

---

# Continuation — phases 06–12 (second Cook session)

## Phase outcomes

| Phase | Adapter | Commit | Outcome | Tests |
|---|---|---|---|---|
| 06 | tatum | `782e1d7` | implemented — live-verify BLOCKED (no key) | 6 green |
| 07 | bitquery | `df169e2` | probe 401 → implemented merge-unverified | 6 green |
| 08 | oklink | `b71b0bd` | probe 401 → implemented merge-unverified | 6 green |
| 09 | blockchair | `71612b8` | probe FAILED → **NOT-VIABLE** | viability report |
| 10 | tokenview | `3631dc7` | probe 401 → implemented merge-unverified | 5 green |
| 11 | chainbase | `3f673de` | probe 401 → implemented merge-unverified | 6 green |
| 12 | failover chain | `7e9e730` | design + re-key migration + chain walk | 10 failover tests + full suite green |

## Docs-first findings (phases 06–12)

- **Tatum**: `GET /v4/data/transaction/history`; pagination is only
  stable *within one `transactionTypes` value* → kind-partitioned
  streams (`native` + `fungible`); amounts are normalized decimals →
  native ×10^18 exact via `big.Rat`, fungible `AmountRaw=""`;
  `zero-transfer` subtype is direction-ambiguous → skipped. Head via
  same-host JSON-RPC `eth_blockNumber`.
- **Bitquery v2**: GraphQL POST; `Currency.Native` partitions streams
  honestly; deterministic multi-key ordering + `limit/offset`;
  `Currency.Decimals` per row → exact raw conversion both streams.
- **OKLink**: one endpoint both kinds (`protocolType`); native list
  field `transactionLists` vs token `transactionList` — mapper accepts
  both; page fields are strings; token decimals underivable → `""`.
- **Blockchair**: `/ethereum/stats` live (200) but both BSC slugs 404;
  documented chain list excludes BSC → NOT-VIABLE
  (`reports/blockchair-viability.md`).
- **Tokenview**: newest-first shared list; `tokenTransfer[]` embedded
  per row with raw values; `tokentx` endpoint is single-block-only
  (`endblock=startblock+1`) → unusable; hard 50-page cap → artificial
  ceiling = unfinished streams, no continuation token, no false
  coverage claim.
- **Chainbase**: independent native/token endpoints with server-side
  `from_block`/`to_block`; ordering undocumented → buffer per stream,
  emit ascending on stream exhaustion; token rows without
  `contract_address` skipped (never invent asset IDs).
- **Phase 12**: `reports/phase-12-design.md` — D1 schema re-key
  (pre-decided a), D2 shared pool sized max-of-chain + per-adapter cap,
  D3 chain walk inside `ensure()` (rejected `provider.Chain` wrapper —
  per-source attribution is a service concern), D4 `timeout` excluded
  (spec-faithful, ambiguity rationale), D5 re-key ripple enumeration +
  union-of-slots dedup semantics.

## Phase 12 mechanics (landed)

- Migration `000019`: `wallet_index_state` PK → `(network_id,
  wallet_id, source_id)`, `source_id` NOT NULL; up+down verified.
  Cross-source `ResetWalletIndexSource` deleted — per-source rows make
  watermark isolation structural.
- `transaction.New` takes `map[string][]provider.TxProvider`;
  `selectTxProviderChain` in main.go resolves comma selectors;
  unset → single historical default (byte-identical); empty entries
  and unknown names fail boot.
- Chain walk: per-attempt `srcID` + per-source state row + per-source
  gates; failover kinds write FAILED on the attempt's own row then
  continue; non-failover kinds return immediately; success/bound-stop
  is terminal; all-disabled → honest disabled state; all-failed → last
  attempted source's FAILED row.
- Index claim (`MarkWalletIndexing`) moved BEFORE first budget spend
  with an `Exhausted()` pre-check; raced-drain and head-lag paths
  release the claim (`not_indexed`) — preserves never-attempted
  semantics and leaves no stale `indexing` rows.
- `failover_test.go`: 10 tests — primary-healthy-no-secondary-traffic,
  rate-limit failover, partial-batch discard, 4xx never walks,
  disabled skip, all-disabled, all-fail, empty-page terminal,
  per-source watermark resume, budget semantics.
- Glimpse regression fixed: the pre-failover stall mechanism (locked
  index-state row) now targets the wallet's per-source row, which the
  moved index claim blocks on — emitted-prefix semantics preserved.

## Test counts (this session)

- New adapter suites: tatum 6, bitquery 6, oklink 6, tokenview 5,
  chainbase 6 — all parse real wire shapes.
- Failover suite: 10 tests.
- Full `go test ./...` green; `gofmt -l .` empty; `go vet` clean;
  `go build` clean. Migrations 000014–000019 each verified up+down on
  dedicated test DBs.

## Deviations (phases 06–12)

- Tokenview cap test takes ~24.5s (resilience backoff pacing on 51
  pages) — real pacing, not a defect.
- Tokenview exhaustion test corrected mid-phase: page-1 floor ≥
  fetchFrom requires a second page fetch to *prove* exhaustion →
  `UpstreamCalls=2` is correct, the initial expectation was wrong.
- `writeDisabled` records state under chain[0]'s `source_id` and
  skips persistence entirely if no source resolves — never writes a
  NULL-source row post-rekey.

## Residual risks / pending work

- 10 keyed adapters remain live-verify BLOCKED until keys arrive
  (plan.md tracking table); blockchair is permanently NOT-VIABLE for
  BSC — remove it from any future selector documentation.
- Down-migration 000019 collapses per-source history to the freshest
  row per pair — documented, one-way-lossy by design.
- Divergent `transfer_index` slots across sources persist as bounded
  residue (D5 union semantics) — accepted; per-row source_id is a
  future additive option, not required for correctness.
- Tatum doc drift noted: OpenAPI shows singular `transaction` while
  some pages show plural — implementation pins the OpenAPI path.
