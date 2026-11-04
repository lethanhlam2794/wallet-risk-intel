# Pipeline Log — Phase 6: Transaction Tracing

## STAGE_1_VERIFICATION — Grounded Pre-Planning + Adversarial Debate
- [x] Plan scaffolded (`plan.md` + 4 phase files) — grounded on spec
  §4.7/§7.13/§9.9–9.10/§11/§17/§19 and repo seams (provider.go,
  resilience/, singleflight, LookupWallet, sqlc conventions).
- [x] Devin Critic adversarial debate: **9 rounds**, ~125 findings
  applied — R1 (21: dead BSC provider → Etherscan V2 + NodeReal;
  transfer_index identity; watermark semantics), R2 (22: self-contained
  plan; CU economics; 100k cap; DO-UPDATE revalidation), R3 (21:
  content-derived `.{k}` identity; tail-refetch; prefix emission;
  is_error edges; root coverage), R4 (3+7+12: span-capped window;
  persisted coverage columns + derived stale; max-per-stream counting),
  R5 (1+4+17: DISABLED lifecycle + false-ok provider_status;
  matched_signals per-kind + LEGAL; head<fetch_from; direction="self";
  named TTL knobs), R6 (6+13: stale on /transactions; max_hops=1
  arithmetic; cross-depth dedup; indexed_at terminal-write; head_block;
  phase sync), R7 (2+4: total order key; per-endpoint direction enums),
  R8 (1+1: sentinel item rank; watermark label), R9: clean.
- [x] `PLAN_CONSENSUS_REACHED` — reports/critique-round-9.md.
- [x] Key decisions frozen: chainscan(Etherscan V2)/nodereal(nr_
  getAssetTransfers) adapters; lazy incremental convergence
  (~1300-call BSC cold start declared); content-hash transfer identity;
  contiguous-prefix bounded BFS; coverage-honest envelopes on both
  endpoints; INTERNAL transfers excluded (declared deviation).

## STAGE_2_VERIFICATION — Devin Cook implementation
- [x] cook-report.md written (moved into reports/ — Cook wrote to repo root).
- [x] Artifacts: migration 000008 (transactions + wallet_index_state +
  data_sources seeds); queries/transactions.sql + transactions.sql.go;
  TxProvider seam (provider.go — LookupTransfers/HeadBlock/SourceCode/
  Enabled); internal/provider/{chainscan,nodereal} on resilience client;
  internal/transaction/{model,service,identity,list}; internal/tracing/
  {model,service}; httpserver/transactions.go; cmd/api/main.go wiring;
  config keys; OpenAPI + screening enum + 5 regenerated captures.
- [x] Independent gates re-run by orchestrator: `gofmt -l .` = 0 files;
  `go build ./...` pass; `go vet ./...` pass; `go test ./...` all green;
  `go test -race` on transaction+tracing+tests all green (Cook found+fixed
  a real race on edgesExamined — atomic); `sqlc generate` clean.
- [x] Wire enum verified: `index_status` values `{fresh,partial,...}`
  match plan (cook-report's prose "indexed_fresh" refers to outcome
  names, not wire values).

## STAGE_3_VERIFICATION — round 1: FAILED → remediated

- Reviewer verdict: **CHANGES_REQUESTED** — 1 blocker (per-stream
  watermark over-claim), 6 majors, 10 minors, 6 nits
  (`reports/review-round-1.md`).
- Remediation applied in-place (Stage 4 protocol): all 23 findings
  resolved; companion latent bug fixed (unfinished stream could earn
  `fresh`). Regression tests added per fix — see
  `reports/remediation-round-1.md`.
- Gate re-run: gofmt/build/vet/sqlc/test/race/openapi — all green.
- → launching review round 2.

## STAGE_3_VERIFICATION — round 2: FAILED → remediated

- Verdict: **CHANGES_REQUESTED** — all r1 fixes verified genuine; new
  findings: N1 major (zero-page budget stop over-claims fetch_to), N2
  (recordCap on short pages), N3 (M2 test didn't reach the branch),
  N4/N5 nits (`reports/review-round-2.md`).
- Remediation: `reports/remediation-round-2.md`. Package tests green.
- → launching review round 3.

## STAGE_3_VERIFICATION — round 3: PASSED

- Verdict: **APPROVED** (`reports/review-round-3.md`) — all r2 fixes
  verified in code; no blocker/major; re-hunt clean across both
  terminal-switch arms, capped-token replay, CAS liveness, ordering
  keys, M2 test determinism.
- Non-blocking follow-ups closed: O1 discriminating test added
  (zero-page budget stop), O3 ordering contract pinned on
  TransferPage; O2 deferred to Phase 7.
- Stage 4 saturation status: regression coverage now discriminating
  per fix; full gate green (gofmt/build/vet/test/race/sqlc/openapi).

## STAGE_4_VERIFICATION — PASSED (folded into round-3 closure)

## STAGE_5_VERIFICATION — PASSED

E2E against the real API (built binary, dev DB :5433, Redis :6380,
no provider keys — honest disabled lifecycle):

- `GET /wallets/ethereum/{w}/transactions` → `index_status=disabled`,
  `provider_status="disabled"`, `items:[]` (never null), zero rows,
  `index_scope="native+token"`.
- `GET .../exposures` → `items:[]`, `provider_calls:0`, full root
  coverage block, sanctions+labels snapshot versions populated.
- Persisted state (SQL): `wallet_index_state` row `status=disabled`,
  `error='disabled'`, `rows_indexed=0`.
- Error matrix (8 cases): INVALID_NETWORK, INVALID_ADDRESS,
  BAD_REQUEST (max_hops 0|3, limit=0, direction, from_timestamp),
  INVALID_CURSOR — all 400 with distinct codes.
- `refresh=true` under disabled provider → still `disabled` (no fake ok).
- Latency: /transactions ~10ms, /exposures ~20ms (disabled path).
- Real-index E2E covered by API test suite (real Gin engine + real
  test DB + stubbed upstream): items, self-row, cursor, filters,
  bounded_bfs coverage — `tests/transactions_api_test.go`.

## STAGE_6_VERIFICATION — PASSED

- Guidebook: `docs/guidebook-transaction-tracing.md` (architecture,
  endpoints, index honesty contract, identity, exposure semantics,
  config surface, failure playbook).
- Feature map: `docs/feature-maps/transaction-tracing.md` + live
  captures `assets/live-{transactions,exposures}-disabled.json`.

## PHASE 6 — COMPLETE (all 6 stages PASSED)

Review trail: round 1 CHANGES_REQUESTED (B1+6M+10m+6n) → round 2
CHANGES_REQUESTED (N1+2m+2n) → round 3 APPROVED. Remediation reports:
`reports/remediation-round-{1,2}.md`. Gates green: gofmt/build/vet/
test/race/sqlc/openapi.
