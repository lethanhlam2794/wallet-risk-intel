---
phase: 4
title: "Dormant Solana TxProvider adapter"
status: completed
priority: P2
dependencies: [3]
---

# Phase 4: Dormant Adapter — `internal/provider/<chosen>/`

## Overview

Implement the `provider.TxProvider` adapter for the Phase-2-chosen
provider, mirroring `internal/provider/trongrid/`'s dormant shape:
compile-clean, imported by no production binary, `Enabled()` gated so it
can never emit traffic until configured AND the network is active.

## Requirements

- Functional: `Name()`/`Enabled(net)`/`HeadBlock`/`LookupTransfers`
  satisfying `provider.go:279-288`; two streams `sol`+`spl` mapping to
  `TransferPage.Streams` with per-stream `Finished` and merged
  `NextPageToken`; `UpstreamCalls` counts every real outbound request.
- Non-functional: timeout/concurrency/`Retry-After`/backoff/circuit
  breaker via `internal/provider/resilience` (AGENTS.md §7); SSRF
  allowlist + `init()` registration (dormant — never fires in prod).

## Architecture

- Mirror trongrid file layout: `adapter.go` (client, gating, step
  logic), `mapper.go` (row→`Transfer`), `adapter_test.go` +
  `adapter_blind_test.go` fixtures.
- `Enabled(net)`: per decision doc — keyed provider ⇒
  `apiKey != "" && chains[lowercase(code)]` (chainscan pattern,
  `adapter.go:150-152`); keyless public RPC ⇒ `chains[code]`-only plus a
  documented reason the keyless rate surface is still safe to expose.
  Either way `Enabled()=false` ⇒ `ErrDisabled` before any HTTP.
- `HeadBlock` = finalized-commitment slot only (semantics §c).
- `LookupTransfers` implements the Phase-3 bridge: page token carries
  window + desc collection cursor + emission position; emits ascending
  slices only for fully-collected slots; truncation-honesty stream state
  per §f; bounded fan-out per step (page clamp like `trc20PageClamp`)
  honoring `UpstreamCalls` budget semantics (`service.go:339-344`).
- Parsed/enhanced API shapes may be used ONLY where proven equivalent to
  raw-tx evidence (Phase-2/3 equivalence notes) — never emit evidence
  the raw chain cannot reproduce.
- Package doc comment states dormancy explicitly (trongrid header
  pattern); `var _ provider.TxProvider = (*Adapter)(nil)` assertion.

## TDD (Red → Green → Refactor)

Fixtures over httptest — no live calls in unit tests:

- `Enabled()` truth table (no key / no chain / non-solana code) ⇒
  zero upstream calls — httptest request counter proves it.
- Signature-desc → ascending-emission: multi-page window **straddling
  a slot boundary** emits strictly ascending, complete same-slot sets —
  and **cross-step global ascending** (the emitted sequence across steps
  is monotonic, not just within a step — Critic R1).
- Emission gating: no row is emitted before desc collection passes below
  `FromBlock` (contiguous-ascending-prefix invariant) — a test proves a
  mid-collection step emits zero rows rather than a top-anchored slice.
- Refetch-safety: same window twice ⇒ identical `transfer_index` sets
  (determinism under `UNIQUE(network_id, tx_hash, transfer_index)`).
- Per-stream `Finished`: `Kinds={NATIVE}` drops `spl` cursor while still
  declaring the stream (INV-2 analogue); ledger-floor-above-`FromBlock`
  ⇒ `Finished=false` + empty token ⇒ `partial`/`history_truncated` path;
  SPL-coverage-limited design (if chosen) asserts `spl` never lies
  `Finished`.
- **Dual per-step bounds as named constants**: max desc sig-pages
  collected AND max `getTransaction`/fan-out calls per step (trongrid
  `trc20PageClamp` parity — both bounds pinned, Critic R1).
- **Batch `UpstreamCalls` convention**: one JSON-RPC batch HTTP request
  carrying N logical calls — decide and pin which side Budget charges
  (HTTP requests vs logical calls) and assert it in tests.
- `meta.err` txs excluded; fee never emitted; `blockTime` absent ⇒
  `BlockTimestamp` zero (indexer persists NULL — identity.go:192-196);
  malformed payload ⇒ `ErrMalformed`; JSON-RPC `error` ⇒ typed
  `ErrDegraded`/`ErrUpstream5xx`.
- INV-1 analogue: unresolvable mid-step ⇒ no token advance.
- HeadBlock: finalized slot parsed; absent/invalid ⇒ typed error, never
  fabricated.

## Related Code Files

- Create: `apps/api/internal/provider/<chosen>/adapter.go`, `mapper.go`,
  `adapter_test.go`, `adapter_blind_test.go` (+ fixtures).
- Read/mirror: `internal/provider/trongrid/*`, `internal/provider/resilience/`,
  `internal/provider/chainscan/adapter.go`.

## Implementation Steps

1. Write failing tests per matrix above.
2. Scaffold adapter + mapper to green.
3. Assert contracts: ascending, Finished, UpstreamCalls, ErrDisabled
   zero-traffic.
4. `go build ./...` + `go test ./internal/provider/<chosen>/...`.

## Success Criteria

- [x] All prompt acceptance criteria for the adapter pass with tests.
- [x] Zero production-binary imports (dormancy check via `go list`).
- [x] `Enabled` gate, resilience config, allowlist all verified in tests.

## Risk Assessment

Emission-order bugs lie silently about coverage → boundary tests are
mandatory, not optional. Fixture realism — hand-built RPC fixtures must
match probed shapes (Phase-2 transcripts are the fixture source of
truth).
