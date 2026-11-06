---
phase: 6
title: "Gates & Docs — clean build/test gates, audit doc update, final report"
status: completed
priority: P1
dependencies: [2, 3, 4, 5]
---

# Phase 6: Gates & Documentation

## Overview

Run the repo's full quality gates, update `docs/non-evm-audit.md` with
what this change resolved vs. what remains for §15, and write the final
report.

## Requirements

- Functional: all gates green; audit doc reflects reality; no acceptance
  criterion left unverified.

## Architecture

### Gates (all run from `apps/api/`)

- `gofmt -l .` → empty output
- `go vet ./...` → exit 0
- `go build ./...` → exit 0
- `go test ./...` → all pass (unit + `_test`-DB integration)

### Audit doc update (`docs/non-evm-audit.md`)

- Move resolved items out of "Per-module blockers"/"Pending §15 review":
  - `Service.GetOrCreate` signature (address_type as a parameter) → now
    resolved by Phase 4; note the new signature + fail-closed `""` rule.
- Add the NEW call site discovered: `internal/httpserver/server.go:270`
  (the audit listed `service.go:25` only) — corrected for the §15 reviewer.
- Mark the TxProvider-adapter row: adapter code exists (dormant, unwired)
  — no longer "no adapter code exists"; enablement wiring is still §15.
- Point remaining blockers at the two new docs
  (`tron-provider-decision.md`, `tron-indexer-semantics.md`): data_sources
  row, providerconfig descriptor, selector wiring, identity.go normalize
  switch, transactions.sql literal, wattrFiles/tron.csv, OpenAPI pattern,
  is_active flip, OFAC TRX mapping, warmindex check.
- Keep every still-standing invariant intact (tracer/scoring/etc.).
- Update the discovery doc's status header
  (`docs/non-evm-provider-discovery.md:3` — "no adapter code exists and
  none is authorized" is stale after Phase 5; adapter exists, dormant).
- Record the `len(chain)==0 → writeDisabled` dormant-runtime behavior
  (`transaction/service.go:296-300`) in the audit update so a reviewer
  knows the runtime shape of an unwired network (critic RA-10).

### Final report

`plans/20261106-1946-tron-enablement-prep/reports/final-report.md`:
phase-by-phase evidence, acceptance-criteria checklist mapped to tests,
drift findings, provider decision summary, remaining §15 items.

## Related Code Files

- Modify: `docs/non-evm-audit.md`
- Create: `plans/20261106-1946-tron-enablement-prep/reports/final-report.md`

## Implementation Steps

1. Run all four gates; fix anything that fails (never weaken a test).
2. Verify every acceptance criterion from the source prompt against real
   test output — checklist below.
3. Update the audit doc per the mapping above.
4. Write the final report with evidence links.

## Success Criteria (source-prompt acceptance criteria)

- [x] tron rows still `is_active=FALSE`; `/networks` still returns
      bsc+eth only (integration assertion already exists —
      `tests/non_evm_networks_test.go` must still pass)
- [x] Adapter zero-traffic test proven
- [x] Refetch determinism test proven
- [x] Ascending-order + per-stream Finished tests proven
- [x] GetOrCreate tron/evm stamping tests proven
- [x] Discovery doc: zero UNCONFIRMED TRON rows
- [x] No new external dependency; no secrets in code
- [x] All four gates clean
- [x] Audit doc updated; final report written

## Risk Assessment

The gates are the objective proof; the risk is paperwork drift — the audit
doc must not claim more than the code does. Every audit edit should cite
the code or doc that backs it.
