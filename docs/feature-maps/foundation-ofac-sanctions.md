# Feature Map — Foundation + OFAC Sanctions Intelligence

Scope: spec Phase 1 (repo/platform foundation) + Phase 2 (OFAC SDN sanctions
intelligence). Verified against the live OFAC Advanced XML on 2026-11-03
(snapshot `ofac-c1279a38fa18-1793687967`, 19,484 entities / 1,075 wallet
listings / 72 programs).

---

## 1. Module Map

```
cmd/
  api/      REST server entrypoint (Gin)            cmd/api/main.go
  worker/   ingestion CLI (-file / -source modes)   cmd/worker/main.go
  migrate/  embedded migration runner               cmd/migrate/main.go

internal/
  config/      env-driven config                    config.go
  platform/    postgres (pgx), redis, logger        platform/{postgres,redis,logger}
  network/     network registry + code resolver     network/resolver.go
  wallet/      EVM address normalization/validation wallet/address.go
  provider/
    provider.go           Snapshot/Check interfaces
    ofac/
      downloader.go  HTTPS/SSRF-allowlisted bounded downloader
      parser.go      streaming xml.Decoder (strict, no DOCTYPE)
      mapper.go      DistinctParty→entities/wallets/programs/refs
      importer.go    file → staged rows
  ingestion/   run lifecycle, advisory lock, staging→validate→publish
  sanctions/   query service, reason model, HTTP handlers
  middleware/  request-id, access log, body limit, recovery envelope,
               API-key auth (SHA-256), role guard, Redis rate limit
queries/       sqlc sources → internal/db generated
migrations/    000001..000004 (init, staging, snapshots, provenance+heartbeat)
tests/         integration + failure-mode suites (dedicated _test DB only)
```

## 2. Data Flow — OFAC Snapshot Ingestion

```
worker (or POST /admin/ingestion-runs)
  → downloader.Fetch(ctx)
      HTTPS-only (prod), fixed host allowlist,
      per-hop CheckRedirect validation, bounded size,
      bounded Retry-After (≤5min), SHA-256 checksum,
      temp file, FinalURL recorded
  → parser (streaming, Strict=true, DOCTYPE rejected,
      root=<Sanctions Version=3> verified, charset ≤ UTF-8/US-ASCII,
      FeatureType table discovered dynamically)
  → mapper (profile/entity identity, Digital Currency feature →
      currency_code + network mapping; unresolvable → network_id NULL)
  → importer → stg_* tables scoped by run_id
  → validate (counts, orphan check, entity sanity band,
      DateOfIssue non-regression)
  → publish in ONE transaction:
      upsert entities/wallets/programs/references
      soft-revoke (revoked_at) rows absent from new snapshot
      revive rows that reappear (revoked_at → NULL)
      insert sanctions_snapshots row → version
      mark run COMPLETED
  → advisory lock released; stg_* rows deleted on success
     (retained on failure for diagnosis)
```

Concurrency: one `RUNNING` row per source — enforced by a **partial unique
index** + a PostgreSQL **advisory lock** held on the pinned run connection.
Stale sweeps key off `heartbeat_at` (written every 30s by a ticker goroutine),
not merely `started_at`, so long-but-alive runs are not swept.

## 3. Query Semantics — Wallet Sanctions Lookup

`GET /api/v1/sanctions/wallets/{network}/{address}` partitions results into
three explicit scopes — SQL NULL three-valued logic can never silently hide
a listing:

| Scope | Predicate | Meaning |
|---|---|---|
| direct | `network_id = :net` | CHAIN_SPECIFIC match on this network |
| cross_chain_observations | `network_id IS NOT NULL AND network_id <> :net` | same normalized address listed on *another* chain — evidence only, never a direct match |
| unresolved_network_listings | `network_id IS NULL` | listed address whose network could not be resolved — surfaced, never dropped |

`matched = true` only for direct scope. `match_scope` is CHAIN_SPECIFIC or
UNRESOLVED. A sanctions **non-match is not "safe"** — the response still
carries `data_status` so callers can distinguish AVAILABLE/STALE/UNAVAILABLE.

## 4. Reason Model — Never Invented

`internal/sanctions/reason.go`:

- `OFFICIAL_EXPLICIT` — the source published a reason; `summary` carries it
  verbatim.
- `PROGRAM_CONTEXT_ONLY` — only program codes exist (e.g. MESRI Behzad →
  CYBER2 + HRIT-IR); `summary = null`.
- `NOT_AVAILABLE` — nothing published; `summary = null`.

Handlers never synthesize text. Entity/program detail responses include
`data_status` + `last_synced_at` recomputed per request from the latest
published snapshot.

## 5. Freshness & Caching

- `sanctions_snapshots.published_at` is the single source of truth for
  freshness; `data_status`/`last_synced_at` are derived **at response time**
  (never stored in the cache payload).
- Cache keys are versioned: `wallet:sanctions:{net}:{addr}:{snapshot_version}`
  — publishing a new snapshot naturally orphans old entries; no explicit
  invalidation scan needed. Negative results may be cached under the same
  versioned key.
- The Redis "current snapshot" pointer was **removed** after review: it could
  outlive the DB version and mislabel stale results as fresh. The service now
  reads the latest snapshot directly from PostgreSQL.

## 6. Provenance

- Every sanctions row carries source linkage; entity-program links and
  references are **soft-revoked** (`revoked_at`) rather than deleted so
  historical evidence survives; relisting revives the row and preserves the
  original `first_seen_at`.
- `sanctions_wallets` keeps both `listed_address` (verbatim) and
  `normalized_address` (lowercase EVM canonical) — e.g. entity 4703 keeps
  `0x1CAb8177ACe78b1B6B1c393371F4f2dCAE40CbEB` verbatim next to its
  normalized form.
- `wallets.fingerprint`-style stability: staging inserts are idempotent per
  `run_id`; publish upserts on stable natural keys.

## 7. Security Posture (review-hardened)

- Downloader: production requires HTTPS; `WithInsecureHTTP()` exists **only**
  for tests; every redirect hop re-validated against scheme + host allowlist;
  `Retry-After` capped at 5 min; size bounded; checksum verified.
- API keys stored as SHA-256 hashes only; admin routes need `admin` role.
- Redis Lua-token-bucket rate limiting; anonymous bucket keyed by client IP.
- Stable error envelope `{error:{code,message,request_id}}` including the
  panic-recovery path (no leaked stack traces).
- Worker detached runs get a 45-minute `context.WithTimeout`; a panic marks
  the run FAILED instead of leaving a phantom RUNNING row.

## 8. Key Files

| Concern | File |
|---|---|
| Downloader (SSRF/HTTPS/retry) | `internal/provider/ofac/downloader.go` |
| Streaming XML parser | `internal/provider/ofac/parser.go` |
| OFAC → domain mapping | `internal/provider/ofac/mapper.go` |
| Run lifecycle + heartbeat | `internal/ingestion/service.go` |
| Atomic publish + soft-revoke | `internal/ingestion/publish.go` |
| Lock + stale sweep | `internal/ingestion/locks.go` |
| Staging validation | `internal/ingestion/validate.go` |
| Lookup partitioning + freshness | `internal/sanctions/service.go` |
| Reason model | `internal/sanctions/reason.go` |
| HTTP handlers | `internal/sanctions/handler.go` |
| EVM normalization | `internal/wallet/address.go` |
| Panic envelope | `internal/middleware/recovery.go` |
| Cache keys | `internal/platform/cache/keys.go` |
| SQL (sqlc) | `queries/{sanctions,ofac_publish,ingestion_runs}.sql` |
| Schema | `migrations/000001–000004` |
| API contract | `api/openapi.yaml` |

## 9. Verification Entry Points

- Unit/integration: `go test -count=1 ./...` (integration requires the
  `_test` DB — testutil refuses non-`_test` names)
- Race: `go test ./... -race`
- Blind saturation: `scripts/saturation-loop.sh` (10 shuffled iterations)
- Real-source import: `go run ./cmd/worker -source ofac_sdn`
- Live evidence: `docs/feature-maps/assets/live-*.json`,
  `docs/guidebook-ofac-sanctions.md`

## 10. Explicit Deviations / Accepted Trade-offs

- Spec §9.3 example shape adapted: response exposes true OFAC
  `external_entity_id` and a compact `source:{code,name,url}` instead of
  redundant `list/provider/record_id` fields — recorded in
  `plans/20261103-1217-foundation-ofac-sanctions/phase-05-sanctions-query-api.md`.
- Most real OFAC listings (950/1,075) are `UNRESOLVED` because the source
  often names a currency (e.g. USDT) without a chain; they are surfaced under
  `unresolved_network_listings` rather than guessed onto a network.
