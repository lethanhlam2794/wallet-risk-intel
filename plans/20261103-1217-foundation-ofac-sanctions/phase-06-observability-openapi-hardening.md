---
phase: 6
title: "Observability, OpenAPI & Failure-Mode Hardening"
status: completed
priority: P2
dependencies: [4, 5]
---

# Phase 6: Observability, OpenAPI & Failure-Mode Hardening

## Overview

Production-readiness pass for the shipped surface: Prometheus metrics per
spec §17 (scope-applicable subset), `api/openapi.yaml` contract with
examples, structured-log discipline, failure-mode test suite from spec §18.3,
and `docs/` + README updates.

## Requirements

- Functional: `GET /metrics` (Prometheus); metrics:
  `sanctions_matches_total`, `sanctions_ingestion_duration_seconds`,
  `sanctions_snapshot_age_seconds` (gauge from last published),
  `ingestion_records_total{source,result}`,
  `ingestion_last_success_timestamp`, `http_requests_total{route,status}`,
  `http_request_duration_seconds`.
- `api/openapi.yaml` — OpenAPI 3.1 covering only implemented endpoints
  (`/networks`, `/wallets/{n}/{a}`, `/sanctions/...`, `/admin/ingestion-runs`,
  health) with request/response examples matching real payloads.
- Logging: request-id on every line; durations; `snapshot_version` in
  ingestion logs; **no API keys/secrets ever logged** (negative test:
  logger output contains no key material).
- Failure tests (spec §18.3 subset applicable to scope): interrupted
  download, truncated XML, malformed XML, unexpected schema, worker crash
  mid-run (run row marked FAILED), concurrent ingestion, disappearing
  listing revocation, snapshot-missing-records validation.

## Architecture

- `internal/platform/metrics/metrics.go` — registered collectors, injected
  into middleware + ingestion service.
- Snapshot-age gauge updated by a cheap ticker or computed on scrape via
  GaugeFunc against `sanctions_snapshots.published_at`.
- Worker crash semantics: run rows stuck `RUNNING` past timeout are surfaced
  via `GET /admin/ingestion-runs/{id}` as stale; service startup marks
  orphaned RUNNING rows `FAILED` (`error_message="worker restarted"`) —
  no silent zombie runs.
- Audit logging (spec §16): `ingestion_runs.triggered_by` captures the api
  client id / 'worker'; admin-triggered ingestion also emits a structured
  audit line (actor, source, run_id, request_id).
- `/metrics` requires reader key (decided in phase 02 posture).
- `README.md`: setup (docker compose, migrate, bootstrap keys, run
  api/worker, curl examples), test commands, architecture map.

## Related Code Files

- Create: `internal/platform/metrics/metrics.go`, `api/openapi.yaml`,
  `README.md`, `tests/failure_modes_test.go`
- Modify: `internal/httpserver/server.go` (metrics middleware + /metrics),
  `internal/ingestion/service.go` (metric + stale-run sweep), `Makefile`
  (`lint` target if golangci-lint present — optional)

## Implementation Steps (TDD)

1. Red: metrics test — after a fixture ingestion, `/metrics` exposes
   `ingestion_records_total` and snapshot age gauge >0.
2. Red: `failure_modes_test.go` — each §18.3 failure fixture → correct
   error path + zero live-table mutation + run marked FAILED.
3. Red: stale-run sweep — insert fake RUNNING row, restart service, row
   becomes FAILED.
4. Green: metrics wiring, openapi doc, sweep, README.
5. Validate openapi.yaml parses (CI step: python yaml load or
   `npx @redocly/cli lint` if available — fallback yaml parse).
6. Full suite: `go test ./... -race` + `go vet` + `gofmt -l .` green.

## Success Criteria

- [x] `/metrics` serves all declared collectors with real values.
- [x] Every spec-§18.3 failure case in scope has a failing-then-green test.
- [x] openapi.yaml valid; examples match live responses byte-shape.
- [x] No secrets in logs; request_id present on all request-scoped lines.
- [x] `go test ./... -race` exit 0; README commands verified verbatim.

## Risk Assessment

- Metric cardinality blowup → route labels use route patterns, not raw paths.
- openapi drift → CI-lite check: handler-level tests assert documented
  response keys exist.
- Stale-run sweep racing a live run → sweep only on boot + only rows older
  than run timeout.
