---
phase: 2
title: "GoPlus Security adapter"
status: completed
priority: P1
dependencies: [1]
---

# Phase 2: GoPlus Security Adapter

## Overview

`internal/provider/goplus` — on-demand `Provider` for
`GET api.gopluslabs.io/api/v1/address_security/{address}?chain_id={ref}`.
Primary enrichment source: free, keyless, EVM chain_id = our
`network.ChainReference` verbatim ("1" ethereum, "56" bsc).

## Requirements

- Functional: response model for the live-verified envelope `{code:int,
  message:string, result:{19 flag fields as "0"/"1" strings + data_source}}`.
- Functional: `code != 1` → typed DEGRADED error (never parse flags as clean).
- Functional: flag→category mapping table — every `"1"` flag emits an
  `Observation`; confident mappings only, unmapped risk flags land on
  `OTHER` (never drop signal, never invent a category):

  | field | category |
  |---|---|
  | sanctioned | SANCTIONED |
  | phishing_activities | PHISHING |
  | money_laundering | MONEY_LAUNDERING |
  | stealing_attack | HACK |
  | mixer | MIXER |
  | darkweb_transactions | DARKNET |
  | cybercrime, financial_crime, blacklist_doubt, blackmail_activities, fake_kyc, fake_token, fake_standard_interface, gas_abuse, malicious_mining_activities, honeypot_related_address, number_of_malicious_contracts_created | OTHER |
  | contract_address, reinit, data_source | signals only — no observation |

  (`honeypot_related_address` demoted to OTHER per Critic F8 — a
  honeypot *association* flag is not a scam verdict; raw flag survives in
  Signals/evidence.)

- Functional: `result:null` or `{}` parses as OK-empty (zero
  observations, populated Signals) — never `malformed` (F8; fixture
  test required).
- Functional: all raw flags copied into `Signals`; `data_source`
  (e.g. "SlowMist,BlockSec") preserved into `Observation.OriginalSource`
  + evidence; `verification_status="reported"`, `confidence="unknown"`.
- Functional: `"0"` flags produce NO observations and no safe verdict —
  they are data only (invariant: field=0 ≠ safe).
- Functional: optional `GOPLUS_API_KEY` → `Authorization: Bearer`; absent
  key still enabled (public tier).
- Non-functional: resilience.Client policies — timeout 8s, limiter
  default 30/min (env-tunable), concurrency 4, retry ≤2, breaker 5/60s.

## Architecture

`client.go` (request build, chain_id from `net.ChainReference`),
`model.go` (wire types), `mapper.go` (flag table + Observation build),
`adapter.go` (`Provider` impl wrapping resilience.Client).

Label text = human-readable flag name (e.g. "Stealing attack"); evidence
carries `{field, value, data_source}`; observed_at = fetch time.

## Related Code Files

- Create: `internal/provider/goplus/{client,model,mapper,adapter}.go` + tests + `testdata/address_security_sanctioned.json` (live capture) + `testdata/address_security_clean.json`
- Modify: none outside new package

## Implementation Steps (TDD)

1. RED: golden-fixture parse test (captured OFAC address response →
   sanctioned/stealing_attack/blacklist_doubt observations, OTHER for
   blacklist_doubt, data_source in evidence); clean-address fixture →
   zero observations but Signals populated.
2. RED: httptest-based tests — `code:0` → DEGRADED; 429 → rate_limited;
   500 → retried then upstream error; malformed JSON → malformed;
   Authorization header sent iff key set; chain_id from ChainReference.
3. GREEN: implement client/model/mapper/adapter.
4. REFACTOR: gofmt, vet.

## Success Criteria

- [x] Golden fixture tests pass against REAL captured payload
- [x] DEGRADED/429/500/malformed paths all typed correctly
- [x] No `"0"`-flag observations; `data_source` in evidence
- [x] gofmt/vet/build clean; race-clean

## Risk Assessment

- Over-mapping risk (suy diễn category) → conservative table above;
  unmapped flags degrade to OTHER rather than being dropped.
- Public tier 30/min is per-IP — limiter defaults conservative; env override.
- `contract_address` first-call quirk: documented, not retried.
