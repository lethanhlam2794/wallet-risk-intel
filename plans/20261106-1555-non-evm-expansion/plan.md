---
title: "Non-EVM Expansion Groundwork — Solana + TRON (address formats, inactive registry, audit)"
status: completed
priority: P1
created: 2026-11-06
source_prompt: docs/prompts/20261104-1055-claude-code-non-evm-expansion.md
blockedBy: []
---

# Plan: Non-EVM Expansion Groundwork (Solana + TRON)

## 1. Overview

Prepare the codebase for future Solana and TRON support **without enabling any
live traffic path** (AGENTS.md §15 gate). Scope is strictly groundwork:

1. An `AddressFormat` abstraction on the network model (`evm | solana | tron`)
   with pure-function validators and a dispatching `wallet.Normalize`.
2. Two **INACTIVE** `blockchain_networks` rows (solana, tron) — the resolver
   must keep rejecting them exactly as today.
3. An OpenAPI contract update marking the new codes as registered-but-unavailable.
4. Two findings documents: a per-module compatibility audit and a provider
   discovery brief (the TxProvider slot stays PENDING DISCOVERY — no adapter
   code anywhere).

Explicit non-goals: no TxProvider/SnapshotSource adapter code, no ingestion
changes, no transactions/tracing/scoring/screening behavior changes, no schema
redesign beyond the two network rows, no paid sources, no behavior change to
existing ethereum/bsc flows.

## 2. Grounded scouting summary (verified against code, 2026-11-06)

| Fact | Evidence |
|---|---|
| `blockchain_networks` already has `is_active BOOLEAN NOT NULL DEFAULT TRUE` | `apps/api/migrations/000001_init.up.sql` L4-12 |
| Resolver loads only `is_active = TRUE` rows → inactive rows auto-reject with `ErrInvalidNetwork` → HTTP 400 | `apps/api/queries/networks.sql`; `apps/api/internal/network/resolver.go` L48-75; `apps/api/internal/httpserver/server.go` L260-268 |
| `network.Network` has `Namespace`+`ChainReference` (CAIP-2) but **no** format field | `apps/api/internal/network/resolver.go` L11-17 |
| `wallet.ResolveAndNormalize` unconditionally calls `NormalizeEVM` — the single dispatch point used by all `{network}/{address}` endpoints | `apps/api/internal/wallet/address.go` L55-65 |
| Direct `NormalizeEVM` callers (EVM-correct, stay): `transaction/identity.go`, `transaction/list.go`, `provider/ofac/mapper.go`, `provider/walletattribution/mapper.go`, `provider/stablefreeze/scanner.go`, `ingestion/pipeline_freeze.go`, `warmindex/queue.go` | grep |
| `address_type` literal `'evm'` is hardcoded in 4 SQL queries + `wallet/service.go` (`GetOrCreate` lacks a format param) — audit finding | `queries/{transactions,ofac_publish,labels_publish,stablecoin_freeze}.sql`; `internal/wallet/service.go` L25. NOTE: `queries/wallets.sql` is NOT a literal site — `address_type` arrives as `$4` |
| `transactions`/`wallet_index_state` use generic `tx_hash VARCHAR(255)`, `block_number BIGINT` — fits base58 sigs/slots but semantics differ — audit finding | `migrations/000008_transactions.up.sql` |
| `TxProvider` contract is block-number-based (`HeadBlock`, `FromBlock/ToBlock`, ascending-block ordering) — audit finding for slot-based Solana | `internal/provider/provider.go` L242-288 |
| OFAC `resolveListing` returns `("", CHAIN_SPECIFIC)` for non-EVM formats — TRX/SOL mapping gap is documented behavior to extend later | `internal/provider/ofac/mapper.go` L32-44 |
| OpenAPI network enums: `parameters.Network` `[ethereum, bsc]` (L1628) + probe body enum (L1337); `/networks` lists active only; **`Address` param has hard `pattern: "^(0x)?[0-9a-fA-F]{40}$"` (L1636)** — EVM-only schema constraint | `api/openapi.yaml` |
| **`bn.code` joins lack `is_active` filters** — `StgLabelOrphanNetworks`, `PublishEnsureLabelWallets`, `PublishUpsertWalletLabels`, `PublishRevokeAbsentWalletLabels`, `CountLiveLabelsForStagedNetworks` all match `bn.code = s.network_code` unfiltered: post-000026 a staged `solana`/`tron` row joins the inactive row → publishes `'0x'+lower(base58)` garbage wallets — and `wallets.network_id` FK then breaks the down-migration | `queries/labels_staging.sql` L19-21,L43-47; `queries/labels_publish.sql` L15,L60,L100 |
| `testutil/testdb_integration_test.go` L23 asserts `count(*) == 2` networks — **guaranteed break** on 000026; must be updated to the new seed set | `internal/testutil/testdb_integration_test.go` L23 |
| Test convention: seeded `math/rand` property tests, `propIterations = 5000` | `internal/wallet/address_property_test.go` |
| No base58 library in `go.mod`; `math/big` + `crypto/sha256` (stdlib) suffice | `apps/api/go.mod` |
| CAIP-2 refs confirmed: `solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp`, `tron:0x2b6653dc` | CAIP-2 registry / public docs |
| Dev stack running: postgres 127.0.0.1:5433, redis :6380, sqlc v1.30.0 installed | `docker compose ps` |
| Latest migration: `000025_alchemy_split` → next is `000026` | `apps/api/migrations/` |

## 3. Design decisions (proposed for debate)

- **D1 — `AddressFormat` derived from `namespace`, not a new column**
  (type name per the source prompt's mandated vocabulary — `AddressFormat`,
  not `Format`). `eip155→evm`, `solana→solana`, `tron→tron`, else `""`
  (unknown → `ErrInvalidAddress`, fail-closed). `namespace` is already the
  CAIP-2 discriminator; a column could drift (`namespace='eip155'` +
  `format='solana'` would be a lie). Known limitation recorded in the audit
  doc: namespace↔format is not universally 1:1 (e.g. `bip122` covers
  multiple encodings) — fails closed at exactly the §15-gated moment.
  Implemented as `Network.AddressFormat()` (method, zero-value-safe).
- **D2 — Validators are pure functions in `internal/wallet/`**
  (`address_solana.go`, `address_tron.go`, `base58.go`): solana = base58,
  decoded exactly 32 bytes, 32–44 chars; tron = base58check, decoded 25 bytes
  (`0x41` + 20-byte payload + 4-byte double-SHA256 checksum verified).
  Normalized form = input **as-is** (base58 alphabet is case-sensitive —
  never lowercase).
- **D3 — `wallet.Normalize(net, raw)` dispatches on `net.AddressFormat()`**;
  `ResolveAndNormalize` routes through it. Error shape unchanged
  (`ErrInvalidAddress`/`ErrInvalidNetwork` → HTTP 400 same codes).
- **D4 — Migration `000026_non_evm_networks`** inserts solana + tron rows
  `is_active = FALSE`; resolver rejection is a *query-level* consequence
  (`WHERE is_active = TRUE`), proven by a `tests/` integration test and a
  booted-API 400 in Stage 5. Down migration deletes by code guarded on
  `is_active = FALSE` (never reverts a future enablement; FK to `wallets`
  has no cascade — rows must never gain wallet references while inactive).
  `ON CONFLICT DO NOTHING` (no target — silently skips collisions on either
  unique; acceptable for fixed seed values, and the integration test asserts
  the exact row contents so drift is caught downstream).
- **D4b — SQL-layer `is_active` predicates (post-debate amendment).**
  "Resolver unreachable" holds at the app layer, but five `bn.code` joins
  in `labels_staging`/`labels_publish` bypass `is_active` — after 000026 a
  staged `solana`/`tron` `network_code` would join → publish → create
  `wallets` rows normalized as `'0x'+lower(base58)` garbage under an
  inactive chain, and those `wallets` FK references would break the
  down-migration. Fix: add `AND bn.is_active` to the five code-keyed
  sites — semantics-preserving (for every code that could match today the
  predicate is already true; inactive codes keep today's
  orphan/rejected semantics) + a test staging `network_code='solana'`
  asserting it stays orphan-rejected.
- **D5 — OpenAPI**: extend `parameters.Network.schema.enum` (and the probe
  request-body enum) to `[ethereum, bsc, solana, tron]` with a description
  marking solana/tron **registered but INACTIVE — returns `INVALID_NETWORK`
  until enabled**, plus an example; the marking text must explicitly state
  the sibling `Address` parameter's `pattern` remains EVM-only — the enum
  admits codes no request can currently exercise, so the unexercisable
  combination is made explicit rather than implicit. `/networks` keeps
  listing active only.
- **D6 — Docs at repo-root `docs/`** (`non-evm-audit.md`,
  `non-evm-provider-discovery.md`) — spec-adjacent deliverables named by the
  task; pipeline artifacts stay under `apps/api/docs/`.

## 4. Phase roadmap

| # | Phase | File | Depends |
|---|---|---|---|
| 1 | AddressFormat + non-EVM validators + `Normalize` dispatch | `phase-01-address-format.md` | — |
| 2 | Inactive solana/tron registration (migration 000026 + rejection proof) | `phase-02-inactive-networks.md` | 1 |
| 3 | OpenAPI contract marking (enums + unavailable note + example) | `phase-03-openapi-contract.md` | 2 |
| 4 | Compatibility audit + provider-discovery docs + gates + commits | `phase-04-audit-docs.md` | 1–3 |

## 5. Risk matrix

| Risk | Mitigation |
|---|---|
| Solana/TRON validator bugs (base58check edge cases: leading-zero bytes, wrong checksum) | Property tests (5000 iters, seeded) with independent encode/verify path; table tests against known mainnet vectors |
| Inactive rows leaking into resolver | `ListActiveNetworks` already filters; integration test asserts `List()` excludes solana/tron and `Resolve("solana")` fails |
| **Orphan-check drift (D4b)**: `bn.code` joins ignore `is_active` → staged solana/tron labels would publish garbage wallets + break down-migration FK | `AND bn.is_active` predicates on all five code-keyed sites + staged-`solana` orphan-rejection test |
| **Seeded-count assertion break**: `testdb_integration_test.go` L23 expects 2 networks | Enumerated update to `{ethereum,bsc,solana,tron}` code set assertion (spec change, not weakened test — commit msg justifies) |
| **Contract contradiction**: `Address` `pattern` is EVM-only while `Network` enum admits solana/tron | Marking text explicitly documents the pattern constraint as pending-enablement |
| OpenAPI enum addition misleading partner clients | Explicit "unavailable — INVALID_NETWORK until enabled" marking + example; runtime still rejects (fail-closed) |
| Lowercasing base58 in any future path | `Normalize` keeps solana/tron input verbatim; tests assert case preservation |
| Scope creep into adapters/schema redesign | Task stop-conditions encoded in phase files; §15 review listed as blocker in final report |

## 6. Stage 1 debate summary

**Devin Critic session**: `defiant-summer` (round 1, `smart` permission
mode — `auto` rejected read-only exec calls in non-interactive mode;
downgrade recorded).

**Verdict**: DONE_WITH_CONCERNS — all 4 phase files present, core claims
verified; 6 required amendments, all **accepted and applied**:

1. ✅ `bn.code` joins ignore `is_active` (Blind Spot #1) — real blast
   radius; added D4b predicates + orphan-rejection test to Phase 2.
   Orchestrator concession: the "unreachable by construction" claim held
   only at the app layer; Critic's SQL-layer counter-evidence verified at
   `labels_staging.sql`/`labels_publish.sql`.
2. ✅ `testdb_integration_test.go:23` count==2 assertion — enumerated as a
   Phase-2 change (verified at `internal/testutil/testdb_integration_test.go`).
3. ✅ `'evm'` literal census corrected — `queries/wallets.sql` is a `$4`
   param site, not a literal; real blocker is `Service.GetOrCreate`
   signature (`service.go:25`). Audit doc fixed.
4. ✅ OpenAPI `Address` `pattern` regex contradiction — marking text must
   document the EVM-only pattern explicitly (Phase 3 amended).
5. ✅ Naming reconciled to `AddressFormat` (prompt vocabulary).
6. ✅ Risk matrix extended with all three findings.

**Positive confirmations recorded by Critic**: 5 existing API tests
already use `solana` as expected-invalid network (free regression
coverage); `providerconfig` `enabled_chains` validates against
resolver-derived codes; `000024` CROSS JOIN is `WHERE bn.code='ethereum'`
(benign); OFAC staging resolves via resolver-derived map → TRX/SOL stay
`CHAIN_SPECIFIC`/`network_id NULL` (invariant §5.6 preserved).

## 7. Exit criteria (definition of done)

- `AddressFormat` + solana/tron validators + `Normalize` dispatch; property
  tests green; `ResolveAndNormalize` behavior unchanged for ethereum/bsc.
- Migration 000026 applies cleanly **up and down** on the dev database.
- solana/tron rejected by resolver — proven by `tests/` integration test and
  a booted-API `400 INVALID_NETWORK` (Stage 5 curl evidence).
- `docs/non-evm-audit.md` + `docs/non-evm-provider-discovery.md` exist with
  concrete file references and the UNCONFIRMED candidate table.
- `api/openapi.yaml` enums updated + marked unavailable with example.
- Gates: `gofmt -l .` empty, `go vet ./...` clean, `go build ./...` clean,
  `go test ./...` green.
- Two commits: `feat(network): groundwork for solana and tron chain support`,
  `docs(spec): non-evm expansion audit and provider discovery`.
- Final report lists what remains blocked pending §15 review and provider
  discovery.
