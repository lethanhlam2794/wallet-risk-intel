---
phase: 1
title: "Provider Foundation — contracts, resilience toolkit, schema, config, metrics"
status: completed
priority: P1
dependencies: []
---

# Phase 1: Provider Foundation

## Overview

Lay the groundwork every on-demand adapter and the enrichment service build
on: the spec §13 `Provider`/`Observation`/`LookupResult` contracts, a shared
resilience toolkit (rate limit, concurrency cap, bounded retry, circuit
breaker), migration 000006 (`provider_observations` audit table + provider
`data_sources` seeds), config keys, and spec §17 provider metrics.

## Requirements

- Functional: typed `Provider` interface with `Enabled()`; `LookupResult`
  carrying observations + raw payload + optional provider score + fetch
  metadata; typed `LookupError` kinds (rate_limited, timeout, upstream_5xx,
  client_4xx, malformed, circuit_open, disabled). The `LookupResult`/
  `Enabled()` additions are a deliberate, documented extension of the
  spec §13 contract — `[]Observation` alone cannot carry the raw payload,
  provider score, or enablement state that §4.6 demands (F7: record this
  rationale in the provider.go doc comment).
- Functional: resilience `Client` wrapping `http.Client` — per-provider
  token-bucket limiter consuming **one token per HTTP attempt** (not per
  request, so retries cannot triple-burn upstream quota); bounded limiter
  wait — reservation delay beyond a small threshold fails fast as typed
  `rate_limited` + `retry_after`; semaphore concurrency cap; bounded retry
  for 5xx/transport only inside the remaining request budget (429 NEVER
  sleeps in-request — the typed error carries `retry_after` verbatim to
  the caller); circuit breaker (closed→open→half-open); same-host
  redirects only; ≤4 MB bodies; **per-provider allowed-host sets
  hardcoded in adapter code** (mirror `ofac.defaultAllowedHosts`) with
  the configured base URL validated against the set at construction —
  fail fast at startup; https-only with a test-only insecure hook (F6).
- Functional: `provider_observations` append-only audit table — pinned
  DDL (F5): `status VARCHAR(20)` + CHECK over the status vocabulary;
  `requested_chain TEXT` recording the literal chain token sent upstream
  (spec §4.6); `payload JSONB NOT NULL`; `provider_score DOUBLE
  PRECISION NULL`; `error JSONB NULL`; `http_status INT`; `latency_ms
  INT`; `fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()`; index
  `(source_id, wallet_id, fetched_at DESC)`; retention = newest 25 rows
  per (source_id, wallet_id) pruned **inside the insert transaction**
  (transient overflow tolerance documented); `data_sources` seeds
  `goplus`, `publicaml`, `chainabuse` (`source_type='api'`).
- Non-functional: metrics `provider_requests_total{source,result}`,
  `provider_errors_total{source,reason}`,
  `provider_rate_limited_total{source}`, `provider_circuit_open{source}`.
- Non-functional: ctx cancellation end-to-end; no goroutine leaks;
  thread-safe limiter/breaker (concurrent requests).

## Architecture

`internal/provider/provider.go` gains on-demand types (existing snapshot
types untouched):

```go
type Observation struct {
    Network, Address, Category, Label, Entity, Source, OriginalSource,
    SourceURL, Confidence, VerificationStatus string
    ObservedAt time.Time
    Evidence   json.RawMessage
}
type LookupResult struct {
    Source, Network, Address string
    Observations   []Observation
    Signals        map[string]any   // provider-native flags, unmapped kept raw
    ProviderScore  *float64         // e.g. aml_score — NEVER internal risk
    Exposures      []Exposure       // indirect-only, never attribution
    Raw            json.RawMessage
    FetchedAt      time.Time
}
type Exposure struct { // indirect exposure summary (PublicAML §4.6)
    Counterparty, Category string
    Hops int; Score *float64; Direction string
}
type Provider interface {
    Name() string
    Enabled() bool
    LookupAddress(ctx context.Context, net network.Network, normalized string) (*LookupResult, error)
}
type LookupError struct { Kind ErrKind; HTTPStatus int; RetryAfter time.Duration; Msg string }
```

`internal/provider/resilience/`: `Client{http, limiter, sem, breaker, retry}` —
`Do(ctx, req)` executes limit→semaphore→breaker→attempt loop; classifies
responses into `LookupError` kinds; metrics via injected `Hooks` interface
(avoids metrics→resilience import cycle).

`migrations/000006_provider_observations.up.sql` + `.down.sql`;
`queries/provider_observations.sql` (insert + select-latest + retention
delete) → `sqlc generate`.

## Related Code Files

- Create: `internal/provider/resilience/{client,limiter,breaker,retry,errors}.go` + tests
- Create: `migrations/000006_provider_observations.{up,down}.sql`, `queries/provider_observations.sql`
- Modify: `internal/provider/provider.go` (contracts), `internal/config/config.go` (provider keys), `internal/platform/metrics/metrics.go` (provider collectors), `.env.example`

## Implementation Steps (TDD)

1. RED: tests for error classification (429+Retry-After → rate_limited w/
   hint; 5xx → upstream; 4xx → client_4xx non-retriable; ctx deadline →
   timeout; bad JSON → malformed), retry bounds (max attempts honored,
   Retry-After hint value capped), breaker transitions (5 fails → open →
   rejects fast → half-open probe → close), limiter wait + ctx cancel.
2. GREEN: implement resilience toolkit to pass.
3. RED: migration test — provider_observations insert/select-latest/
   retention prune (26 inserts → 25 kept, newest win); seeds present.
4. GREEN: migration + sqlc queries.
5. REFACTOR: gofmt, vet.

## Success Criteria

- [x] Resilience tests pass incl. race mode (`-race`)
- [x] Migration applies cleanly on `check_wallet_test`; down rolls back
- [x] sqlc generates; `go build ./...` clean
- [x] New metrics registered and scrapeable (unit-verified)
- [x] Config parses provider keys with sane defaults; missing optional keys fine

## Risk Assessment

- Retry storms on provider outage → bounded retries + breaker; test proves max-attempts.
- In-process limiter/breaker only (single API process MVP) — documented limitation; Redis-backed limit deferred.
- `provider_observations` growth → retention bound tested.
