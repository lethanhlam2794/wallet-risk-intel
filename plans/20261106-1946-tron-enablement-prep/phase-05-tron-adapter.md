---
phase: 5
title: "Dormant TRON Adapter — provider.TxProvider over the Phase-2 winner"
status: completed
priority: P1
dependencies: [2, 3]
---

# Phase 5: Dormant TRON Adapter

## Overview

Implement `internal/provider/<chosen>/` (provisional: `trongrid`) —
a `provider.TxProvider` adapter satisfying every contract the indexer
verified in Phase 1 and every semantic rule Phase 3 documented. It must
**never emit traffic** until explicitly configured AND serving an active
network; it is NOT wired into any provider selector (stop condition).

## Requirements

- Functional: `Name()`, `Enabled(net)`, `HeadBlock(ctx, net)`,
  `LookupTransfers(ctx, net, normalized, opts)` per
  `internal/provider/provider.go:279-288`.
- Non-functional: timeout, concurrency limit, rate limit, bounded retry
  honoring `Retry-After`, circuit breaker, SSRF allowlist — all via the
  shared `resilience.Client` (AGENTS.md §7).

## Architecture

### Construction + gating (mirror chainscan)

```go
func New(baseURL, apiKey string, enabledChains []string, opts ...Option) (*Adapter, error)
func (a *Adapter) Enabled(net network.Network) bool
```

- `Enabled(net)`: exact gating per the Phase-2 decision doc
  (expected: `chains[strings.ToLower(net.Code)]` AND apiKey present —
  decide keyless-allowed vs key-required per verified quota evidence;
  default conservative: require key if docs show keyless quotas are
  unfit for sustained indexing).
- **Enabled() layering (critic RA-5):** `network.Network` has no
  `IsActive` field — the adapter CANNOT check network activeness; that
  gate is the resolver's (inactive networks never resolve). The prompt's
  "zero calls when network inactive" criterion is proven by
  `tests/non_evm_networks_test.go`, not the adapter — the adapter proves
  only the config gate. State this explicitly in the package doc.
- Disabled `Enabled()` ⇒ `HeadBlock`/`LookupTransfers` return
  `provider.Errorf(ErrDisabled, …)` **before** any HTTP — a test with an
  httptest server + request counter MUST prove 0 requests.
- Resilience config values: follow chainscan's shape (`Timeout ~8s`,
  conservative `Rate`, `MaxRetries 2`, `BreakerFailures 5`, bounded
  `MaxBodyBytes`), tuned to the Phase-2 verified rate limit.
- `defaultAllowedHosts`: the provider's API host(s) only;
  `init() { provider.RegisterAllowedHosts(SourceCode, …) }` per
  chainscan convention (`adapter.go:383`).
- `SourceCode`: the `data_sources` registry key (e.g. `"trongrid"`) —
  the row itself is an enablement task, NOT created here.
- Compile-time contract assertion (critic RA-9):
  `var _ provider.TxProvider = (*Adapter)(nil)` — cheapest drift armor
  for a package nothing else imports.
- Package doc states: dormant by design under the §15 gate (unwired —
  never imported by cmd binaries, so `init()` allowlist registration
  never fires in prod), the contract revision it was written against
  (HEAD SHA or section refs), and the Enabled() layering note above.

### HeadBlock

- Solidified head ONLY (Phase 3 decision): `walletsolidity/getnowblock`
  → `block_header.raw_data.number`. Never the unconfirmed `getnowblock`.
- Malformed/missing fields ⇒ `ErrMalformed`.

### LookupTransfers — two streams + merged page token

- Streams (names per Phase 3): `trx` (native, from
  `/v1/accounts/{a}/transactions`) and `trc20` (from
  `/v1/accounts/{a}/transactions/trc20`).
- `pageToken` struct mirrors chainscan's (`adapter.go:186-218`):
  per-stream `fingerprint` cursors, finished/capped flags, base64-JSON —
  a nil cursor = stream exits the token; empty token = all finished.
  **The token must carry the resolved ms window** so fingerprint
  continuation is identical and boundary blocks aren't re-bridged each
  step (critic RA-4).
- Per step: at most one page per wanted, unfinished stream;
  `Kinds` filter drops a stream early (`Finished=true`) per contract.
- **INV-1 cursor-advance (binding, test-asserted):** a fingerprint may
  only advance past fully-resolved rows; a step that cannot fully
  resolve its trc20 rows ⇒ typed error, cursor held — never
  drop-and-advance.
- **INV-2 stream-declaration (binding, test-asserted):** every
  `Transfer.Stream` value emitted MUST appear in `page.Streams` that
  step — an undeclared stream is a permanent silent data loss
  (`service.go:444-449`).
- **Ascending order within each stream** — contractually required
  (`provider.go:269-271`); use the documented order params
  (`order_by=block_timestamp,asc`) and, if upstream ties are possible,
  a documented tiebreak or explicit invariant check.
- `FromBlock`/`ToBlock` → ms window per the Phase-3 (b1) scheme
  (block-bridge via `walletsolidity/getblockbynum`; each bridge call
  increments `UpstreamCalls`). Guard `FromBlock ≤ ToBlock`; fixture the
  `fetchFrom=0 → getblockbynum(0)` degenerate boundary (critic #12).
- `StreamStatus`: `Finished` when the stream's fingerprint is exhausted
  (no `meta.links.next`); TRC-20 per-row block resolution per the
  Phase-3 (b2) decision — **bounded fan-out**: clamp the trc20 page
  size to a stated bound, dedup txIDs within the step, charge every
  bridge + info call to `UpstreamCalls` (a ~200-call step must not blow
  the post-hoc `settle` budget — the clamp is the bound, critic RA-5).
- `Transfer` mapping per Phase 3: `TxHash` lowercase 64-hex verbatim;
  `Type` NATIVE/TOKEN; `Asset` TRC-20 contract base58; `From`/`To`
  **base58-canonical** (native-stream hex converted in-adapter per
  Phase-3 (d)); `AmountRaw` raw integer decimal string
  (`""` if underivable — never float); `BlockNumber`, `BlockTimestamp`,
  `Confirmations` = `opts.HeadBlock − blockNumber + 1` (0 when unknown);
  `IsError` per the documented `contractRet`/`ret` rule.
- `UpstreamCalls` = real outbound requests this step including bridge +
  fan-out calls — the request-global budget charges against it.
- Typed errors via `provider.Errorf` with the right `ErrKind`; malformed
  JSON ⇒ `ErrMalformed`; upstream error text ⇒ `ErrDegraded`/`ErrClient4xx`
  mirroring chainscan's envelope handling; txinfo-lag misses ⇒ bounded
  in-step retry ⇒ `ErrDegraded` (never block-less rows).

## Related Code Files

- Create: `internal/provider/<chosen>/adapter.go`,
  `internal/provider/<chosen>/mapper.go`,
  `internal/provider/<chosen>/adapter_test.go`
- Read: `internal/provider/chainscan/{adapter,mapper}.go`,
  `internal/provider/provider.go`, `internal/provider/resilience/client.go`,
  `docs/tron-indexer-semantics.md`, `docs/tron-provider-decision.md`
- NOT touched: `cmd/api/main.go`, `cmd/worker/main.go`,
  `internal/providerconfig/`, `internal/config/`, `data_sources` seed —
  the adapter is importable but registered nowhere.

## Implementation Steps (TDD)

1. **Red**: table-driven tests against `httptest.NewServer` +
   `WithAllowedHosts`/`WithInsecureHTTP` (chainscan test conventions):
   - disabled `Enabled()` → `ErrDisabled`, request counter = 0.
   - `HeadBlock` happy path + malformed envelope.
   - `LookupTransfers`: two-stream paging, fingerprint merge in
     `NextPageToken` (resolved window carried in token — second step
     issues ZERO bridge calls), `Kinds` filtering, ascending order
     assertion, per-stream `Finished`.
   - `UpstreamCalls` exact-count fixture: head + pages + bridge +
     fan-out = N (critic RA-9).
   - INV-1 fixture: a trc20 page whose txinfo resolution fails after
     bounded retries ⇒ typed error AND cursor unchanged (no advance).
   - INV-2 fixture: every emitted `Transfer.Stream` present in
     `page.Streams` each step.
   - `walletsolidity/gettransactioninfobyid` stub incl. blockNumber
     presence + a persistent-miss ⇒ `ErrDegraded` case;
     `getblockbynum` boundary fixtures incl. failure + `num:0`.
   - Replay safety: identical fixture window → identical `Transfer` set
     (incl. duplicate same-content events in one tx → `.k` suffix).
   - Typed errors: 429 → `ErrRateLimited` w/ Retry-After; 5xx →
     `ErrUpstream5xx`; bad JSON → `ErrMalformed`; unallowlisted host →
     error before socket.
   - TRC-20 row with no block number → mapped per Phase-3 rule (assert
     the documented field, never a silently-zeroed block).
2. **Green**: implement adapter + mapper.
3. **Refactor**: share nothing across adapters (each keeps its quirks);
   gofmt; `go vet`.

## Success Criteria

- [x] `Enabled()=false` ⇒ zero HTTP requests (counter-asserted test)
- [x] Same window refetch ⇒ identical transfer sets (replay-safe test)
- [x] Ascending within-stream order + per-stream `Finished` asserted
- [x] INV-1 (no cursor advance past unresolved) + INV-2 (stream
      declaration) test-asserted
- [x] `UpstreamCalls` exact-count asserted incl. bridge + fan-out
- [x] trc20 page clamp bound stated; txID dedup within step
- [x] `var _ provider.TxProvider = (*Adapter)(nil)` present
- [x] `AmountRaw` always raw integer decimal (property-style check on
      fixtures)
- [x] Typed `LookupError` kinds for 429/5xx/malformed/disabled/
      txinfo-lag→degraded
- [x] No wiring into selectors/registers — adapter compiles standalone;
      package doc states dormant-by-§15 + contract revision + Enabled()
      layering
- [x] `go build ./...`, `go test ./internal/provider/<chosen>/` green

## Risk Assessment

- The TRC-20 missing-block gap is the riskiest mapping — if Phase 3 chose
  bridge/fan-out, assert the extra calls in `UpstreamCalls` and bound the
  fan-out by page size.
- TronGrid free limits may throttle tests — tests must never hit the real
  network; live probing stays in Phase 2/3.
- A dormant adapter invites "dead code" critique — acceptable by design
  (§15 gate); the package doc comment must state that explicitly.
