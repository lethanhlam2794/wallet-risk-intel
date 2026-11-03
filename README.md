# check-wallet — Blockchain Wallet Risk Intelligence

Go + PostgreSQL + Redis modular monolith that answers, with evidence:
is this Ethereum/BSC wallet on the OFAC SDN list — under which entity,
program, and official reference — and how fresh is that data?

Scope: spec §19 phases 1–4 (foundation, OFAC sanctions, public labels,
live enrichment). Later phases (scoring, tracing) are intentionally absent.

## Architecture

```
cmd/api      REST API (Gin) — lookups, admin triggers, /metrics
cmd/worker   one-shot snapshot ingestion (-source ofac_sdn [-file x.xml])
cmd/migrate  embedded golang-migrate runner
internal/
  config/          env-only config (DATABASE_URL required)
  platform/        database, cache, logger, metrics adapters
  network/         chain registry + resolver (eip155:1, eip155:56)
  wallet/          EVM address normalization + get-or-create
  provider/ofac/   bounded downloader, streaming XML parser, mapper, stager
  ingestion/       single-flight runs → staging → validate → atomic publish
  sanctions/       read path: direct match, cross-chain, unresolved listings
  middleware/      request-id, API-key auth, rate limit, error envelope
  testutil/        _test-database harness (P-020 guard)
migrations/  paired .up/.down SQL (000001 domain, 000002 staging, 000003 snapshots)
queries/     sqlc inputs → generated internal/db (committed)
api/         openapi.yaml (OpenAPI 3.1, implemented endpoints only)
tests/       cross-package integration tests on check_wallet_test
```

Key invariants: non-match is never "safe"; sanctions reasons are never
invented (`reason.summary` only from official text); cross-chain and
network-unresolvable listings are surfaced separately and never count as
direct matches; publish is one transaction — failure leaves the live
snapshot untouched.

## Setup

```bash
cp .env.example .env            # then edit values
docker compose up -d            # postgres :5433, redis :6380
export DATABASE_URL='postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable'
export REDIS_URL='redis://127.0.0.1:6380/0'
go run ./cmd/migrate "$DATABASE_URL"
```

Bootstrap API keys (upserted at api start; removing env vars does NOT
deactivate existing clients):

```bash
export API_BOOTSTRAP_ADMIN_KEY='dev-admin-key'
export API_BOOTSTRAP_READER_KEY='dev-reader-key'
```

## Run

```bash
# API on :8080
DATABASE_URL=... REDIS_URL=... go run ./cmd/api

# Ingest OFAC SDN (downloads the ~128MB Advanced XML)
DATABASE_URL=... REDIS_URL=... go run ./cmd/worker -source ofac_sdn

# Or ingest a local file (skips download)
go run ./cmd/worker -source ofac_sdn -file /tmp/SDN_ADVANCED.XML

# Ingest public wallet labels (two bounded GETs, one per network)
go run ./cmd/worker -source wallet_attribution

# Or from local CSVs (comma-separated; single file = network-scoped run)
go run ./cmd/worker -source wallet_attribution -file /tmp/ethereum.csv,/tmp/bsc.csv
```

## Public wallet labels

Source: [prettydeath/wallet-attribution](https://github.com/prettydeath/wallet-attribution)
(MIT license) — one CSV per supported network under `data/`. Rows are
community/provider attributions: `verification_status` is always `reported`
and never auto-verified; an absent label is **no data, not a safe verdict**;
`GAMBLING`/`EXCHANGE` are classifications, not risk verdicts.

```bash
# List a wallet's labels (reader key)
curl -H "X-API-Key: dev-reader-key" \
  'http://localhost:8080/api/v1/wallets/ethereum/0x…/labels?limit=50'

# Filters: category / source / source_field / verification_status /
# active_only=false (shows soft-revoked rows) / cursor pagination
curl -H "X-API-Key: dev-reader-key" \
  'http://localhost:8080/api/v1/wallets/ethereum/0x…/labels?category=scam&active_only=false'
```

`data_status`/`last_synced_at` reflect the latest published label snapshot
(`UNAVAILABLE` before first publish; `STALE` after `LABELS_STALE_AFTER`,
default 168h). The wallet endpoint's `data_status` stays sanctions-scoped.

## Live enrichment

`GET /api/v1/wallets/{network}/{address}/enrichment` fans out to three live
providers and returns per-provider observations, signals, scores, and
indirect exposures with typed failure statuses.

| Provider | Auth | Notes |
|---|---|---|
| GoPlus Security | keyless (`GOPLUS_API_KEY` optional) | address-risk flags |
| PublicAML | keyless (`PUBLICAML_API_KEY` optional) | `aml_score` + direct/indirect exposure graph |
| Chainabuse (TRM) | **requires `CHAINABUSE_API_KEY`** | community reports; DISABLED (zero calls) without a key — the free tier is ~10 calls/month, unusable on-demand |

Attribution & semantics (never violated): `provider_score` is the
provider's own number (e.g. PublicAML `aml_score`) — never the internal
risk score, and `0` is data, not a safe verdict; every observation is
`reported`, never auto-verified; indirect `exposures[]` are graph-derived
and never imply ownership; a failed provider surfaces a typed status
(`RATE_LIMITED`, `TIMEOUT`, `UPSTREAM_5XX`, …) — never a clean result.
Positive responses cache 15 min, negative/degraded ones 60 s (marked via
`cached`/`cache_expires_at`); `?refresh=1` bypasses the read once per
provider+address per 60 s cooldown; `?providers=` selects a subset. Every
fetch persists a `provider_observations` audit row (verbatim payload,
chain token sent upstream, status, latency; newest 25 kept per
source+wallet). Provider `Authorization` headers are never logged.

```bash
curl -H "X-API-Key: dev-reader-key" \
  'http://localhost:8080/api/v1/wallets/ethereum/0x098B716B8Aaf21512996dC57EB0615e2383E2f96/enrichment'
```

## Example calls

```bash
# Exact sanctions match (reader or admin key)
curl -H "X-API-Key: dev-reader-key" \
  'http://localhost:8080/api/v1/sanctions/wallets/ethereum/0x098B716B8Aaf21512996dC57EB0615e2383E2f96'

# Trigger an ingestion (admin key) → 202 + run id
curl -X POST -H "X-API-Key: dev-admin-key" -H 'Content-Type: application/json' \
  -d '{"source":"ofac_sdn"}' http://localhost:8080/api/v1/admin/ingestion-runs

# Poll run status
curl -H "X-API-Key: dev-admin-key" \
  http://localhost:8080/api/v1/admin/ingestion-runs/<run_id>

# Metrics (reader key)
curl -H "X-API-Key: dev-reader-key" http://localhost:8080/metrics
```

## Test

```bash
go test ./...          # unit + integration (check_wallet_test only)
go test ./... -race    # race detector
go vet ./... && gofmt -l .
```

Integration tests require the compose stack. `internal/testutil` refuses
any database whose name lacks a `_test` suffix, self-applies the full
migration manifest, and truncates mutable tables between tests.

## Notes

- **Keys**: API keys are stored as SHA-256 hashes, never logged, and
  bootstrap env keys only *create* clients — they never deactivate them.
- **Snapshots**: identical checksums skip publish entirely; older
  `DateOfIssue` values are rejected; entity-count sanity band and orphan
  checks gate every publish; staging rows survive failed runs for
  diagnosis (last 5 runs kept).
- **Concurrency**: one run per source, enforced by a session-level
  advisory lock on a pinned connection *and* a partial unique index on
  `status='RUNNING'` — a second trigger gets `409 INGESTION_RUNNING`.
- **Stuck runs**: `RUNNING` rows older than 1h are reaped as
  `FAILED`/`"worker restarted"` at boot and at run start.
- API contract: `api/openapi.yaml` — the only implemented endpoints,
  with real examples.
