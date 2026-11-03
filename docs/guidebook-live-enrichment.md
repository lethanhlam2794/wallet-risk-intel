# Guidebook — Live Enrichment (Spec Phase 4)

A step-by-step operational guide to the multi-source wallet enrichment
endpoint shipped by `plans/20261103-1746-live-enrichment/` (4 review
rounds → APPROVED; saturation 10/10). Every response below is a **real
capture** from the dev environment — live GoPlus + PublicAML calls against
the OFAC-listed address `0x098B716B8Aaf21512996dC57EB0615e2383E2f96`.

Raw captures: `docs/feature-maps/assets/live-enrichment-*.json`.

---

## 1. Prerequisites

```bash
docker compose up -d            # postgres :5433, redis :6380
go run ./cmd/migrate up         # schema at version 6 (provider_observations)
DATABASE_URL=postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable \
REDIS_URL=redis://127.0.0.1:6380/0 HTTP_ADDR=:18080 \
  go run ./cmd/api &
```

Env keys (all optional in dev): `GOPLUS_API_KEY`, `PUBLICAML_API_KEY`
(quota only — both adapters work keyless), `CHAINABUSE_API_KEY`
(**required** — chainabuse stays `DISABLED` without it and makes zero
outbound calls; startup logs `chainabuse disabled: CHAINABUSE_API_KEY not
configured`).

Dev bootstrap keys: `dev-reader-key-change-me`, `dev-admin-key-change-me`.

## 2. The Endpoint

```
GET /api/v1/wallets/{network}/{address}/enrichment[?refresh=1][&providers=a,b]
```

- `network`: `ethereum` | `bsc` (address validated per chain — 400 on bad).
- `refresh`: `strconv.ParseBool` — bypasses cache once per provider per
  60 s (`SET NX EX` guard on `provider:refresh:*`); inside cooldown it
  silently behaves as a normal (cached) request.
- `providers`: comma subset of `goplus,publicaml,chainabuse`; unknown
  names → `400 BAD_REQUEST`; duplicates deduplicated.

## 3. Happy Path — COMPLETED

```bash
curl -s -H "X-API-Key: dev-reader-key-change-me" \
  "http://127.0.0.1:18080/api/v1/wallets/ethereum/0x098B716B8Aaf21512996dC57EB0615e2383E2f96/enrichment"
```

`live-enrichment-completed.json` (abridged):

```json
{"data": {"status": "COMPLETED", "providers": [
  {"source": "goplus",    "status": "OK",       "cached": false,
   "http_status": 200, "latency_ms": 486,  "persisted": true,
   "observations": [{"category": "SANCTIONED", "label": "sanctioned",
     "verification_status": "reported",
     "evidence": {"field":"sanctioned","value":"1",
                  "data_source":"SlowMist,BlockSec"}}]},
  {"source": "publicaml", "status": "OK",       "cached": false,
   "http_status": 200, "latency_ms": 1822, "persisted": true,
   "provider_score": 100,
   "observations": [{"category": "SANCTIONED", "label": "sanction",
     "entity": "Lazarus Group", "verification_status": "reported"}]},
  {"source": "chainabuse","status": "DISABLED", "enabled": false,
   "persisted": false}]}}
```

Read it right: `provider_score:100` is PublicAML's *own* score — never the
internal risk score; observations are `reported`/unverified; `DISABLED`
means **zero** upstream calls were made; `persisted` reflects the fetch
that produced the result.

## 4. Cache, Refresh, Quota

Second identical call → `live-enrichment-cached.json`: `cached:true`,
`cache_expires_at` set, ~5 ms class. Positive entries live **15 min**;
negative/degraded entries **60 s** and embed their status — a failure can
never replay as clean.

`refresh=1` → `live-enrichment-refresh.json`: cache bypassed, refetched.
In this capture PublicAML then **RATE_LIMITED** — and the audit row shows
it honestly (see §6): status `RATE_LIMITED`, `payload={"attempt":true}`.
Provider failure stays visible; it is never a pass.

`?providers=chainabuse` alone → `live-enrichment-disabled.json`:
`INSUFFICIENT_DATA` — a disabled provider was never attempted, and "no
data" is reported as insufficient, never "safe".

Errors: `?providers=nosuch` → `BAD_REQUEST`; missing key → `UNAUTHORIZED`
(`live-enrichment-error-*.json`).

## 5. Cache Internals (Redis)

```bash
redis-cli -p 6380 -n 0 --scan --pattern 'provider:*'
```

```
provider:goplus:ethereum:0x098b716b8aaf21512996dc57eb0615e2383e2f96
provider:publicaml:ethereum:0x098b716b8aaf21512996dc57eb0615e2383e2f96
provider:refresh:goplus:ethereum:0x098b716b8aaf21512996dc57eb0615e2383e2f96
provider:refresh:publicaml:ethereum:0x098b716b8aaf21512996dc57eb0615e2383e2f96
```

Key shapes: `provider:{source}:{net}:{normalized_addr}` and the refresh
guard `provider:refresh:{source}:{net}:{normalized_addr}` (60 s TTL).
Redis down → the endpoint **fails open**: providers still fetch and the
block carries `cache:"bypassed"` (never silent).

## 6. Audit Trail (PostgreSQL)

Every dispatched fetch appends one row — success or failure:

```sql
SELECT ds.code, po.status, po.requested_chain, po.http_status,
       po.latency_ms, po.provider_score,
       left(po.payload::text, 60) AS payload_head, po.fetched_at
FROM provider_observations po
JOIN data_sources ds ON ds.id = po.source_id
ORDER BY po.fetched_at DESC LIMIT 6;
```

```
 code      | status       | requested_chain | http_status | latency_ms | provider_score | payload_head
-----------+--------------+-----------------+-------------+------------+----------------+-----------------------------------------------------
 goplus    | OK           | 1               |         200 |        138 |                | {"code": 1, "result": {"mixer": "0", ...
 publicaml | RATE_LIMITED | ETH             |             |          0 |                | {"attempt": true}
 publicaml | OK           | ETH             |         200 |       1822 |            100 | {"entities": [{"label": "Lazarus Group", ...
```

- `requested_chain` is the **literal** token sent upstream (`1`/`56`,
  `ETH`/`BSC`) — preserved even on post-dispatch failures.
- `payload` is the verbatim upstream body; non-JSON error bodies ride in
  `{"upstream_body": "…"}` and Postgres-hostile bytes (`\u0000`, lone
  surrogates, invalid UTF-8) are normalized so the row **always** lands.
- `error` JSONB carries `{code, message, retry_after}` (seconds) for
  typed failures.
- Retention: newest **25** rows per (source, wallet) — pruned inside the
  insert transaction. Persistence failure only flips `persisted:false`;
  it never alters provider status.

## 7. Aggregate Status Semantics

| Aggregate | When |
|---|---|
| `COMPLETED` | every attempted provider returned `OK` |
| `PARTIAL` | at least one `OK`, at least one failure |
| `FAILED` | all attempted providers failed |
| `INSUFFICIENT_DATA` | nothing attempted (all `DISABLED`, or subset empty after filter) |

`DISABLED` never counts as attempted and never fabricates an outcome.

## 8. Resilience & Ops Notes

- Per-provider resilience client: token bucket (one token **per attempt**,
  retries included), semaphore, circuit breaker (caller cancels don't
  count), bounded retries on transport/5xx only — 429 never sleeps
  in-request, `Retry-After` bounded and surfaced as `retry_after` seconds.
- HTTPS-only, fixed host allowlists, same-host-only redirects
  (https→http refused), 4 MB body cap, SSRF-checked before the wire.
- Parent fan-out deadline 25 s (`ENRICHMENT_DEADLINE`); one provider's
  failure never cancels siblings (`errgroup.Group`, not `WithContext`).
- Metrics: `provider_requests_total`, `provider_request_duration_ms`,
  `provider_rate_limited_total`, `provider_circuit_state` (Prometheus).
- Logs never contain API keys or `Authorization` headers (F9 — verified
  by grep over live logs).

## 9. Known Limits

- **Chainabuse model is provisional** — docs-shaped; no live key was
  available to verify. Disabled by default; its mapper is unit-tested
  against a docs-shaped fixture only.
- PublicAML `aml_score_breakdown` fields not present in captured fixtures
  are not surfaced in `signals` (raw payload still preserved verbatim).
- `PARTIAL` can't be dev-verified by pointing `*_BASE_URL` at a stub —
  the hardcoded host allowlist correctly refuses it; integration tests
  cover degradation instead.
