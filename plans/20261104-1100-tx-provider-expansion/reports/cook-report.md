# Cook Report — tx-provider expansion (phases 01–12)

Status at this checkpoint: **phases 01–05 implemented, tested, and committed;
phases 06–12 not yet started** (context budget exhausted mid-run — execution
continues sequentially from phase 06).

## Per-phase outcomes

| Phase | Adapter | Outcome | Live-verify |
|---|---|---|---|
| 01 | alchemy (eth+bsc) | implemented | BLOCKED (no ALCHEMY_API_KEY) |
| 02 | blockscout (eth, keyless) | implemented | **VERIFIED** |
| 03 | ankr (eth+bsc) | implemented | BLOCKED (no ANKR_API_KEY) |
| 04 | covalent (bsc) | implemented | BLOCKED (no COVALENT_API_KEY) |
| 05 | moralis (bsc) | implemented | BLOCKED (no MORALIS_API_KEY) |
| 06 | tatum (bsc) | not started | — |
| 07 | bitquery probe | not started | — |
| 08 | oklink probe | not started | — |
| 09 | blockchair probe | not started | — |
| 10 | tokenview probe | not started | — |
| 11 | chainbase probe | not started | — |
| 12 | failover chain | not started | — |

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
