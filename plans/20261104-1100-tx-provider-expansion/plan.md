# Plan: Transaction-History Provider Expansion (11 adapters + quota failover)

> Source spec: `docs/prompts/20261104-1050-claude-code-bsc-tx-providers.md`
> Mode: codex-vibe --auto | Status: in-progress | Created: 2026-11-04

## Overview

Add up to eleven new `provider.TxProvider` adapters for Ethereum + BSC
transaction-history indexing, then an ordered per-network failover chain
so free-tier quota is shared across providers. Tier-1 providers have a
proven contract shape; Tier-2 are VERIFY-FIRST candidates that are only
implemented if a live probe proves the free endpoint delivers BOTH
native and token transfers with pagination.

Goal: eliminate the single-provider dependency (chainscan-only ETH,
nodereal-only BSC) and spread quota across free sources. Non-goal:
automatic round-robin load balancing, paid providers, new chain
support.

## Roadmap

| Phase | Title | Priority | Depends | Live-verify |
|-------|-------|----------|---------|-------------|
| 01 | alchemy adapter (bsc+eth) | P0 | — | DONE — live-verify BLOCKED (no key) |
| 02 | blockscout adapter (eth, keyless) | P0 | 01 | DONE — live-verified |
| 03 | ankr adapter (bsc+eth) | P1 | 02 | DONE — live-verify BLOCKED (no key) |
| 04 | covalent adapter (bsc) | P1 | 03 | DONE — live-verify BLOCKED (no key) |
| 05 | moralis adapter (bsc) | P1 | 04 | BLOCKED (no key) |
| 06 | tatum adapter (bsc) | P1 | 05 | BLOCKED (no key) |
| 07 | bitquery verify-first (bsc) | P2 | 06 | probe required |
| 08 | oklink verify-first (bsc) | P2 | 07 | probe required |
| 09 | blockchair verify-first (bsc) | P2 | 08 | probe required (keyless probe possible) |
| 10 | tokenview verify-first (eth+bsc) | P2 | 09 | probe required |
| 11 | chainbase verify-first (eth+bsc) | P2 | 10 | probe required |
| 12 | quota-sharing failover chain | P1 | 01–11 | integration |

Phases serialize 01→11 (shared file surfaces: config.go / main.go /
.env.example / migration-number space). `Depends` column is authoritative
and mirrors each phase file's frontmatter `dependencies`.

Tier-2 phases may legitimately complete as `NOT-VIABLE` with a written
reason — that is a completed phase, not a failure.

## Architecture

Per-network adapter selection: extend `adapters map` wiring in
`cmd/api/main.go` with env selectors `TX_PROVIDER_BSC` /
`TX_PROVIDER_ETH` (comma list, first = primary; default = current
single adapter so nothing changes when unset). Phase 12 upgrades the
map value to `[]provider.TxProvider` and adds failover on provider-
level failure kinds only (rate_limited, upstream_5xx, degraded,
transport, circuit_open — never on empty pages, client_4xx, malformed).

Each adapter: `internal/provider/<name>/{adapter.go,mapper.go,
adapter_test.go}` implementing TxProvider, resilience.NewClient with
hardcoded SSRF allowlist, `data_sources` row via new migration,
`<NAME>_API_KEY` + `<NAME>_BASE_URL` in config (keyless providers exempt
from key gating), Budget map entry, .env.example placeholder.

## Unverified-adapter tracking (critic R1 Finding 6)

Only ETHERSCAN+NODEREAL keys exist today; ~10 adapters will merge with
live-verify BLOCKED. Blocked adapters MUST be absent from every DEFAULT `TX_PROVIDER_*`
selector (inert until verified; the config still accepts an explicit
operator override — this is a defaults policy, not a lockout). Post-key verification checklist — run live-verify then remove
from this table:

| Adapter | Verified | Adapter | Verified |
|---|---|---|---|
| alchemy | BLOCKED (no key) | tatum | pending-key |
| blockscout | VERIFIED | bitquery | pending-key |
| ankr | BLOCKED (no key) | oklink | pending-key |
| covalent | BLOCKED (no key) | blockchair | keyless-probe ok, key verify pending |
| moralis | pending-key | tokenview | pending-key |
| chainbase | pending-key | | |

## Validate-gate user decisions (this session)

- **Scope**: all 11 adapters implemented now (confirmed; keyed adapters
  merge unverified + tracked).
- **Tier-2**: all 5 included; viability probes still mandatory first.
- **D1 pre-decision**: option (a) schema re-key `(network,wallet,
  source_id)` — per-source watermarks; phase-12 no longer halts on D1.
- **Cook flow**: continuous across phases 01–11 — per-adapter commits
  still required (`feat(provider): add <name>`), but NO per-adapter
  stop/report checkpoint; single consolidated report at the end.

## Minor decisions (critic R1)

- **Comma list pre-phase-12**: `TX_PROVIDER_<NET>` accepts a comma list
  immediately; pre-phase-12 only the first entry is used, extras are
  validated-then-ignored with a startup warning (keeps env shape stable
  for phase-12).
- **Net-code mapping**: `TX_PROVIDER_ETH` → `ethereum`,
  `TX_PROVIDER_BSC` → `bsc` (explicit map in main.go; unknown selector
  names are a config error).
- **`timeout` exclusion**: spec-faithful; request-deadline expiry is
  indistinguishable from upstream slowness at the boundary — full
  rationale in phase-12 D4.
- **WindowCap**: `IndexingWindowCapBSC` encodes nodereal's span limit but
  applies service-wide. Keep as network-level safety bound; each adapter
  additionally enforces its OWN upstream span cap internally.
- **Sequential phases**: phases 01→11 serialize (shared file surfaces:
  config.go/main.go/.env.example/migrations). dependencies updated.
- **Name()↔code pin**: every phase adds a wiring test or boot check that
  registered `Name()` matches its `data_sources.code` — a typo currently
  disables the provider-switch reset silently (Finding 7).

## Invariants (never violate)

- disabled ≠ failed ≠ ok; provider failure never becomes a clean result
- per-stream Finished semantics + ascending block order per stream
- UpstreamCalls counts real outbound requests (request-global budget)
- per-source watermarks DO NOT exist today (one source_id per row) — phase-12 sign-off (D1) decides real semantics; never claim isolation the schema can't express
- unknown ≠ safe; coverage metadata stays truthful
- mocks must parse the real wire shape (the -32700 lesson)
- no secrets/keys in code, logs, tests, commits; .env.example only

## Risk matrix

| Risk | Mitigation |
|------|-----------|
| Upstream contract drift (the -32700 class) | Verify docs first; mock parses real wire shape; live-verify gate or explicit BLOCKED |
| Tier-2 endpoint can't deliver native+token | Probe before coding → NOT-VIABLE report |
| Failover hides a real bug behind a retry | Failover only on provider-level kinds; malformed/4xx propagate |
| Cross-provider watermark poisoning / failover ping-pong wipes coverage | phase-12 D1 sign-off picks schema change vs sticky-writer vs bounded reset-per-flap BEFORE code |
| Pagination/token semantic mismatch | Per-stream opaque tokens; empty = finished; tests pin continuation |
| Rate-limit self-harm | Conservative per-adapter client rates like nodereal (~0.6 rps) |
| 10 adapters BLOCKED on keys | Code+mock+gates green; live-verify marked BLOCKED honestly |
