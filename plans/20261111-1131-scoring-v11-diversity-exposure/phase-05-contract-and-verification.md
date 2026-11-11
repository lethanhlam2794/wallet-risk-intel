---
phase: 5
title: "OpenAPI contract + full verification gates"
status: done
priority: P2
dependencies: [1, 2, 3, 4]
---

# Phase 5: OpenAPI contract + full verification gates

## Overview

Publish the contract deltas in `api/openapi.yaml` (request option, new
response fields/enums), run the full verification gate suite, and
record the v1-parity + A/B evidence required by the prompt's report
section.

## Requirements

- Functional:
  - `api/openapi.yaml`:
    - `ScreeningOptions` gains `policy_version` (string, enum
      `["v1.0.0","v1.1.0"]`, optional — server default applies when
      absent) with A/B description + example.
    - `ScreeningScoring.policy_version` enum `["v1.0.0"]` →
      `["v1.0.0","v1.1.0"]` (A12).
    - `ScreeningExposure` (the screening exposures item — NOT the
      tracing `ExposureItem`) gains optional fields: `source_address`,
      `meaningful_tx_count`, `tx_hashes` (array), `coverage`
      (enum `complete|partial`), `reason`, `taint_kind`
      (enum `direct|chain`); `basis` enum →
      `[provider_reported, onchain_computed]`; `direction` documented
      as `INBOUND|OUTBOUND` normalized vocabulary (A12).
    - `ScreeningCoverage` gains `onchain_coverage`
      (enum `complete|partial|absent`), `sources_evaluated`,
      `sources_receiver_unchecked`.
    - Reason-code documentation updated for the new codes
      (`EXPOSURE_ONCHAIN_SOURCE`, `EXPOSURE_PROVIDER_SOURCE`,
      `EXPOSURE_INFORMATIONAL`, `PROCEEDS_RECEIVER`,
      `POSSIBLE_VICTIM_CONTACT`, `SERVICE_CLASSIFIED_CANDIDATE`).
    - Examples updated (one v1.1 exposure block example).
  - `.env.example`/deployment docs: `SCORING_POLICY` +
    `SCORING_ASSET_PRICES` documented (default, fail-closed semantics,
    reproducibility note for price overrides).
- Non-functional: every contract addition has an example; no removed
  or retyped fields (additive only).

## Related Code Files

- Modify: `apps/api/api/openapi.yaml`
- Modify: `apps/api/.env.example` (or equivalent config doc),
  `AGENTS.md`-adjacent docs only if already listing env vars

## Implementation Steps

1. Inspect `openapi.yaml` screening components; add fields + enums +
   example in existing style.
2. Validate YAML parses (the repo's openapi validation path — `go
   build` embeds? check for a lint target; else `python -c yaml.safe_load`
   or `docker run --rm -v … openapi-spec-validator` if present).
3. Full gate: `cd apps/api && sqlc generate && gofmt -l . && go build
   ./... && go vet ./... && go test ./...`
4. A/B evidence: run service-level test asserting same seeded inputs →
   different verdict under v1.1 vs v1 AND identical under v1 twice;
   record outputs in `reports/`.
5. Update `plan.md` phase statuses + Debate Summary; write
   `reports/final-verification.md` with command outputs.

## Success Criteria

- [ ] `openapi.yaml` valid; new fields present with examples
- [ ] All gate commands exit 0 (sqlc idempotent — regenerated diff empty
      or only additive)
- [ ] `gofmt -l .` output empty
- [ ] `go test ./...` 0 failures incl. `tests/` on the dedicated test DB
- [ ] `reports/final-verification.md` contains: files changed, tests run
      + results, v1 parity proof, deviations (expected: none)

## Risk Assessment

- Contract drift between code and spec → handler/response fields driven
  by the same Go structs; test in `screenings_api_test.go` asserts new
  fields serialize.
- Env-doc drift → `.env.example` additions minimal and mirrored in
  config comments.
