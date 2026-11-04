You are Devin Cook, responsible for executing the implementation plan in:
`/home/lethanhlam/Desktop/Profile/check-wallet/plans/20261104-1100-tx-provider-expansion/plan.md`

Working directory: `/home/lethanhlam/Desktop/Profile/check-wallet`

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md
- Engineering standard: plans/20261104-1100-tx-provider-expansion/reports/compliance/engineering-standard.md
- Learning ledger: plans/20261104-1100-tx-provider-expansion/reports/compliance/learning.md
- Reports dir: plans/20261104-1100-tx-provider-expansion/reports/
- Authoritative spec: docs/tailieu.md (AGENTS.md §0: spec wins on conflict)
If AGENTS.md or learning.md is unreadable: STOP and return `Status: NEEDS_CONTEXT` — do not guess paths or proceed.

## Mission

Implement phases 01→12 SEQUENTIALLY (plan roadmap Depends column is
authoritative — phases share config.go/main.go/.env.example/migration
number space). Eleven `provider.TxProvider` adapters + an ordered
per-network failover chain, per the approved plan.

## Environment facts (verified by orchestrator — do not re-litigate)

- Go module exists; `go build ./...`, `go vet ./...`, `gofmt -l .` must stay clean.
- Postgres: `postgres://wallet:devonly@127.0.0.1:5433/check_wallet` —
  RUNNING (docker compose). Redis: `redis://127.0.0.1:6380` — RUNNING.
- Test DB MUST end in `_test` (testutil hard-fails otherwise). Packages
  running parallel under `go test ./...` MUST use a package-private DB
  (e.g. `check_wallet_txp_test`) and private Redis DB index — the shared
  `check_wallet_test` is TRUNCATEd by every pool caller (P-parallel
  hazard, learned the hard way in live-enrichment run).
- Migrations: `make migrate-test` applies to `check_wallet_test`;
  sqlc generation via `make sqlc` (queries live in `queries/`).
- `.env` holds ONLY `ETHERSCAN_API_KEY` + `NODEREAL_API_KEY`. Every other
  keyed adapter is `live-verify: BLOCKED` — implement + mock + gates,
  mark BLOCKED honestly, keep OUT of every default `TX_PROVIDER_*`
  selector (defaults policy, not lockout). Blockscout (keyless) and the
  blockchair keyless probe CAN live-verify/probe.
- Existing TxProvider reference impls: `internal/provider/chainscan`
  (ETH/etherscan-family) and `internal/provider/nodereal` (BSC). Follow
  their adapter.go/mapper.go/testdata shape, resilience.NewClient
  config, and hardcoded SSRF allowlists.

## Execution rules

1. **Strict TDD per phase**: Red (fixture tests parsing the REAL wire
   shape for that transport — JSON-RPC→positional array, REST→method+
   path+query+auth) → Green → Refactor. Mocks must parse real wire
   shapes; never weaken/skip tests for green.
2. **Docs-first per adapter**: verify the CURRENT upstream contract
   against official docs BEFORE writing the mapper; docs win over
   assumptions — surface conflicts in the phase report.
3. **Per-phase conventional commit**: `feat(provider): add <name>
   transaction-history adapter` (or `feat(txindex): ...` for phase 12).
   Continuous flow — NO per-phase stop/report checkpoint; one
   consolidated final report.
4. **Tier-2 (07–11)**: STEP 0 viability probe first; probe-FAILED →
   `reports/<name>-viability.md` with `NOT-VIABLE: <reason>` → phase
   DONE (legitimate completion). Probe-BLOCKED (no key) ≠ NOT-VIABLE →
   merge-unverified path.
5. **Phase-12 gate**: write `reports/phase-12-design.md` answering
   D1–D5 BEFORE any failover code. D1 is pre-decided (a) schema re-key
   `(network_id, wallet_id, source_id)`; D2–D5 choices are yours to make
   and document — record trade-off tables. No further halt needed:
   --auto mode pre-authorizes documented choices; the design doc itself
   is the sign-off artifact.
6. **Each phase**: update its frontmatter `status: completed` (or
   `not-viable` where applicable) and tick Success Criteria `[x]` only
   when literally true.
7. **Gates after EVERY phase**: `gofmt -l .` empty, `go vet ./...`,
   `go build ./...`, `go test ./...` exit 0 — run them, don't assume.
8. **Domain invariants** (plan.md Invariants + AGENTS.md §5): disabled ≠
   failed ≠ ok; provider failure never becomes a clean result;
   ascending block order per stream; per-stream Finished semantics;
   UpstreamCalls = real outbound requests; `Name()` == `data_sources.code`
   pinned by wiring test/boot check; `Budget[Name()] > 0` enforced; no
   secrets in code/logs/tests/commits (.env.example placeholders only).
9. **Live-verify evidence** (per phase checklists): upstream HeadBlock +
   upstream LookupTransfers on a real address + booted
   `GET /api/v1/wallets/<net>/<addr>/transactions?refresh=1` showing
   `provider_status:"ok"` — or honest `live-verify: BLOCKED (no key)`.
10. **Unverified-adapter tracking table** in plan.md: update each row's
    status as phases complete.

## Consolidated report (write at the end)

`reports/cook-report.md`: per-phase outcome (implemented / NOT-VIABLE +
reason / BLOCKED), commits list, test counts, live-verify evidence or
BLOCKED note, design doc link, deviations, and residual risks.

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
