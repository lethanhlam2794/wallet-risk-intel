# Repository Guidelines

This file governs how agents work in this repository. The authoritative design
specification is `docs/tailieu.md` (Technical Design Specification v2.0). When
this file and the spec conflict, the spec wins; when both conflict with a
direct user requirement, stop and ask.

## 1. Project Overview

**Blockchain Wallet Risk Intelligence** — a Go + PostgreSQL modular monolith
that collects, normalizes, stores, and analyzes risk signals for blockchain
wallet addresses, exposed through a REST API for partner screening.

Primary networks: **Ethereum** and **BNB Smart Chain (BSC)**.

The system must answer, with evidence:

- Is the wallet on OFAC SDN or another supported sanctions list? Under which
  entity and program? What official references explain the context?
- Is it labeled SCAM / PHISHING / HACK / RANSOMWARE / MIXER / GAMBLING /
  EXCHANGE?
- Does it transact directly with risky addresses? Indirectly via how many
  hops? Which transaction hashes prove the path?
- How is the risk score computed, under which policy version?
- How fresh and how complete is the underlying data?

## 2. Tech Stack

| Layer | Choice |
|---|---|
| Language | Go |
| HTTP framework | Gin |
| Database | PostgreSQL via `pgx` |
| SQL generation | `sqlc` (queries in `queries/`) |
| Migrations | `golang-migrate` (files in `migrations/`) |
| Cache | Redis |
| API contract | OpenAPI 3.1 (`api/openapi.yaml`) |
| Logging | `slog` |
| Metrics | Prometheus |

No microservices in MVP. No paid providers without approval.

## 3. Project Structure

Follow the modular-monolith layout from spec §6:

```text
cmd/api/            REST API entrypoint
cmd/worker/         ingestion & background jobs entrypoint
internal/
  config/           configuration loading
  platform/         database, cache, logger adapters
  network/          blockchain network management
  wallet/           wallet management
  entity/           entity attribution
  sanctions/        sanctions intelligence (handler/service/repository/model)
  attribution/      attribution workflows
  label/            wallet label domain
  screening/        screening orchestration
  scoring/          risk scoring engine
  tracing/          transaction graph traversal
  transaction/      transaction indexing
  provider/         external adapters: ofac, walletattribution, goplus,
                    chainabuse, publicaml, blockscout
  ingestion/        ingestion service/worker/scheduler
  middleware/       auth, rate limiting
migrations/         SQL migrations
queries/            sqlc queries
api/openapi.yaml    API contract
configs/            configuration files
tests/              cross-package integration tests
```

Layering rule: keep HTTP handlers thin; business rules in services;
persistence in repositories; provider-specific mapping inside the owning
adapter under `internal/provider/<name>/`.

## 4. Build, Test, and Development Commands

The Go module is not yet scaffolded. Once `go.mod` exists, baseline commands:

- `go run ./cmd/api` — run the REST API locally
- `go run ./cmd/worker` — run ingestion and background jobs
- `go build ./...` — compile every package
- `go test ./...` — run unit and integration tests
- `go test -race ./...` — detect concurrency defects
- `go vet ./...` — static analysis
- `gofmt -l .` — verify formatting (must be empty)

Do not claim these commands pass until `go.mod` and the referenced packages
exist. Add a `Makefile` when the module is initialized.

## 5. Domain Invariants — Never Violate

These are the core product rules (spec §1.2, §21). Breaking any of them is a
serious defect.

- **Never equate "no data" with "safe".** Unknown wallet ≠ clean wallet.
- **Never equate a non-match on sanctions with a safe wallet.**
- **Never invent sanctions reasons, entity names, record IDs, official
  notices, or source references.** If the official source does not publish a
  reason: `reason_summary = null`, `reason_status = NOT_AVAILABLE`.
- **Never attribute wallet ownership from transaction exposure alone.** A
  graph path does not prove identical fund flow, shared ownership, or direct
  transfer.
- **Never treat a direct-label and an indirect exposure as the same thing.**
  Keep direct attribution, direct exposure, and indirect exposure separate
  end-to-end.
- **Never turn an ETH listing into a BSC listing.** A `CHAIN_SPECIFIC` OFAC
  record stays chain-specific. Cross-chain address observations may be
  reported but must be clearly distinguished from direct listings.
- **Never convert provider failure into a clean result.** Timeouts, 429s,
  degraded responses, and malformed payloads produce `PARTIAL`/`FAILED`
  status with provider metadata — not a pass.
- **Never treat `risk.score = 0` or a `0` field from a provider as safe**,
  especially under `degraded` status.
- **Never auto-verify community/provider reports.** `reported` stays
  `reported`; only evidence upgrades `verification_status`.
- **GAMBLING and EXCHANGE are classifications, not crimes.** They do not
  raise risk score by default; EXCHANGE is entity information only.
- **Provider risk scores are not internal risk scores** and are not ground
  truth.
- **Do not assume public RPC supports historical token-transfer queries**;
  BEP-20/ERC-20 history may require event logs or explorer indexing APIs.
- **Do not assume free-tier terms permit commercial use**; check license and
  quota per source before integration.

## 6. Data Integrity & Ingestion Rules

### Provenance

- Every label, sanction record, and observation keeps: original source,
  original label, source URL, evidence payload, observation time, and
  ingestion fingerprint.
- `wallet_labels.fingerprint` must be stable across ingestion runs; never
  upsert labels by `wallet_id` alone.
- Do not delete old evidence when a source changes.

### Snapshots (OFAC bulk and similar)

- Download to a temp file; validate HTTP status, completeness, XML validity,
  and checksum before parsing.
- Parse large XML with `encoding/xml.Decoder` streaming — never load the
  whole file into RAM.
- Stage → validate row counts and snapshot consistency → publish atomically.
- **Never replace the current snapshot** when download fails, the file is
  truncated, XML is invalid, the parser errors, or completeness cannot be
  proven.
- Mark records `revoked_at`/inactive only after a fully validated snapshot;
  never delete records after an incomplete sync.

### Transactions

- Keep raw integer amounts (`NUMERIC`); never store token amounts as float.
- Token transfers must carry the token contract / asset identifier.
- Handle blockchain reorganization (`confirmation_status`).

### Database

- Relational model first; JSONB only for metadata, raw observations, and
  volatile-shape data — never as a substitute for relations.
- `network_id` on `sanctions_wallets` is set only when the mapping is
  evidenced; `normalized_address` rules: EVM = validated 20-byte hex,
  lowercase for indexing, original representation preserved.

## 7. Provider Adapters

Contracts (spec §13): `Provider` (on-demand lookup), `BulkSource` (batched),
`SnapshotSource` (file download + import). Not every provider implements
every interface — do not force incompatible providers into one workflow.

Each provider requires: timeout, concurrency limit, rate limit, bounded
retry (honor `Retry-After`, exponential backoff, no infinite retries),
circuit breaker, and failure metrics.

Registered sources:

| Source | Type | Role |
|---|---|---|
| OFAC SDN Advanced XML | SnapshotSource | Primary sanctions data |
| prettydeath/wallet-attribution | BulkSource | Public wallet labels |
| GoPlus Security | Provider | Address security signals |
| Chainabuse | Provider (optional) | Community reports |
| PublicAML | Provider (optional) | Risk enrichment |
| Blockscout / explorer APIs | Provider | Transaction data |
| SanctionsAI | dev-only cross-check | Never a primary source |

## 8. Screening & Risk Scoring

- Scoring policy is **versioned**; every `screening_results` row stores
  `scoring_policy_version`. Scores must be reproducible by version.
- Score range 0–100, heuristic not probabilistic. `risk_score` may be NULL
  when data is insufficient (`INSUFFICIENT_DATA` status).
- Screening statuses: `COMPLETED`, `PARTIAL`, `INSUFFICIENT_DATA`, `FAILED`.
- Direct sanctions carry a flag independent of the numeric score.
- No double-counting duplicate evidence; no stacking points from mirror
  sources.
- `POST /screenings` is idempotent via `Idempotency-Key`.
- Tracing: bounded BFS, 1-hop/2-hop only in MVP, with max hops/nodes/edges,
  query timeout, and response size limits. Every path edge keeps tx evidence
  (hash, transfer type, asset, amount_raw, block data); every path reports
  coverage/truncation metadata.

## 9. API Conventions

- Base URL `/api/v1`; auth via API key or JWT; admin endpoints need a
  separate role.
- JSON responses, UTC timestamps, stable error codes, cursor pagination,
  request ID on every log line.
- Endpoint surface: `/networks`, `/wallets/...`, `/sanctions/...`,
  `/screenings`, `/admin/ingestion-runs` — see spec §9 for contracts.
- Contract changes require updating `api/openapi.yaml` with examples.

## 10. Caching

- Key patterns: `wallet:labels:{net}:{addr}`,
  `wallet:sanctions:{net}:{addr}:{snapshot_version}`,
  `wallet:screening:{net}:{addr}:{policy_version}`,
  `provider:{source}:{net}:{addr}`.
- Sanctions cache embeds the snapshot version and is invalidated on publish;
  never serve stale sanctions data as current.
- Separate positive and negative cache entries; never cache provider errors
  as clean results; prevent stampedes; return freshness metadata.

## 11. Coding Style

- `gofmt` formatting (tabs). Package names short, lowercase, singular.
- Exported `PascalCase`; unexported `camelCase`; tests in `*_test.go` beside
  source.
- Use `context.Context` cancellation end-to-end; bounded timeouts on all
  external calls.
- Parameterized SQL only (sqlc/pgx) — no string-interpolated queries.
- Keep provider quirks inside the owning adapter; keep domain logic free of
  HTTP/provider details.

## 12. Security

- Never commit secrets or `.env` files; never log API keys or credentials.
- Chain-specific address validation on all inputs.
- SSRF protection: fixed allowlist for provider endpoints; download timeout
  and size limits; XML parser resource limits; safe temp-file handling.
- Client rate limiting, request size limits, role-based admin access, audit
  logging.

## 13. Testing Guidelines

- `testing` package, table-driven tests for shared behavior.
- Unit tests beside source; integration/DB tests under `tests/` against a
  **dedicated test database** — never the dev database.
- Required coverage areas (spec §18): address normalization, OFAC
  identifier/entity/program/reference extraction, fingerprint stability,
  scoring, idempotent upsert, snapshot staging/publish, cache invalidation,
  graph queries.
- Failure tests are first-class: interrupted download, truncated/malformed
  XML, schema drift, worker crash, concurrent ingestion, 429/timeout/
  malformed provider responses, reorg, disappearing listings, conflicting
  labels.
- Acceptance bar: unknown ≠ safe; reasons never invented; reported ≠
  verified; exchange ≠ risky; 1-hop ≠ ownership; repeated ingestion creates
  no duplicates; provider failure visible to API consumers; scores
  reproducible by policy version.
- Never write mocks that distort real provider/driver return contracts.

## 14. Commit & Pull Request Guidelines

- Conventional Commits, e.g. `feat(screening): add exact OFAC match`.
  Focused commits, no AI references.
- PRs explain intent, affected modules, verification commands, migration and
  rollback impact, and linked issues; include OpenAPI examples for contract
  changes.

## 15. Stop And Request Review Before

- Changing database architecture or migration strategy.
- Adding a paid provider or a chain not yet supported.
- Changing the risk scoring policy.
- Introducing automatic enforcement decisions.
- Expanding tracing beyond agreed scope (3+ hops, cross-chain).
- Introducing microservices.

## 16. Implementation Order

Spec §19 defines phases: (1) foundation + auth, (2) OFAC sanctions
intelligence, (3) public wallet labels, (4) live enrichment, (5) risk
scoring, (6) transaction tracing, (7) advanced tracing — phase 7 requires
separate approval. Build in order; do not scaffold later phases early.

## 17. Local Tooling

Local Codex support lives in `.codex/`:

- `.codex/skills/` — backend-development, databases, cook, scout,
  ck-code-review, ck-security, devops-workflow, qa-automation, planner,
  project-structure, request-normalizer, senior-reviewer, doc-reporter,
  preview, team.
- `.codex/agents/code_reviewer.toml` — read-only reviewer for high-risk or
  cross-module changes.
- `.codex/rules/` — `default.rules`, `engineering-standard.md`.

Use the backend/database/security skills for matching work and the
code-review agent before risky merges.
