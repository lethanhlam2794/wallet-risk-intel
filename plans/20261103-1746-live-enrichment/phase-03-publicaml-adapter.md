---
phase: 3
title: "PublicAML adapter"
status: completed
priority: P2
dependencies: [1]
---

# Phase 3: PublicAML Adapter

## Overview

`internal/provider/publicaml` — on-demand `Provider` for
`POST intelapi.publicaml.org/v1/enrich`. Free non-profit keyless API
(~1k req/hour public tier; optional `PUBLICAML_API_KEY` only raises limit).
Provides provider-side AML score + direct attribution + indirect exposure.

## Requirements

- Functional: **per-address, chain-specific calls only** — spec §4.6
  warns batch requests can return different results than chain-specific
  ones; we never batch. Request `{addresses:[{wallet_address,chain}],
  include:["aml_score","axes","category","risk","incidents",
  "source_of_funds"], top_n:5}` where chain comes from an **explicit
  static map** `{"ethereum":"ETH","bsc":"BSC"}` (F1 — `upper(Code)` would
  yield "ETHEREUM"; fixture test asserts the request-body chain token per
  network, and the literal token is recorded in
  `provider_observations.requested_chain`).
- Functional: response model for `{entities:[{aml_score,
  aml_score_breakdown{direct_attribution{category,score,source,
  source_confidence}, direct_exposure, indirect_exposure,
  outbound_exposure{count,items[{src,category,hops,score,source}]},
  exposure_direction, propagated_sources, frozen_direct, incident_bonus,
  behavioral_risk, attributed_infra_by}, label, category, sanctioned}]}` —
  tolerant of absent/null sub-fields (live response shows nulls).
- Functional: `direct_attribution` → `Observation` (category normalized
  via static map — sanction→SANCTIONED, scam→SCAM, phishing→PHISHING,
  hack→HACK, mixer→MIXER, ransomware→RANSOMWARE, gambling→GAMBLING,
  cex/exchange→EXCHANGE, darknet→DARKNET, else OTHER; entity `label`
  carried through), `verification_status="reported"`,
  `confidence="unknown"`, evidence = raw breakdown slice.
- Functional: `aml_score` → `LookupResult.ProviderScore` (float64 ptr;
  absent → nil) — named `provider_score` end-to-end, never risk_score.
- Functional: `outbound_exposure.items[]` + propagated hops →
  `LookupResult.Exposures` with `Direction` + `Hops` preserved —
  indirect exposure ONLY, never folded into Observations/attribution
  (spec: no ownership inference from graph data).
- Functional: `sanctioned:true` at entity level is a provider claim →
  SANCTIONED observation marked source=publicaml (distinct from our
  first-party OFAC match; never merged).
- Functional: HTTP error body `{error:"..."}` → typed error; empty
  `entities` → OK with zero observations (not failure, not "safe").
- Non-functional: resilience policies — timeout 10s (endpoint does graph
  work), limiter 15/min (under ~1k/hour), concurrency 2, retry ≤2,
  breaker 5/60s; request ≤4 MB, response ≤4 MB.

## Architecture

`client.go` (POST JSON, optional Bearer key), `model.go`, `mapper.go`
(category map, direct/indirect split), `adapter.go`. Sanitize payload
before persist: raw `entities[i]` is stored verbatim (bounded by top_n).

## Related Code Files

- Create: `internal/provider/publicaml/{client,model,mapper,adapter}.go` + tests + `testdata/enrich_sanctioned.json` (live capture, abridged to top_n=3 shape) + `testdata/enrich_unknown.json` + `testdata/error.json`

## Implementation Steps (TDD)

1. RED: golden-fixture parse — sanctioned entity yields SANCTIONED
   observation + provider_score=100 + exposures with hops preserved;
   null breakdown fields tolerated.
2. RED: httptest — error envelope → typed error; 429/5xx/timeout/
   malformed; request body asserts single chain-specific address entry;
   key header iff configured.
3. GREEN: implement adapter.
4. REFACTOR: gofmt, vet.

## Success Criteria

- [x] Golden fixture (real capture) parses: score, direct attribution,
      indirect exposures separate
- [x] Provider score never coerced into categories/observations
- [x] Typed errors on all failure classes; race-clean; build green

## Risk Assessment

- Payload size (counterparties can be large) → top_n bound + 4 MB body cap.
- Category vocabulary drift → unmapped categories → OTHER + raw kept in
  evidence (same pattern as wallet_attribution dynamic categories).
- `sanctioned` provider claim vs our OFAC data → separate source; never
  merged into direct sanctions findings.
