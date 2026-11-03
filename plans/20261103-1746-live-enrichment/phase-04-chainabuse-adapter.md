---
phase: 4
title: "Chainabuse adapter (optional, key-gated)"
status: completed
priority: P3
dependencies: [1]
---

# Phase 4: Chainabuse Adapter — Optional, Key-Gated

## Overview

`internal/provider/chainabuse` — on-demand `Provider` for
`GET api.chainabuse.com/v0/reports` (Basic auth; API key as both username
and password per TRM docs). Free standard keys allow only **10
calls/month** — unusable for on-demand enrichment, so the adapter ships
**disabled** unless `CHAINABUSE_API_KEY` is configured (spec §4.5: use the
API only when access is granted).

## Requirements

- Functional: `Enabled()` = `CHAINABUSE_API_KEY != ""`. Disabled → service
  reports status `DISABLED` with reason `no_api_key`; zero outbound calls.
- Functional: `GET /v0/reports?addresses={addr}` (address param per docs);
  Basic auth key:key; response → `Observation` per report, category from
  static map (scam→SCAM, phishing→PHISHING, ransomware→RANSOMWARE,
  hack→HACK, stolen funds→HACK, else OTHER), `verification_status`
  **always `reported`** — community reports never auto-verify.
- Functional: empty report list → OK + zero observations.
- Non-functional: resilience policies — timeout 8s, limiter very
  conservative default 1/min (free tier ~10/mo; env-tunable for partner
  keys), concurrency 1, retry ≤1, breaker 3/120s.

## Architecture

Same adapter shape as goplus/publicaml; wire model coded from TRM API
docs (response cannot be live-verified without a key — tests use
documented-shape fixtures via httptest only; no test ever calls the real
host).

## Related Code Files

- Create: `internal/provider/chainabuse/{client,model,mapper,adapter}.go` + tests + `testdata/reports_sample.json` (docs-shape fixture)

## Implementation Steps (TDD)

1. RED: disabled adapter — `Enabled()==false`, `LookupAddress` returns
   typed `disabled` error, zero HTTP traffic (assert via counting
   transport).
2. RED: enabled path via httptest — Basic auth header == key:key; reports
   map to `reported` observations; empty list OK; 401 → client_4xx typed.
3. GREEN: implement.
4. REFACTOR: gofmt, vet.

## Success Criteria

- [x] Disabled-by-default proven (no outbound call)
- [x] Auth header exact per docs; reported-only statuses
- [x] race-clean, build green

## Risk Assessment

- Unverifiable upstream shape → fixture built strictly from TRM docs;
  adapter marked provisional in code comment; real verification deferred
  until a key exists.
- Tiny free quota → conservative limiter; DISABLED default prevents
  accidental burn.
