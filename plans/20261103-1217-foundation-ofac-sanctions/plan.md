---
plan: foundation-ofac-sanctions
title: "MVP Foundation + OFAC Sanctions Intelligence (Spec §19 Phases 1-2)"
status: completed
risk: R3
created: 2026-11-03
source-spec: docs/tailieu.md (Technical Design Specification v2.0)
---

# Plan: Foundation + OFAC Sanctions Intelligence

## 1. Overview

Greenfield scaffold of the **Blockchain Wallet Risk Intelligence** modular
monolith (Go + Gin + PostgreSQL/pgx + sqlc + Redis + golang-migrate), covering
**spec §19 Phase 1 (Foundation)** and **Phase 2 (OFAC Sanctions
Intelligence)** only.

Deliverable: a working REST API where a partner can query a wallet's direct
OFAC sanctions status with entity, program, and source evidence — backed by a
locally searchable, atomically published OFAC SDN snapshot.

**Change risk: R3** — new schema (migrations), auth boundary (API keys),
security-sensitive ingestion pipeline (XML download/parse). Not R4: no
payments, no irreversible production data ops.

## 2. Explicit Scope Boundary

| In scope (spec §19) | Out of scope (later pipeline runs) |
|---|---|
| Phase 1: Foundation | Phase 3: public wallet labels ingestion |
| Phase 2: OFAC sanctions intelligence | Phase 4: GoPlus/Chainabuse/PublicAML enrichment |
| | Phase 5: risk scoring & screening API |
| | Phase 6-7: transaction tracing |

Tables `risk_categories`, `wallet_labels`, `transactions`,
`screening_results` are **not** created in this increment (spec §3
implementation order: do not scaffold later phases early). No paid providers.
No chains beyond eip155:1 (ethereum) and eip155:56 (bsc).

## 3. Architecture

Modular monolith per spec §6:

```
cmd/api            REST server (Gin)
cmd/worker         ingestion entrypoint (one-shot OFAC sync; optional interval loop)
internal/config    env loading (no secrets in code)
internal/platform  database (pgxpool + embedded golang-migrate), cache (redis), logger (slog)
internal/network   chain registry (eip155 namespace)
internal/wallet    address normalization/validation + wallet get-or-create
internal/sanctions handler/service/repository/model (lookup + entity/program APIs)
internal/provider/ofac  SnapshotSource: downloader → streaming parser → mapper → importer
internal/ingestion run orchestration: stage → validate → publish (advisory lock)
internal/middleware request-id, API-key auth, rate limit
internal/db        sqlc-generated query code
internal/testutil  dedicated-test-DB helper (P-020: enforces *_test db)
migrations/        0001 core+sanctions schema; 0002 OFAC staging tables
queries/           sqlc queries
api/openapi.yaml   contract for implemented endpoints only
configs/           example config
tests/             cross-package integration tests (dedicated check_wallet_test)
```

Data flow (spec §8.1):

```
SDN_ADVANCED.XML → temp file → validate (status/size/checksum/XML)
→ streaming xml.Decoder → staging tables → row-count/consistency checks
→ single-tx publish → revoke absent identifiers → invalidate sanctions cache
```

## 4. Key Design Decisions

1. **Snapshot safety**: publish only after a fully validated snapshot; absent
   identifiers get `revoked_at`, never hard delete (spec §4.1, §12).
2. **Chain-specific matching**: `sanctions_wallets.network_id` set only when
   the currency→network mapping is evidenced (ETH→eip155:1, BNB→eip155:56).
   Direct match requires `normalized_address` + `network_id` equality.
   Same-normalized-address rows on other networks surface as
   `cross_chain_observations`, never as direct matches.
3. **Reason model (spec §8.3)**: `reason.summary` populated only from official
   evidence → `OFFICIAL_EXPLICIT`; program-only context →
   `PROGRAM_CONTEXT_ONLY`; nothing published → `summary=null` +
   `NOT_AVAILABLE`. Reasons are never invented.
4. **Non-match ≠ safe**: `matched:false` returns `data_status` reflecting
   snapshot freshness; if no snapshot was ever published → `UNAVAILABLE`.
5. **Ingestion concurrency**: single-flight via Postgres advisory lock +
   `ingestion_runs.status`; concurrent trigger → 409 (G-005).
6. **Auth**: `X-API-Key` → `api_clients` (sha256 key hash, `role` reader|admin).
   `/admin/*` requires admin. Redis fixed-window rate limit per client.
7. **sqlc + pgx**: all queries parameterized in `queries/`; no interpolated SQL.
8. **Test isolation**: integration tests only against `check_wallet_test`
   (name must end `_test` — enforced by `internal/testutil`, ledger P-020);
   test helper applies the full migration manifest itself (P-036).
9. **Tooling**: `docker-compose.yml` provides postgres:16-alpine (127.0.0.1:5433)
   + redis:7-alpine (127.0.0.1:6380) with dev and `_test` databases; system
   Postgres 5432 is not usable (no role for local user).
10. **OFAC XML reality check**: parser is written against the real
    `SDN_ADVANCED.XML` document structure, verified by downloading the real
    file during development; unit tests use small fixtures whose shape is
    validated against the real file (P-021, P-047).

## 5. Phase Roadmap

| Phase | Title | Dependencies | Status |
|---|---|---|---|
| 01 | Repository & Platform Foundation | — | pending |
| 02 | Address Normalization, HTTP Platform & Auth | 01 | pending |
| 03 | OFAC SnapshotSource (Download/Parse/Map) | 01, 02 | pending |
| 04 | Staging Validation, Atomic Publish & Admin Ingestion API | 03 | pending |
| 05 | Sanctions Query API + Snapshot-Versioned Cache | 04 | pending |
| 06 | Observability, OpenAPI & Failure-Mode Hardening | 04, 05 | pending |

## 6. Risk Matrix

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| OFAC Advanced XML schema differs from assumptions | M | H | Parse real file during dev; fixture validated against real doc; fail closed on unexpected root/schema |
| Interrupted/corrupt download destroys snapshot | M | H | Temp-file + checksum + completeness validation before publish; never replace current snapshot on failure |
| sqlc CLI not installed | M | M | Install pinned `go install .../sqlc@v<stable ≥7d>`; Makefile `tools` target |
| System Postgres inaccessible | H | M | Dedicated docker-compose postgres on 127.0.0.1:5433 + `_test` db |
| Concurrent ingestion duplicates/corrupts | M | H | pg advisory lock + unique constraints + 409 on concurrent trigger |
| Cross-chain address confusion (EVM same address ETH/BSC) | M | H | `match_scope=CHAIN_SPECIFIC`; direct match requires network_id; cross-chain surfaced separately |
| Parser memory blowup on 100MB+ XML | M | M | `xml.Decoder` streaming only; parser unit test asserts no full-document unmarshal |
| XML parser resource abuse (XXE/billion-laughs) | L | H | `Decoder.Strict`, charset-limited, entity expansion limits; download size cap |

## 7. Acceptance Criteria (from spec §20 subset)

- Ethereum and BSC addresses validated; invalid rejected 400.
- OFAC Advanced XML downloadable, streamed-parsed, imported.
- Entities/programs/wallet identifiers/references linked and queryable.
- Missing reasons explicitly `NOT_AVAILABLE`, never invented.
- Snapshot publish atomic; failed snapshot preserves existing data.
- Repeated ingestion creates no duplicates (stable unique keys).
- Disappeared listings revoked only after a fully validated snapshot.
- Sanctions lookup returns direct match + entity + program + evidence.
- Integration and failure tests pass on dedicated `_test` database.
- `go build ./...`, `go test ./...`, `go vet ./...`, `gofmt -l .` clean.

## 8. Stage 1 Debate Summary

**Critic session**: `complete-thumb` (2 rounds; critique + verification
transcripts in `reports/`).

**Challenges raised (all conceded — findings were valid)**:
- A1 (blocking): `network_id != :resolved` three-valued-logic bug made
  NULL-network OFAC listings (USDT/ARB/…) invisible even as observations →
  added `unresolved_network_listings[]` + explicit NULL predicates + test.
- A2: revocation unscoped by source; no checksum-identical no-op; no
  DateOfIssue regression guard → all added.
- A3: advisory lock on pooled conn could release mid-run; async run would
  die on request ctx → pinned conn + `UNIQUE(source_id) WHERE RUNNING`
  index + detached ctx + recover.
- A4: no retry policy on ~100MB download (spec §15) → bounded retry +
  Retry-After + failure metrics; circuit breaker deferral recorded.
- A5: unbucketed 401-probing; non-atomic INCR+EXPIRE; Redis-down policy
  unstated → anon bucket, Lua/pipeline atomic, fail-open + metric + readyz
  split.
- A6: freshness fields cacheable → recomputed at serve; sanctions[]
  deduped by entity.
- A7/A8: staging retention policy + `triggered_by` audit trail → added.
- Nits applied: sqlc output committed, `{id}`=internal PK, BOM fixture,
  auth posture per endpoint, `pg_try_advisory_lock`, migration 000003 owns
  `triggered_by`+RUNNING index, `match_scope` semantics split
  (`UNRESOLVED` vs unsupported-chain `CHAIN_SPECIFIC`).

**Consensus**: `PLAN_CONSENSUS_REACHED` (Devin Critic, round 2 verdict
verified against amended files).

**Verified ground truth**: OFAC `SDN_ADVANCED.XML` structure confirmed via
live ranged fetches → `reports/ofac-xml-shape.md`; env facts (go1.27.0,
no sqlc/migrate, system PG unusable → compose on 5433/6380, git baseline)
confirmed by Critic.
