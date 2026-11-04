---
phase: 05
title: Moralis transaction-history adapter
status: pending
priority: P1
dependencies: [phase-04]
---

# Phase 05: Moralis adapter

## Overview
REST deep-index wallet endpoints: native tx + erc20 transfers for BSC (chain=bsc). Multiple endpoint calls may be needed per lookup — partition streams by the endpoints' real pagination axes (native endpoint vs token endpoint; per-direction only if the upstream itself paginates per direction), never fabricate per-direction streams on a unified list.

## Requirements
- Implement `provider.TxProvider` for `internal/provider/moralis/`.
- Verify CURRENT upstream contract against official docs first; docs win
  over assumptions — surface conflicts in the report.
- Enabled() = false without key (keyless providers: gating by
  enabled-chains list instead) — zero upstream traffic when disabled.

## Architecture
Follow internal/provider/nodereal + internal/provider/chainscan:
- adapter.go: constructor `New(baseURL, apiKey, opts...)`, hardcoded
  SSRF `defaultAllowedHosts`, https-only (+ WithInsecureHTTP/
  WithAllowedHosts test opts), resilience.NewClient (bounded rate,
  concurrency 2, MaxRetries 2, jittered backoff, breaker 5/60s,
  MaxBodyBytes 8MiB).
- mapper.go: upstream row -> provider.Transfer; quirks stay here.
- Streams partition by what the upstream API actually paginates
  independently (from|to, kind-split, or unified); ascending block order
  per stream; never fake per-direction on a unified-list upstream;
  opaque NextPageToken; UpstreamCalls = real outbound calls.
- Config: `<NAME>_API_KEY`, `<NAME>_BASE_URL`, enabled-chains;
  `.env.example` commented placeholder.
- Migration: new `data_sources` row (up INSERT, down DELETE).
- cmd/api/main.go: construct + register under `TX_PROVIDER_*` env
  selection (default = current adapter unchanged) + Budget entry.

## Related Code Files
- Create: internal/provider/moralis/adapter.go, mapper.go, adapter_test.go
- Create: migrations/0000NN_moralis.up.sql / .down.sql
- Modify: internal/config/config.go, cmd/api/main.go, .env.example

## Implementation Steps (TDD)
1. Write fixture tests where the fake server parses the REAL wire shape
   the upstream transport requires (JSON-RPC: positional params
   array; REST: method+path+query+auth-header) — Red.
2. Implement adapter + mapper — Green.
3. Config + migration + wiring.
4. Failure tests: malformed, 429->rate_limited, timeout, disabled,
   pagination continuation.
5. Gates: gofmt -l empty, go vet clean, go build clean, go test green.
6. Live-verify gate: BLOCKED (no MORALIS_API_KEY).

## Success Criteria
- [ ] TxProvider implemented; Enabled() gating correct
- [ ] Streams report Finished per real upstream partition; ascending block order
- [ ] UpstreamCalls accurate; budget charged correctly
- [ ] data_sources row via migration (up+down apply)
- [ ] Unit tests green with real-wire-shape mock
- [ ] `Name()` == `data_sources.code` pinned by wiring test or boot check (swallowed `sourceID()` error → reset never fires)
- [ ] `Budget[Name()] > 0` enforced (missing/typo'd `TXINDEX_BUDGET_<NAME>` env → pool 0 = unlimited)
- [ ] if live-verify BLOCKED: adapter absent from ALL default `TX_PROVIDER_*` selectors
- [ ] Gates pass; one conventional commit `feat(provider): add moralis transaction-history adapter`
- [ ] Live-verify evidence = ALL of: upstream HeadBlock + upstream LookupTransfers on a real address + booted `GET /api/v1/wallets/<net>/<addr>/transactions?refresh=1` showing `provider_status:"ok"` (curl a lone HeadBlock does NOT count) — OR `live-verify: BLOCKED (no key)` reported

## Risk Assessment
- Contract drift: docs-first verification + real-shape mock.
- Native-transfer gap: probe confirms coverage or NOT-VIABLE.
- Rate limits: conservative client rate like nodereal.
