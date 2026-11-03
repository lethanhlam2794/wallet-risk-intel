---
phase: 1
title: "Repository & Platform Foundation"
status: completed
priority: P1
dependencies: []
---

# Phase 1: Repository & Platform Foundation

## Overview

Bootstrap the Go module, dev/test infrastructure, configuration, platform
adapters (pgx pool, Redis, slog), and the embedded golang-migrate runner with
migration 0001 (core + sanctions schema). No HTTP surface yet.

## Requirements

- Functional: `go.mod` (module `checkwallet`), Makefile, `.env.example`,
  `docker-compose.yml`, `configs/config.example.yaml` (or env-only config),
  migration 0001 up/down creating: `blockchain_networks`, `wallets`,
  `data_sources`, `entities`, `sanctions_entities`, `sanctions_programs`,
  `sanctions_entity_programs`, `sanctions_wallets`, `sanctions_references`,
  `ingestion_runs`, `api_clients`; seeds for networks (ethereum=eip155:1,
  bsc=eip155:56) and `data_sources` (ofac_sdn).
- Non-functional: migrations embedded via `embed.FS`; runner registered
  alongside files (P-007); all SQL files have matching `.down.sql`;
  no secrets committed; `config.Load()` reads env only.

## Architecture

- `docker-compose.yml`: `postgres:16-alpine` bound to `127.0.0.1:5433`
  (POSTGRES_USER=wallet, POSTGRES_PASSWORD from env default dev-only,
  POSTGRES_DB=check_wallet) with an init script creating `check_wallet_test`;
  `redis:7-alpine` bound to `127.0.0.1:6380`. Ports chosen to avoid the
  occupied system 5432/6379.
- `internal/config`: `Load()` → `Config{HTTPAddr, DatabaseURL, RedisURL,
  LogLevel, ...}` from env; fails fast on missing `DATABASE_URL`.
- `internal/platform/database`: pgxpool with connect timeout; `Migrate()`
  wraps golang-migrate `iofs` source over embedded `migrations/`.
- `internal/platform/cache`: go-redis client + `CacheKeys` registry
  (P-023: no inline key strings anywhere else).
- `internal/platform/logger`: slog JSON handler, level from config.
- `internal/testutil`: `RequireTestPool(t)` — parses test DSN, **fails the
  test unless db name ends `_test`** (P-020), auto-creates the DB, applies
  full migration manifest (P-036), truncates between tests.
- DDL notes: keep spec §7 column definitions verbatim where they exist;
  `wallets.normalized_address` + `UNIQUE(network_id, normalized_address)`;
  `sanctions_wallets` unique index incl. `COALESCE` columns as specified;
  `ingestion_runs` per §7.15 plus `snapshot_version`; `api_clients(id
  bigserial, name, key_hash unique, role check in ('reader','admin'),
  is_active, created_at, last_seen_at)`.

## Related Code Files

- Create: `go.mod`, `Makefile`, `.env.example`, `.gitignore`,
  `docker-compose.yml`, `docker/postgres/init-test-db.sql`,
  `cmd/api/main.go` (stub), `cmd/worker/main.go` (stub)
- Create: `internal/config/config.go`, `internal/platform/database/db.go`,
  `internal/platform/database/migrate.go`, `internal/platform/cache/redis.go`,
  `internal/platform/cache/keys.go`, `internal/platform/logger/logger.go`,
  `internal/testutil/testdb.go`
- Create: `migrations/000001_init.up.sql`, `migrations/000001_init.down.sql`
- Create: `sqlc.yaml`, `queries/` (placeholder `.gitkeep` until phase 02)

## Implementation Steps

1. TDD-Red: `internal/config/config_test.go` (missing env → error; valid env
   → parsed config); `internal/testutil/testdb_test.go`-style guard test
   asserting `_test` suffix enforcement.
2. `go mod init`, add deps: `gin`, `jackc/pgx/v5`, `redis/go-redis/v9`,
   `golang-migrate/migrate/v4` (+postgres+iofs drivers), `google/uuid`,
   `stretchr/testify`, `prometheus/client_golang`. Pin versions ≥7 days old.
3. Write docker-compose + init script; `docker compose up -d`.
4. Write migration 0001 (up+down) + seed inserts for networks/data_sources.
5. Implement config/database/cache/logger/testutil; register embedded
   migrations in `Migrate()` (P-007).
6. TDD-Green: run `go test ./internal/...`; run `make migrate` equivalent
   against `check_wallet` + `check_wallet_test`; verify `\dt` lists all tables.
7. `Makefile` targets: `deps-up`, `migrate`, `sqlc`, `test`, `test-int`,
   `build`, `vet`, `fmt`, `run-api`, `run-worker`, `tools` (installs sqlc,
   golang-migrate pinned).

## Success Criteria

- [x] `docker compose up -d` healthy; `psql` to 5433 works as `wallet`.
- [x] Migration up/down/up replays cleanly on both DBs (P-018 sanity).
- [x] `\dt check_wallet` shows all 11 domain tables + schema_migrations.
- [x] `check_wallet_test` exists and `testutil` rejects `check_wallet` as test db.
- [x] `go build ./...` exit 0; `go test ./internal/config` green.
- [x] `gofmt -l .` empty; `go vet ./...` clean.

## Risk Assessment

- Migration replay non-idempotent → down.sql must fully drop; verify
  up-down-up cycle in CI step (P-018).
- sqlc missing → `make tools` pins version; document in README.
- **sqlc generated output (`internal/db`) is committed** so `go build` works
  without the sqlc binary; `make sqlc` regenerates.
- `API_BOOTSTRAP_*` semantics: upsert on boot; removing the env var does NOT
  deactivate a previously seeded client — rotation requires explicit
  deactivation (document in README).
- Accidental secret commit → `.gitignore` covers `.env*`; `.env.example`
  uses placeholders only.
