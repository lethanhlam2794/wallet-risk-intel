---
phase: 5
title: "Gates + documentation refresh"
status: completed
priority: P2
dependencies: [4]
---

# Phase 5: Gates + Docs

## Overview

Run the full gate suite, update `docs/non-evm-audit.md` marking what
this change resolved vs what remains §15-gated, and write the final
report.

## Requirements

- `gofmt -l .` → empty; `go vet ./...`, `go build ./...`,
  `go test ./...` → exit 0 (run from `apps/api/`).
- `docs/non-evm-audit.md` marks the solana dormant adapter + semantics
  doc as shipped groundwork and lists remaining §15 items.
- `reports/final-report.md`: phase checkpoints, evidence, file list,
  calls-per-ensure recap, stop-condition ledger (every stop condition
  confirmed not triggered, or surfaced).

## Implementation Steps

1. Run all gates; capture outputs into `reports/gate-evidence.md`.
2. Update audit doc (dormant adapter exists; remaining blockers).
3. Sanity-verify dormancy: `solana` still `is_active=FALSE`, adapter not
   imported by `cmd/api` or `cmd/worker` (`grep`/`go list -deps`).
4. Write `final-report.md` + `reports/session-log.md` (worker session
   ids for audit).

## Success Criteria

- [x] All four gate commands exit 0 with captured evidence.
- [x] Audit doc updated; no §15 item silently closed.
- [x] `/networks` still returns bsc+eth only (documented assertion).

## Risk Assessment

Low — verification phase. Main risk is skipping a gate; all four are
mandatory with captured exit codes.
