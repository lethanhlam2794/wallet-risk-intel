---
phase: 5
title: "Enrichment service + GET /wallets/{net}/{addr}/enrichment"
status: completed
priority: P1
dependencies: [1,2,3,4]
---

# Phase 5: Enrichment Service + API Endpoint

## Overview

`internal/enrichment` orchestrates all enabled providers for a wallet —
cache-first with singleflight, persists raw observations, aggregates
per-provider status into `COMPLETED|PARTIAL|FAILED|INSUFFICIENT_DATA` —
behind a new read endpoint `GET /api/v1/wallets/{network}/{address}/enrichment`.

## Requirements

- Functional: per-provider flow — cache GET `provider:{src}:{net}:{addr}`
  → hit serves cached (flag `cached:true`) → miss: singleflight →
  `LookupAddress` → persist `provider_observations` row → cache write.
  Positive entry TTL 15 m (spec §14); negative/degraded entry 60 s and
  self-describing (`status` in payload — never replayable as clean).
- Functional: `?refresh=1` bypasses cache read (still writes through)
  BUT is quota-guarded (F3): per-key Redis cooldown
  `provider:refresh:{src}:{net}:{addr}` `SET NX EX 60` — inside the
  cooldown the flag is treated as a normal request (cache allowed); a
  cache miss still fetches (a miss is not a repeat burn). `refresh`
  parses via `strconv.ParseBool`.
- Functional: `?providers=goplus,publicaml` subset filter (400 on
  unknown name; empty value = all providers).
- Functional: fan-out via `errgroup.Group` — **never**
  `errgroup.WithContext`-derived cancellation (F4): a sibling failure
  must not cancel or fabricate `context canceled` statuses on other
  providers; each outcome is recorded independently, and caller-cancel
  reports true per-provider statuses.
- Functional: whole fan-out bounded by a parent deadline (~25 s,
  env-tunable).
- Functional: per-provider `provider_status` block in response:
  `{source, enabled, status, cached, fetched_at, cache_expires_at,
  http_status, latency_ms, error:{code,message,retry_after}}`.
- Functional: aggregate `status` — all-ok COMPLETED; mix PARTIAL; all
  attempted failed FAILED; none attempted INSUFFICIENT_DATA.
- Functional: response `data` = network, address, normalized_address,
  status, providers[]{source,status,observations[],signals,
  provider_score,exposures[],error}, `enriched_at` (not `screened_at` —
  Phase-5 vocabulary stays Phase 5). `signals` emits `{}` not `null`.
  Observation shape:
  category,label,entity,source_url,confidence,verification_status,
  observed_at,evidence.
- Functional: openapi.yaml update in-scope — fix the stale
  `info.description` (still says "phases 1–2" / "labels absent"); the
  new route's docs state `provider_score` ≠ internal risk score, `0` ≠
  safe under degraded status, `retry_after` units, `cache_expires_at`
  semantics, DISABLED semantics; Authorization headers are never logged
  (F9).
- Functional: Redis outage → bypass cache, mark `cache:"bypassed"`,
  log warn, still serve providers (fail-open, never fail the request).
- Functional: persistence failure logs + `persisted:false` on that
  provider block — does not alter provider status (decoupled concerns).
- Non-functional: whole endpoint bounded by the parent deadline (per-
  provider timeouts apply inside it); concurrent provider fan-out via
  errgroup, never sequential.
- Non-functional: metrics feed (requests/errors/rate-limited/breaker);
  openapi.yaml gains the route with examples; README attribution note.

## Architecture

```
enrichment.Service{providers []provider.Provider, q, rdb, sf group, ttl}
  Enrich(ctx, net, addr, opts) → Result
    ├─ wallet.GetOrCreate (persist FK target; reuse wallet.Service)
    ├─ fan-out per provider: cache → lookup → persist → cache
    └─ aggregate + marshal
httpserver: Deps.Enrichment *enrichment.Service → route registration
cmd/api/main.go: build adapters from cfg (goplus, publicaml always;
  chainabuse iff key), service → Deps
```

## Related Code Files

- Create: `internal/enrichment/{service,handler,model,cache}.go` + tests; `tests/enrichment_api_test.go`, `tests/enrichment_failure_test.go`
- Modify: `internal/httpserver/server.go` (Deps + route), `cmd/api/main.go` (wiring), `internal/config/config.go` + `.env.example` (provider keys), `api/openapi.yaml`, `README.md`

## Implementation Steps (TDD)

1. RED: service tests with fake providers — cache hit path (provider
   called once across two Enrich calls), refresh bypass, refresh inside
   cooldown → cache allowed, negative cache replay marked non-clean,
   PARTIAL on mixed outcomes, FAILED all-fail, INSUFFICIENT_DATA
   all-disabled, Redis-down bypass.
2. RED: persistence test — observation rows land (payload, status,
   provider_score) + retention prune.
3. RED: API tests — 200 shape, invalid network/address 400s, providers
   filter 400, refresh flag.
4. GREEN: implement service + handler + wiring.
5. REFACTOR: OpenAPI examples, README note, gofmt/vet.

## Success Criteria

- [x] All service/handler tests pass incl. `-race` (concurrent fan-out)
- [x] Live `curl` against dev stack: GoPlus+PublicAML real data, second
      call `cached:true`, `refresh=1` refetches
- [x] PARTIAL visible when a provider is stubbed to fail (httptest env
      override URL — dev-verified)
- [x] openapi.yaml updated; guidebook-able examples produced

## Risk Assessment

- Provider latency stacking → concurrent fan-out + per-provider timeout.
- Negative-cache masking real recovery → 60 s TTL only; refresh bypass.
- Observation persistence write amplification → single insert per fetch,
  retention bound; DB errors isolated from provider status.
- Cache stampede → singleflight per (source,net,addr).
