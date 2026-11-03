---
plan: live-enrichment
spec_scope: docs/tailieu.md §19 PHASE 4
status: completed
---

# Plan — Live Enrichment (Spec Phase 4)

Scope: spec §19 Phase 4 — on-demand provider adapters (GoPlus required;
Chainabuse + PublicAML optional), provider caching, provider rate limiting
+ circuit breaker, degraded (PARTIAL/FAILED) response handling.
Deliverable per spec: "multi-source wallet screening" — surfaced as a new
read endpoint (additive contract change, mirrored in api/openapi.yaml).

Out of scope: risk scoring / `POST /screenings` (Phase 5), transaction
tracing (Phase 6), anything in Phase 7.

## Sources (verified live 2026-11-03)

| Provider | Access | License/terms check | Endpoint (live-verified) |
|---|---|---|---|
| GoPlus Security | Free, no key needed; optional key raises quota. Free tier ≈30 calls/min (support docs) / 150 CU-min + 30k CU-day (pricing). License: "open, license-free Web3.0 security data API"; non-personal use requires "Powered by Go+ Security" attribution. | OK — free tier sufficient for MVP; attribution via `source` fields + README note | `GET api.gopluslabs.io/api/v1/address_security/{addr}?chain_id={1|56}` → `{code:1,result:{sanctioned:"1",stealing_attack:"1",blacklist_doubt:"1",data_source:"SlowMist,BlockSec",…}}` (captured for OFAC-listed 0x098B…2f96) |
| PublicAML | Free non-profit, **no API key**, ~1k req/hour | OK — keyless; optional key only raises limit | `POST intelapi.publicaml.org/v1/enrich` `{addresses:[{wallet_address,chain:"ETH"\|"BSC"}],include:[…]}` → `{entities:[{aml_score, aml_score_breakdown:{direct_attribution,direct_exposure,indirect_exposure,outbound_exposure.items[{src,hops,score,category}],…}}]}` (live-verified) |
| Chainabuse | API key required (Basic auth, key as user+pass). Free standard key = **10 calls/month** — not usable for on-demand enrichment; partner tier needed | Key-gated → adapter ships DISABLED unless `CHAINABUSE_API_KEY` configured (spec §4.5: "chỉ sử dụng API khi có quyền truy cập") | `GET api.chainabuse.com/v0/reports` (docs.trmlabs.com; shape coded from docs — not live-verified, disabled by default) |

Provider quirks verified/known:
- GoPlus `contract_address` may be empty on first call for an address
  (upstream browser lookup latency; ~5s to populate). Documented; we do NOT
  re-request to force it.
- PublicAML: spec §4.6 warns identical addresses can return different
  results between chain-specific and batch requests → we issue
  **per-address, chain-specific** calls only; never batch.
- GoPlus `code` field: `1` = success; any other code → DEGRADED. Flag value
  `"0"` ≠ safe (invariant: zero fields are data, not verdicts).

## Architecture

```
GET /api/v1/wallets/{net}/{addr}/enrichment[?refresh=1&providers=goplus,…]
        │
        ▼
internal/enrichment.Service        ← orchestration, aggregate status,
        │                            provider_status metadata, wallet
        │                            get-or-create, singleflight
        ├─ Redis cache  provider:{src}:{net}:{addr}   (15m TTL positive /
        │                                              60s negative;
        │                                              fail-open on Redis err)
        ├─ per provider: resilience.Client  (rate limiter → semaphore →
        │     circuit breaker → bounded retry → http; token per attempt)
        │        ├─ internal/provider/goplus      (always enabled)
        │        ├─ internal/provider/publicaml   (enabled by default)
        │        └─ internal/provider/chainabuse  (disabled w/o API key)
        └─ persist raw payload → provider_observations (audit + degraded
                                   status tracking; bounded retention)
```

Aggregate status: all providers OK → `COMPLETED`; mix of OK and failed →
`PARTIAL`; every attempted provider failed → `FAILED`; no provider
attempted (all disabled) → `INSUFFICIENT_DATA`. Provider failure is never
flattened into a clean result (spec §15).

## Phases

| # | File | Title | Depends on |
|---|---|---|---|
| 1 | `phase-01-provider-foundation.md` | Provider contracts, resilience toolkit (limiter/semaphore/retry/circuit-breaker), migration 000006 (`provider_observations` + `data_sources` seeds), config, metrics | — |
| 2 | `phase-02-goplus-adapter.md` | GoPlus `Provider` impl: client, response model, conservative flag→category mapping, quirks | 1 |
| 3 | `phase-03-publicaml-adapter.md` | PublicAML `Provider` impl: per-address chain-specific enrich, direct vs indirect separation, provider score passthrough | 1 |
| 4 | `phase-04-chainabuse-adapter.md` | Chainabuse `Provider` impl: key-gated optional adapter, DISABLED status path, reports→reported-only observations | 1 |
| 5 | `phase-05-enrichment-api.md` | `internal/enrichment` service: cache layer, orchestration, persistence, `GET …/enrichment`, OpenAPI, wiring, failure-mode tests | 1–4 |

## Key design decisions (post-Critic round 1 — F1–F9 applied)

- **New read endpoint** `GET /wallets/{n}/{a}/enrichment` — Phase 4 needs a
  queryable surface; spec §9 has none for provider data (screenings are
  Phase 5). Additive only; openapi.yaml updated in the same phase.
- **Provider observations are NOT wallet_labels.** Live provider signals
  are volatile (15-min cache TTL) — mixing them into the snapshot-driven
  `wallet_labels` lifecycle would break revocation semantics and risk
  double-counting in Phase 5 scoring. Raw payloads persist to a dedicated
  `provider_observations` audit table; normalized signals ride in the
  endpoint response only.
- **`provider_observations`** = append-only audit log (source_id,
  wallet_id, `requested_chain` TEXT per spec §4.6, status VARCHAR w/ CHECK,
  http_status INT, latency_ms INT, payload JSONB NOT NULL,
  provider_score DOUBLE PRECISION NULL, error JSONB NULL, fetched_at
  TIMESTAMPTZ NOT NULL DEFAULT now()), index
  `(source_id, wallet_id, fetched_at DESC)`; retention = newest 25 rows
  per (source_id, wallet_id) pruned inside the insert transaction —
  bounded growth, transient overflow tolerance documented.
- **Cache**: `cache.Provider(source, net, addr)` key exists already
  (`keys.go`). Positive entries 15 min (spec §14); negative/degraded
  entries 60 s and always tagged by status so a failure can never be
  replayed as "clean". `refresh=1` bypasses read, still writes through.
  Redis outage → bypass cache entirely, flag `cache:"bypassed"`.
- **Resilience toolkit** `internal/provider/resilience`: per-provider
  `x/time/rate` limiter (new dep — canonical, stable since 2015),
  `x/sync/semaphore` concurrency cap (already vendored), bounded retry
  (≤2 retries; jittered exponential backoff; retries only 5xx/transport
  errors within remaining request budget; **429 never sleeps in-request** —
  the typed error carries `retry_after` verbatim to the caller), circuit
  breaker (5 consecutive failures → open 60 s → half-open single probe),
  per-provider **hardcoded allowed-host sets** validated against the
  configured base URL at construction (mirror `ofac.defaultAllowedHosts`;
  https-only, test-only insecure hook), redirect-to-same-host-only,
  ≤4 MB bodies, ctx timeouts end-to-end.
- **Synchronous-path bounds** (F2): parent fan-out deadline ~25 s
  (env-tunable); rate limiter consumes one token **per HTTP attempt**
  (not per request) so retries can't triple-burn quota; limiter wait is
  bounded — when the reservation delay exceeds a small threshold the call
  fails fast as typed `rate_limited` + `retry_after` instead of queueing.
- **Metrics** (spec §17): `provider_requests_total{source,result}`,
  `provider_errors_total{source,reason}`,
  `provider_rate_limited_total{source}`, `provider_circuit_open` gauge.
- **Chainabuse** ships as a full adapter but `Enabled()==false` without a
  key; status reported as `DISABLED` (visible metadata, never an error);
  docs-shaped wire model marked provisional until a real key verifies it;
  Authorization headers are never logged (spec §16).
- **`refresh=1` quota guard** (F3): per-key Redis cooldown
  `provider:refresh:{src}:{net}:{addr}` SET NX EX 60 — a refresh request
  inside the cooldown is treated as a normal request (cache allowed);
  refresh is only meaningful when a cache entry exists to bypass.
- **No sibling-cancel fan-out** (F4): providers run via `errgroup.Group`
  (never `WithContext`-derived cancellation); each provider's outcome is
  recorded independently so one failure cannot fabricate `context
  canceled` statuses on siblings.
- **Contract details** (F9 + minors): response uses `enriched_at` (not
  `screened_at` — don't pre-commit Phase-5 vocabulary); `refresh` parses
  via `strconv.ParseBool`; empty `providers=` means "all"; `signals`
  emits `{}` not `null`; openapi `info.description` staleness fixed in
  the same change; docs state `provider_score` ≠ internal risk, `0` ≠
  safe under degraded, `retry_after` units, `cache_expires_at` semantics.
- **Spec §13 extension documented** (F7): `LookupResult`/`Enabled()` are
  deliberate extensions of the spec's `Provider` contract — `[]Observation`
  alone cannot carry raw payload, provider score, or enablement state
  required by §4.6 (raw persistence + degraded tracking).
- **Verification statuses**: GoPlus/PublicAML/Chainabuse observations are
  `reported`/`unverified` — never auto-verified (spec invariant).
- **Provider scores** (e.g. `aml_score`) pass through as
  `provider_score` — never labeled as internal risk (spec §4.6).
- **GoPlus attribution**: README + guidebook note ("Powered by Go+
  Security" required for non-personal use).

## Stage 1 Debate Summary

Critic round 1 (`devin -p` session `imminent-felidae`, dangerous
read-only mode; `reports/critique.md`): verdict CHANGES_REQUESTED with 9
amendments — all accepted and applied.

- F1 (concrete defect): PublicAML `upper(network.Code)` yields
  "ETHEREUM" not "ETH" → explicit static chain map + request-body
  fixture test + `requested_chain` recorded.
- F2: bounded synchronous path — parent ~25 s deadline, limiter token
  per HTTP attempt, bounded limiter wait, 429 returns typed
  `retry_after` instead of in-request sleep.
- F3: `refresh=1` guarded by per-key Redis cooldown (60 s).
- F4: no `errgroup.WithContext` sibling-cancel — outcomes recorded
  independently.
- F5: `provider_observations` DDL pinned (CHECK, index, column types,
  `requested_chain`, in-transaction prune).
- F6: allowed-host sets hardcoded per adapter, base URL validated at
  construction (mirrors `ofac.defaultAllowedHosts`).
- F7: §13 contract extension documented.
- F8: `honeypot_related_address` demoted SCAM→OTHER; `result:null`/
  `{}` → OK-empty parsing + fixture.
- F9: openapi `info.description` staleness fixed; provider_score / `0`≠
  safe / retry_after units / DISABLED semantics documented; no
  Authorization logging.
- Open questions resolved: dedicated endpoint (correct — embedding
  would burn quota on every wallet read); append-log over upsert
  (flapping/degraded evidence preserved); `x/time` accepted (x-family
  already vendored); Chainabuse adapter kept disabled+provisional;
  `top_n` bounded, entities stored verbatim.

## Acceptance criteria

- `go test -count=1 ./...` + `-race` green on `check_wallet_test`
- Live GoPlus + PublicAML calls against dev env return real observations
- 429/5xx/timeout/malformed each produce PARTIAL/FAILED metadata, never
  a clean result; circuit opens on sustained failure
- Cache hit avoids second provider call; `refresh=1` bypasses
- Chainabuse absent key → `DISABLED`, zero outbound calls
- `gofmt -l .` / `go vet` / `go build ./...` clean
- openapi.yaml documents the new endpoint with examples
