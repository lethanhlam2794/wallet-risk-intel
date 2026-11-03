---
phase: 2
title: "Address Normalization, HTTP Platform & Auth"
status: completed
priority: P1
dependencies: [1]
---

# Phase 2: Address Normalization, HTTP Platform & Auth

## Overview

EVM address normalization/validation, wallet get-or-create persistence, Gin
HTTP server with request-id, API-key auth, per-client rate limit, stable error
envelope, health endpoints, and the first two read APIs (`/networks`,
`/wallets/{network}/{address}`).

## Requirements

- Functional: `wallet.NormalizeEVM` (20-byte hex, `0x` optional→required per
  spec example; lowercase for index; preserve original); `network.Resolver`
  (code→network, eip155 namespace); `wallets` get-or-create on read path;
  middleware: `RequestID`, `APIKeyAuth` (roles), `RateLimit` (Redis
  fixed-window via `CacheKeys`), error envelope; routes `GET /healthz`,
  `GET /readyz`, `GET /api/v1/networks`, `GET /api/v1/wallets/{network}/{address}`.
- Non-functional: every handler validates inputs → 400 with stable code;
  unknown client key → 401; non-admin on `/admin/*` → 403; all external calls
  ctx-bounded; keys hashed sha256 in `api_clients.key_hash`, never logged.

## Architecture

- Normalization rule (spec §7.3): EVM = validated 20-byte hex; indexing
  lowercase; original representation preserved in `wallets.address`.
- Auth: `api_clients` seeded at boot from env `API_BOOTSTRAP_ADMIN_KEY` /
  `API_BOOTSTRAP_READER_KEY` (dev only; upsert sha256 hash, role).
  `X-API-Key` header → lookup hash → role into ctx.
- Rate limit: fixed window — authenticated clients keyed
  `rl:{client_id}:{window}`; **unauthenticated/invalid-key requests keyed
  `rl:anon:{ip}`** (401-probing is bucketed too, spec §16). INCR+EXPIRE
  executed atomically via Lua script or pipeline (never a non-atomic
  two-call sequence — avoids TTL-less phantom counters); string type only
  (P-008). 429 + `Retry-After` on exceed.
- **Redis-down policy (explicit decision)**: rate limiter fails OPEN
  (request proceeds, `rate_limiter_errors_total` metric incremented) —
  sanctions-read availability is the product; `/readyz` still reports
  Redis connectivity separately (degraded ≠ dead).
- Error envelope: `{"error":{"code":"INVALID_ADDRESS|UNAUTHORIZED|FORBIDDEN|
  NOT_FOUND|RATE_LIMITED|INTERNAL","message":"...","request_id":"..."}}`.
- `/wallets/{net}/{addr}`: resolve network (400 `INVALID_NETWORK`), normalize
  (400 `INVALID_ADDRESS`), get-or-create wallet, return wallet + `labels: []`
  (labels out of scope; field reserved) + `data_status` freshness.
  **Acknowledged tradeoff**: write-on-GET accepts enumeration/bloat risk;
  the anon+client rate limit is the guard (documented decision).
- Endpoint auth posture: `/healthz`,`/readyz` public; `/metrics` requires
  reader key; request body size limit middleware (spec §16, e.g. 1MB).
- API-key lookup: indexed DB query on `key_hash` (sha256) — the lookup
  itself is the compare; no extra brute-force claim.

## Related Code Files

- Create: `internal/wallet/address.go`, `internal/network/resolver.go`,
  `internal/wallet/repository.go` (+ `queries/wallets.sql`, sqlc gen),
  `internal/network/repository.go` (+ `queries/networks.sql`)
- Create: `internal/middleware/request_id.go`, `internal/middleware/auth.go`,
  `internal/middleware/rate_limit.go`, `internal/middleware/errors.go`
- Create: `internal/httpserver/server.go` (router wiring) —
  **or** `internal/platform/httpserver/` if that matches Cook's layout; pick
  one, keep thin.
- Modify: `cmd/api/main.go` (wire config→db→redis→router, graceful shutdown),
  `migrations/000001` already created `api_clients`
- Modify: `queries/` populate; run `make sqlc`.

## Implementation Steps (TDD)

1. Red: `address_test.go` — valid EIP-55/mixed-case/uppercase normalize to
   same lowercase; reject: empty, non-hex, wrong length, missing 0x handling,
   `0x` only, 19/21 bytes.
2. Red: `resolver_test.go` — ethereum/bsc resolve; `bsc`≠`ethereum` ids;
   unknown → error.
3. Red: `auth_test.go` — no key→401; bad key→401; reader on /admin→403.
4. Red: `wallet_handler_test.go` — invalid network 400, invalid address 400,
   happy path returns normalized + original preserved; second call idempotent
   (no duplicate row — assert `wallets` count stable, UNIQUE enforced).
5. Green: implement normalization, resolver, repos (sqlc), middleware, routes.
6. Integration (`tests/api_wallets_test.go` on `check_wallet_test`): full
   request → DB row exists; repeated GET → single row.
7. Refactor: extract shared `normalizeAndResolve` used later by sanctions
   handler (phase 05).

## Success Criteria

- [x] Unit tests green for normalization/resolver/middleware.
- [x] `GET /api/v1/wallets/ethereum/0x098B...` returns 200 JSON, wallet
      persisted once (verified via SQL count on `_test` db).
- [x] 400/401/403/404/429 return the stable envelope with `request_id`.
- [x] `/healthz` liveness OK; `/readyz` fails when DB down (verify manually
      or via test).
- [x] API key never appears in logs (grep test or review).
- [x] `go build ./...` clean; gofmt/vet clean.

## Risk Assessment

- Mixed-case input creating duplicate wallets → UNIQUE(network_id,
  normalized_address) + lowercase normalization is the DB-level gate (P-003).
- Key brute force → anon+client rate-limit buckets + hash-only storage.
- Reader hitting admin routes → role check middleware before handler.
