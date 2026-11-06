---
phase: 4
title: "Compatibility audit + provider-discovery docs + verification gates + commits"
status: completed
priority: P1
dependencies: [1, 2, 3]
---

# Phase 4: Audit document, provider-discovery document, gates, commits

## Overview

Produce the two findings documents required by the task —
`docs/non-evm-audit.md` (per-module blockers with concrete file references)
and `docs/non-evm-provider-discovery.md` (TxProvider contract requirements +
UNCONFIRMED candidate table) — then run all gates and create the two
required commits. This phase writes **documentation only** — no tracing,
scoring, screening, or ingestion code changes.

## Requirements

### `docs/non-evm-audit.md` — concrete blockers per module, with file refs

- **transactions schema** (`migrations/000008_transactions.up.sql`):
  `tx_hash` — solana signatures are ~88-char base58 (fits VARCHAR(255)),
  tron is 64-hex (fits); `block_number BIGINT` vs Solana **slots** (fits,
  but semantic rename/interpretation needed); `transfer_index` identity
  scheme for SPL inner instructions / TRC-20 log entries is undefined;
  `UNIQUE(network_id, tx_hash, transfer_index)` holds.
- **wallet_index_state** (`000008` L47-77): `indexed_until_block` is a
  block watermark — slot vs block semantics differ; Solana has
  commitment-level finality instead of confirmations-count.
- **provider.TxProvider** (`internal/provider/provider.go` L242-288):
  `HeadBlock`/`FromBlock`/`ToBlock` block-window model; ascending-block
  ordering contract; `NATIVE`/`TOKEN` kinds vs SPL/TRC-20 typing; tx_hash
  hex assumption in comments/adapters.
- **tracing** (`internal/tracing/`): BFS keys off `network_id` +
  `normalized_address`; disjoint alphabets across chains ⇒ no cross-chain
  path can form (structurally safe, but documents the expansion boundary —
  same-literal correlation is a separate feature).
- **sanctions ingestion** (`internal/provider/ofac/mapper.go` L32-44):
  `resolveListing` maps `ETH→ethereum`, `BSC|BNB→bsc`; `TRX`/`SOL` currency
  codes currently fall through to `("", CHAIN_SPECIFIC)` / UNRESOLVED —
  mapping to be defined once chains activate; `normalizeListed` keeps
  non-EVM verbatim — wire to `Normalize` later.
- **scoring/screening**: `address_type` literal `'evm'` hardcoded in
  `queries/transactions.sql` (L27), `queries/ofac_publish.sql` (L54),
  `queries/labels_publish.sql` (L9), `queries/stablecoin_freeze.sql` (L58)
  — and, the real enablement blocker, `internal/wallet/service.go` L25
  where `Service.GetOrCreate` stamps `'evm'` via the `$4` param with no
  format/type argument in its signature. NOTE: `queries/wallets.sql` is
  NOT a literal site — the `address_type` column receives `$4`.
- **labels publish path (post-debate finding, fixed by D4b)**: five
  `bn.code` joins lacked `is_active` predicates — documented here as the
  reason the D4b guards exist and as the pattern to re-check when future
  code-keyed joins are added (`queries/labels_staging.sql`,
  `queries/labels_publish.sql`).
- **harness seed assertion**: `internal/testutil/testdb_integration_test.go`
  asserted `count(*) == 2` networks — updated in Phase 2 to the
  `{bsc, ethereum, solana, tron}` set.
- **warmindex** (`internal/warmindex/queue.go` L46-47): stored-address
  check assumes `NormalizeEVM` form.
- **OpenAPI**: `Address` param description says "EVM address (20-byte hex)"
  — needs per-format generalization at enablement time (not now).
- **networks registry**: `wattrFiles` (`cmd/{api,worker}/main.go`) maps
  `code+".csv"` — inactive networks absent, fine; note for enablement.

### `docs/non-evm-provider-discovery.md`

- Contract requirements a provider MUST meet to become `TxProvider` for
  these chains: per-address transfer history covering **native + token**
  (SPL / TRC-20) with pagination; deterministic ordering usable for
  coverage watermarks (block or slot); a typed head/slot endpoint; free
  tier without a credit card.
- Candidate list — **all UNCONFIRMED, do not code**: Helius Enhanced
  Transactions (Solana), TronGrid (TRON), Tatum multi-chain, Ankr
  multi-chain, Covalent multi-chain.
- Per-candidate evaluation table: free quota, endpoint shape, native
  coverage, token coverage, pagination semantics, block-range limits,
  known quirks.

### Gates & commits

- `gofmt -l .` empty, `go vet ./...` clean, `go build ./...` clean,
  `go test ./...` green (dev DB :5433 for `tests/`).
- Commit 1 (phases 1–3): `feat(network): groundwork for solana and tron
  chain support`.
- Commit 2 (docs): `docs(spec): non-evm expansion audit and provider
  discovery`.

## Related Code Files

- Create: `docs/non-evm-audit.md`
- Create: `docs/non-evm-provider-discovery.md`
- Modify: — (docs only)
- Delete: —

## Implementation Steps

1. Write `docs/non-evm-audit.md` with the per-module blocker list above —
   every claim carries a `file:line` reference verified at write time.
2. Write `docs/non-evm-provider-discovery.md` with the contract
   requirements and the UNCONFIRMED candidate evaluation table.
3. Run all gates; fix any failures.
4. `git add` code+migration+openapi+tests → commit 1; `git add` docs →
   commit 2 (Conventional Commits, no AI references per AGENTS.md §14).
5. Final report: list what remains blocked pending §15 review (chain
   enablement, provider selection, adapter work) and provider discovery.

## Success Criteria

- [x] Both docs exist at repo-root `docs/` with concrete file references
- [x] Audit doc covers: transactions schema, wallet_index_state,
      TxProvider, tracing, sanctions ingestion, scoring/screening,
      warmindex, OpenAPI, networks registry
- [x] Provider doc lists all 5 candidates marked UNCONFIRMED with the
      required evaluation fields
- [x] All four gates green
- [x] Exactly two commits with the required messages; working tree clean
- [x] Report enumerates §15-blocked remainder

## Risk Assessment

- **Inventing findings**: every blocker must cite a verified `file:line`;
  if a claim cannot be cited, it is omitted — honesty over completeness.
- **Doc drift**: docs describe *future* work; mark every candidate and
  proposed change as pending §15 review.
- **Commit hygiene**: ensure `.env`/secrets never staged; verify
  `git status` before each commit.
