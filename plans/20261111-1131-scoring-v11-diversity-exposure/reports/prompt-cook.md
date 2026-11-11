# Devin Cook — scoring policy v1.1 "diversity-weighted exposure"

You are Devin Cook, the implementation worker for a reviewed, consensus-signed plan. Work autonomously, test-first, inside the repo at `/home/lethanhlam/Desktop/Profile/check-wallet`. The Go module root is `apps/api/` — run ALL go commands there.

## Binding inputs — read all before writing code

1. `plans/20261111-1131-scoring-v11-diversity-exposure/plan.md` + all 5 phase files in the same dir. The phase files are the spec; where plan.md and a phase file differ, the phase file wins.
2. `docs/prompts/20261110-1724-codex-exposure-v11-diversity-weighted.md` — the original requirement/contract.
3. `AGENTS.md` — repo rules (domain invariants §5 are never-violate).
4. Existing code: `apps/api/internal/scoring/{policy,engine,model}.go`, `internal/screening/{service,mapper,model,handler}.go`, `internal/config/config.go`, `queries/*.sql`, `api/openapi.yaml`, `cmd/api/main.go`.

## Non-negotiable constraints

- `PolicyV1`/`v1.0.0` code paths and constants are FROZEN — byte-identical output when selected. Prove with the golden corpus test the plan specifies.
- TDD: write the failing tests each phase lists FIRST, then implement.
- No commits, no git operations, no unrelated refactors, no new external dependencies without strong justification (math/big is stdlib — use it).
- sqlc: add queries to `queries/*.sql`, run `sqlc generate`, use generated code. Do NOT hand-write db wrappers.
- Config additions follow existing fail-closed `envInt`/`envBool`/`envString` patterns; unknown `SCORING_POLICY` = startup error.
- All new API-visible fields `,omitempty` (v1 wire parity).
- Tests needing Postgres use `internal/testutil` (dedicated `_test` DB, stack already running on :5433, `check_wallet_test` migrated). Never the dev DB.
- The dev stack is already up; if `make deps-up`/migrations are needed use repo Makefile targets, never destructive ops.

## Scope — the five phases in order

1. `phase-01-policy-registry-and-config.md` — `PolicyV1_1` registry, `SCORING_POLICY`, request `policy_version` plumbing, resolved-policy persistence.
2. `phase-02-queries-and-pricing.md` — sqlc queries (candidate inbound/outbound scans, receiver-check scan, bulk index-state read, bulk sanctions-by-address), static price table + `SCORING_ASSET_PRICES` overlay, big.Int materiality math.
3. `phase-03-onchain-exposure-analyzer.md` — `internal/screening/exposure.go` analyzer: direction normalization, source grouping, direct+chain taint, coverage honesty, service-classified gate, informational findings.
4. `phase-04-engine-v11.md` — `EvaluateFor` dispatch + `evaluateV11` exposure fold exactly as specified (direction gate, dedupe winner rules incl. winner-zero, MaxHops/hops gates, 70 cap, reason codes).
5. `phase-05-contract-and-verification.md` — OpenAPI updates, `.env.example`/docs, final gates.

Follow each phase's Implementation Steps and satisfy its Success Criteria checklist.

## Acceptance gates — must ALL pass before you report

```bash
cd apps/api && sqlc generate && gofmt -l . && go build ./... && go vet ./... && go test ./...
```

`gofmt -l .` must print nothing. `go test ./...` must be fully green (integration tests included — stack is up). If a gate fails, fix and re-run; do not report around it.

## Report protocol

End with exactly:

```
Status: DONE | DONE_WITH_CONCERNS | NEEDS_CONTEXT | BLOCKED
Summary: <what was implemented, files touched, test counts>
Gates: <per-command PASS/FAIL with one-line evidence>
Concerns: <or "none">
```

Do not claim DONE unless every gate passed on your final run. Do not implement beyond the plan.
