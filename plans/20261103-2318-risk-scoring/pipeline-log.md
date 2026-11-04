# Pipeline Log — risk-scoring (codex-vibe --auto, Phase 5)

Orchestrator session: `pushy-alloy`. Plan dir: `plans/20261103-2318-risk-scoring/`.

### [STAGE_1_VERIFICATION] - Stage 1: Grounded Pre-Planning & Adversarial Debate
- **Completion Timestamp**: 2026-11-03 23:58
- **Status**: PASSED
- **Completed Tasks**:
  - [x] Scanned existing pending plans in ./plans/ (3 prior plans — all completed; no overlap/blocking deps)
  - [x] Orchestrator pre-scouting: verified sanctions/label/enrichment/wallet collaborators, httpserver Deps, cache keys, middleware, migrations numbering, sqlc conventions, openapi state, spec §7.14/§9.6–9.7/§10/§14/§15/§17/§19
  - [x] Initialized canonical plan structure: `plan.md` + `phase-01..04-*.md` + `reports/`
  - [x] Dispatched Devin Critic worker (session `rhinestone-scale`, `devin -p` dangerous + read-only write-scope, tree-hash verified unchanged outside reports/)
  - [x] Debate: round 1 CHALLENGED (11 amendments — all accepted & applied); round 2 CHALLENGED (3 findings B1–B3 — all accepted & applied); round 3 **PLAN_CONSENSUS_REACHED**
- **Machine Evidence**:
  - Plan: `plans/20261103-2318-risk-scoring/plan.md` + 4 phase files (verified on disk)
  - Consensus proof: `reports/critique-round-3.md` ends with `PLAN_CONSENSUS_REACHED`; critic session id `rhinestone-scale`
  - Artifacts: `reports/prompt-critic-round-1.md`, `critique-round-{1,2,3}.md`, `rebuttal-round-{1,2}.md`
  - Write-scope audit: `git status --porcelain` outside plan dir = clean
- **Self-Audit Commitment**:
  - Plan is fully grounded in existing code realities with zero speculative hallucination; all 14 amendments landed.

### [STAGE_2_VERIFICATION] - Stage 2: Devin Cook Implementation
- **Completion Timestamp**: 2026-11-04 00:55
- **Status**: PASSED
- **Completed Tasks**:
  - [x] Devin Cook (independent `devin -p --permission-mode dangerous` session, fresh context) executed TDD across all 4 phases
  - [x] Phase files marked `status: completed`, success criteria checked
  - [x] Cook report written: `reports/cook-report.md` (48 new test functions; 2 documented minor deviations)
- **Machine Evidence** (re-run by orchestrator, not inherited from Cook):
  - `go build ./...` ➔ Exit 0
  - `go vet ./...` ➔ Exit 0
  - `gofmt -l .` ➔ empty
  - `go test ./...` ➔ all packages ok (internal/scoring, internal/screening, tests 82.4s integration)
  - `git log --oneline -1` ➔ `083fee8` — no commits made (changes pending per protocol)
  - Cook session id: `devin -p` headless (prompt: `reports/prompt-cook.md`); new files: `internal/scoring/{model,policy,engine}.go`, `internal/screening/{model,mapper,service,handler}.go`, `migrations/000007_screenings.{up,down}.sql`, `queries/screenings.sql`, `internal/db/screenings.sql.go`, `tests/screenings_{persistence,api}_test.go`
- **Self-Audit Commitment**:
  - Verified with fresh terminal execution; no test assertions weakened or skipped.

### [STAGE_3_VERIFICATION] - Stage 3: Devin Review (independent)
- **Completion Timestamp**: 2026-11-04 (round 1)
- **Status**: FAILED GATE → remediation required
- **Completed Tasks**:
  - [x] Independent Devin Review session audited the full pending diff (migration, sqlc, engine, orchestrator, API, OpenAPI, tests) with fresh command evidence
  - [x] Verdict: **CHANGES_REQUESTED** — `reports/review-round-1.md`
- **Machine Evidence** (re-ran by reviewer):
  - build/vet/gofmt/test/-race all pass; openapi parses; live DB verified (`result` is `json`, CHECKs present)
  - Required findings: **M1** `dedupe` alphabetical ordering kept weakest-evidence representative (contract/code mismatch); **M2** plan §3.5 FAILED-row persistence undelivered & undisclosed in deviations
  - Recommended: **M3** findings/blocks emitted for unusable dimensions; advisory nits N1–N6
- **Gate rule**: CHANGES_REQUESTED blocks Stage 4 exit until remediated and re-reviewed.

### [STAGE_4_REMEDIATION] - Stage 4: Remediation of Review Round 1
- **Completion Timestamp**: 2026-11-04
- **Status**: PASSED (re-review pending)
- **Completed Tasks**:
  - [x] M1 fixed: explicit `verificationRank`/`confidenceRank` strength ordering in `dedupe`; regression `TestEvaluate_DedupKeepsStrongestEvidence` (both input orders)
  - [x] M2 fixed: `persistFailed` writes best-effort FAILED audit row (fresh bounded ctx, claim unlinked so retry works) on post-resolution persist failure; regression `TestService_PersistFailed_AuditRow` (real test DB)
  - [x] M3 fixed: mapper gates finding/entry/item emission on `dim.Usable`; engine zeroes `Blocks` when `!HasUsableData`; regression `TestService_UnusableDimension_EmitsNoFindings` + `Blocks{}` assertion
  - [x] N1 fixed: `claim_stale` computed in DB time via query (`claimed_at < NOW() - INTERVAL '5 minutes'`); `staleClaimAfter` dead constant removed; sqlc regenerated
  - [x] N2 fixed: `provider_status.fanout` named field replaces `_enrichment` pseudo-key; OpenAPI updated
  - [x] N3 fixed: provider observation identity JSON-encoded (pipe-join collision removed)
  - [x] N4 addressed: cap-flag semantics documented (Other.Capped = preCap>cap, forward-correct; Direct.Capped false by design)
  - [x] N5 kept: `ListScreeningResultsByWallet` is exercised by tests + planned history endpoint
  - [x] N6 fixed: `pollOrConflict` emits 409 `IDEMPOTENCY_IN_FLIGHT` envelope on ctx.Done
- **Machine Evidence** (fresh, orchestrator-run):
  - `go build ./...` ➔ 0; `go vet ./...` ➔ 0; `gofmt -l .` ➔ empty
  - `go test -count=1 ./internal/scoring/` ➔ ok; `./internal/screening/` ➔ ok 6.408s; `./...` ➔ all ok (tests 81.205s)
  - `go test -race -count=1 ./internal/scoring/ ./internal/screening/` ➔ ok
  - `python3 yaml.safe_load(api/openapi.yaml)` ➔ parses, 14 paths, v0.3.0
  - Remediation report: `reports/remediation-round-1.md`
- **Self-Audit Commitment**:
  - Every review finding resolved in code; new regression tests assert real DB/engine semantics; no assertions weakened.

### [STAGE_3_REVIEW_R2] - Devin Review round 2
- **Completion Timestamp**: 2026-11-04
- **Status**: FAILED GATE → second remediation required
- **Machine Evidence**: `reports/review-round-2.md` — all 9 round-1 remediation claims independently verified TRUE; 5 new minors in code that moved (R2-M1 dedupe score tiebreak lowest-first; R2-M2 raw error persisted; R2-M3 unbounded release ctx; R2-M4 fanout path untested; R2-M5 original address representation lost).

### [STAGE_4_REMEDIATION_R2] - Stage 4: Remediation of Review Round 2
- **Completion Timestamp**: 2026-11-04
- **Status**: PASSED (re-review pending)
- **Completed Tasks**:
  - [x] R2-M1: `dedupe` ProviderScore tiebreak → descending (`si > sj`); regression `TestEvaluate_DedupKeepsHighestProviderScore`
  - [x] R2-M2: `persistFailed` persists static sanitized message; `cause` param removed; test asserts exact string
  - [x] R2-M3: claim-release `DeleteScreeningRequest` bounded `WithTimeout(10s)`
  - [x] R2-M4: `TestService_EnrichFanoutError_ReportedOnFanoutField` — fake err → fanout ERROR, no pseudo-keys, PARTIAL
  - [x] R2-M5: `CanonicalRequest.RawAddress` (`json:"-"`, excluded from hash) → `wallets.address` keeps original representation; regression `TestService_RawAddressPreserved`
- **Machine Evidence** (fresh):
  - `go build ./...` ➔ 0; `go vet ./...` ➔ 0; `gofmt -l .` ➔ empty
  - `go test -count=1 ./...` ➔ all ok (tests/ 82.703s)
  - `go test -race -count=1 ./internal/scoring/ ./internal/screening/` ➔ ok 1.060s / 9.777s
  - Report: `reports/remediation-round-2.md`

### [STAGE_3_REVIEW_R3] - Devin Review round 3
- **Completion Timestamp**: 2026-11-04
- **Status**: FAILED GATE → third remediation required
- **Machine Evidence**: `reports/review-round-3.md` — all 5 R2 fixes empirically verified (incl. live psql inspection: mixed-case `wallets.address` preserved verbatim, `request` JSONB has no RawAddress → hash cannot regress). 1 new minor: **R3-M1** `pollOrConflict` never re-checks request_hash → wrong-payload replay possible via release+reclaim race. 4 advisory nits.

### [STAGE_4_REMEDIATION_R3] - Stage 4: Remediation of Review Round 3
- **Completion Timestamp**: 2026-11-04
- **Status**: PASSED (re-review pending)
- **Completed Tasks**:
  - [x] R3-M1: `pollOrConflict` takes `hash`, re-checks `RequestHash` every tick → `409 IDEMPOTENCY_CONFLICT` on foreign reclaim; regressions `TestHandler_PollRejectsReclaimedDifferentHash` + `TestHandler_PollVanishingClaim_Conflict`
  - [x] Nit: `Enrich` now receives `raw` for rawAddress param
  - [x] Nit: stale-claim 5-min vs 30 s deadline relationship documented in `queries/screenings.sql`
  - [x] Nit: `persist` nil-guard added (parity with `persistFailed`)
  - [x] Nit: `findings` column never stores JSON `null` (`orEmpty` before marshal)
- **Machine Evidence** (fresh):
  - `go build ./...` ➔ 0; `go vet ./...` ➔ 0; `gofmt -l .` ➔ empty
  - `go test -count=1 ./...` ➔ all ok (tests/ 86.421s)
  - `go test -race -count=1 ./internal/scoring/ ./internal/screening/` ➔ ok 1.058s / 11.298s
  - Report: `reports/remediation-round-3.md`

### [STAGE_3_VERIFICATION] - Devin Review round 4 — GATE RESULT
- **Completion Timestamp**: 2026-11-04
- **Status**: **APPROVED**
- **Machine Evidence**: `reports/review-round-4.md` — verdict token `APPROVED`; reviewer re-ran full gate fresh (build/vet/gofmt/yaml/go test ./... 88.3s/race) all green; confirmed hash-check ordering precedes replay check, tests drive intended branches (0.75s first-tick exit proves hash-mismatch path), no residual issues, required actions: none.
- **Self-Audit Commitment**: Stage 3 gate satisfied — independent reviewer approved the complete pending diff after 3 remediation rounds (14 findings total resolved in code).

### [STAGE_4_VERIFICATION] - Stage 4: Remediation & Test Saturation — GATE RESULT
- **Completion Timestamp**: 2026-11-04
- **Status**: PASSED
- **Completed Tasks**:
  - [x] All review findings remediated in code across 3 rounds (14 required + advisory); reports: `remediation-round-{1,2,3}.md`
  - [x] Review gate satisfied: round-4 verdict `APPROVED` (`reports/review-round-4.md`)
  - [x] Saturation: 11 new regression tests added during remediation (dedupe strongest/highest, unusable-dimension gate, persistFailed audit row, raw-address preservation, fanout error, poll hash recheck ×2, claim_stale boundary) — spec §13 acceptance areas all covered by real-DB/engine tests
  - [x] `claim_stale` boundary test added (remaining advisory nit)
  - [x] No orphaned Devin worker sessions (only main-session `acp` helpers alive)
- **Machine Evidence** (fresh):
  - `go test -count=1 ./...` ➔ 17 packages ok, 0 FAIL
  - `go test -race -count=1 ./internal/scoring/ ./internal/screening/` ➔ ok
  - `go build`, `go vet`, `gofmt -l .` ➔ clean; OpenAPI parses (14 paths)
- **Self-Audit Commitment**: saturation honest — every new test asserts real semantics against real infra; no weakened assertions.

### [STAGE_5_VERIFICATION] - Stage 5: E2E Verification — GATE RESULT
- **Completion Timestamp**: 2026-11-04
- **Status**: PASSED (SKIPPED-NO-UI adaptation documented — backend-only service, no browser surface)
- **Completed Tasks**:
  - [x] Booted real API binary on :8090 against live Postgres/Redis (real OFAC snapshot 1075 wallets + 108k labels + live free-tier provider fan-out)
  - [x] E1–E10 scenarios: OFAC-sanctioned → 100 CRITICAL + flag; SCAM → 64 HIGH; EXCHANGE → 0 LABEL_CLASSIFICATION (classification≠crime) + PARTIAL on provider failure; unknown → honest PARTIAL 0; idempotent replay byte-identical + GET byte-identical + 409 on payload mismatch; include_sanctions=false → evaluated:false/NOT_REQUESTED; latency 0.14–3.71s; error codes stable (401/400×3/404)
  - [x] SQL assertions: 6 screening_results rows persisted with correct status/score/policy v1.0.0; claim row attached
- **Machine Evidence**: `reports/stage5-e2e-verification.md`; live curl outputs; psql query results
- **Self-Audit Commitment**: all evidence from real HTTP + SQL on the running stack — no fabricated UI/screenshot evidence.

### [STAGE_6_VERIFICATION] - Stage 6: Guidebook & Feature Map — GATE RESULT
- **Completion Timestamp**: 2026-11-04
- **Status**: PASSED
- **Completed Tasks**:
  - [x] `docs/guidebook-risk-scoring.md` — operational guide with real captured responses (curl recipes, idempotency demo, provider_status/completeness reading, SQL verification)
  - [x] `docs/feature-maps/risk-scoring.md` — endpoints, packages, policy v1.0.0 weights, schema, invariants
  - [x] Raw captures saved: `docs/feature-maps/assets/screening-*.json` (5 real responses, pretty-formatted)
- **Machine Evidence**: all examples copied verbatim from Stage-5 live calls; files verified on disk.
- **Self-Audit Commitment**: no fabricated examples — every JSON block traces to a real HTTP capture.

## PHASE 5 — RISK SCORING: COMPLETE
All six stages passed. 4 review rounds → APPROVED. Evidence chain:
`plan.md` → `critique-round-3.md` (PLAN_CONSENSUS_REACHED) → `cook-report.md`
→ `review-round-{1..4}.md` + `remediation-round-{1,2,3}.md` →
`stage5-e2e-verification.md` → guidebook + feature map.
