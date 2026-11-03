# Technical Feature Map — Live Enrichment (Spec Phase 4)

Plan: `plans/20261103-1746-live-enrichment/` (uncommitted working tree —
pipeline convention). Verdict chain: Critic F1–F9 amended → Cook DONE →
Devin Review APPROVED after 4 rounds (R1: 1 MEDIUM + 7 advisories; R2:
JSONB-insert defect; R3: Postgres-rules residual; R4: case/parity/message
edges — all closed). Saturation 10/10.

## Endpoint surface

`GET /api/v1/wallets/{network}/{address}/enrichment?refresh=&providers=`
— thin handler → `enrichment.Service.Enrich` → per-provider pipeline →
aggregate. `refresh` via `strconv.ParseBool`; `providers` dedup'd subset,
unknown → 400.

## Provider contract (`internal/provider/provider.go`, §13 extension)

- `Provider`: `Name()`, `Enabled()`, `LookupAddress(ctx, net, normalized)`.
- `LookupResult`: source/network/address, literal `RequestedChain`,
  observations, signals, `ProviderScore`, `Exposures` (indirect only),
  `Raw` jsonb-bound body, `HTTPStatus`, `FetchedAt`.
- `LookupError`: `Kind` (11-way typed: rate_limited…internal), `Msg`,
  `HTTPStatus`, `Response` (≤4 KB upstream snippet), `RetryAfter`,
  `RequestedChain` (stamped by every post-dispatch error path so audit
  rows keep the literal token).
- `StampRequestedChain(err, token)` — helper for the `*LookupError` stamp.

## Adapters

| Provider | Chain token | Model | Notable |
|---|---|---|---|
| goplus | `net.ChainReference` (`1`,`56`) | `address_security` envelope | `code≠1`→DEGRADED; `honeypot`→OTHER never SCAM; `"0"` flags→zero obs (never "safe") |
| publicaml | static map `ethereum→ETH`, `bsc→BSC` (F1) | `/v1/enrich` single-wallet POST | `aml_score`→`provider_score` only; `direct_attribution`→Observation vs `outbound_exposure`→Exposures never folded; `sanctioned` *bool null-tolerant; `env.Error`→Msg |
| chainabuse | `net.Code` (address-only upstream) | docs-shaped reports array (provisional) | key-gated `Enabled()`; DISABLED=zero calls; Basic key:key; all `reported` |

## Resilience client (`internal/provider/resilience/`)

- `Do`: host allowlist check → per-attempt [limiter token → semaphore →
  breaker → attempt] → bounded retries on transport/5xx only.
- 429 → typed `ErrRateLimited` + bounded `RetryAfter`, never slept on.
- `breaker.Report(false)` skipped when `ctx.Err()≠nil` — caller cancels
  are not upstream-failure evidence; attempt-deadline timeouts still count.
- `redirectPolicy`: ≤3 hops, same-host only, `https→http` refused —
  extracted package func for direct unit coverage.
- 4 MB body cap; `Response` snippet 4 KB; HTTPS enforced; SSRF checked
  pre-wire.

## Enrichment pipeline (`internal/enrichment/`)

```
Enrich(ctx, net, raw, normalized, opts)
  → normalize + validate address, resolve wallet row (create if missing)
  → selectProviders(filter) — dedup + registry lookup
  → errgroup.Group fan-out (NEVER WithContext — F4 sibling isolation)
      under 25 s parent deadline (ENRICHMENT_DEADLINE)
      per provider: DISABLED→typed status, no call
        → refresh guard SET NX EX 60 (provider:refresh:*) — returns
          (granted, cacheDown) so outage+refresh still marks "bypassed"
        → cache get (errCacheDown vs errCacheMiss distinguish outage)
        → singleflight per (source,net,addr)
        → LookupAddress → persist() → cache put (15m pos / 60s neg,
          self-describing entry: status embedded, failures can't replay clean)
  → aggregate: COMPLETED | PARTIAL | FAILED | INSUFFICIENT_DATA
     (DISABLED = not attempted → INSUFFICIENT_DATA when nothing attempted)
```

## Persistence (`provider_observations`, migration 000006)

- Append-only per dispatched fetch: `source_id, wallet_id,
  requested_chain(literal), status(11-value CHECK), http_status?,
  latency_ms?, payload JSONB, provider_score?, error JSONB?, fetched_at`.
- Retention: newest 25 per (source,wallet), pruned in the insert tx.
- `persisted` wire flag reflects the producing fetch (documented on
  cached hits).
- **jsonb legality layer** (`service.go`): `auditPayload` = sanitize →
  `json.Valid` → verbatim | `{"upstream_body": sanitized}` envelope |
  `{"attempt":true}`. `jsonbEscapeClean` is a parity-aware scanner
  (`\\` consumed as a unit — literal `\ud8xx` text never mistaken for an
  escape): rewrites `\u0000` and lone surrogates to `\ufffd`, keeps valid
  pairs; `jsonbText` handles invalid-UTF-8/literal-NUL for envelopes and
  for `ErrorInfo.Message` (upstream-controlled via publicaml `env.Error`).
  Postgres is stricter than `json.Valid` — every payload/error source is
  normalized so the audit row always lands.

## Cache & fail-open

- Keys `provider:{src}:{net}:{addr}`; guard `provider:refresh:{src}:{net}:{addr}`.
- Redis nil/dead → fetch continues, `cache:"bypassed"` surfaced — incl.
  the refresh path (`refreshAllowed` returns `(granted, cacheDown)`).
- Negative entries carry status — degraded never replays as OK.

## Invariants honored (AGENTS §5)

- No-data ≠ safe (INSUFFICIENT_DATA), failure ≠ clean (typed statuses
  persisted + surfaced), provider_score ≠ internal score,
  reported ≠ verified (all observations `reported`), direct attribution
  vs indirect exposure never folded, unmapped categories → OTHER with
  raw preserved, DISABLED = zero outbound calls.

## Review-remediated findings (all verified)

R1 shared-DB rollback race → `check_wallet_rollback_test` + `NotEqual`
guard. R2 literal `requested_chain` on failure rows. R3 refresh+outage
marker. R4 honest sibling-failure test. R5 provider-filter dedup.
R6 OpenAPI evidence shape + `persisted` doc. R7 caller-cancel breaker
filter. 8a https→http redirect refusal. R2-r2 → R4: JSONB-legality chain
(non-JSON → Go-valid-PG-illegal → uppercase-D/parity/Message edges)
closed by the scanner + real-DB battery (10 shapes + hostile-Msg test).

## Known limits

- Chainabuse model provisional (no live key); PublicAML unknown-breakdown
  fields unsurfaced (raw preserved); `redirectPolicy` `via≥3` branch
  untested; PARTIAL covered by integration tests only (allowlist blocks
  dev stubbing).
