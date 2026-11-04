You are Devin Cook (continuation session — a prior Cook ran out of
context budget after phase-05). Execute phases 06→12 SEQUENTIALLY of:
`/home/lethanhlam/Desktop/Profile/check-wallet/plans/20261104-1100-tx-provider-expansion/plan.md`

Working directory: `/home/lethanhlam/Desktop/Profile/check-wallet`

### Compliance Inputs (MANDATORY — read before any work)
- Project rules: /home/lethanhlam/Desktop/Profile/check-wallet/AGENTS.md
- Engineering standard: plans/20261104-1100-tx-provider-expansion/reports/compliance/engineering-standard.md
- Learning ledger: plans/20261104-1100-tx-provider-expansion/reports/compliance/learning.md
- Reports dir: plans/20261104-1100-tx-provider-expansion/reports/
- Authoritative spec: docs/tailieu.md
- Prior checkpoint: plans/20261104-1100-tx-provider-expansion/reports/cook-report.md (read FIRST)
If AGENTS.md or learning.md is unreadable: STOP → `Status: NEEDS_CONTEXT`.

## Where things stand (verified by orchestrator — do not re-litigate)

- Phases 01–05 DONE + committed: alchemy `b9d3a7c`, blockscout `3fd37e0`
  (live-verify PASSED), ankr `c8f28ab`, covalent `510fb7c`, moralis
  `3b18ae5`; testutil seed fix `5ff5090`. All keyed adapters honestly
  `live-verify: BLOCKED`, absent from default `TX_PROVIDER_*` selectors.
- Cross-cutting landed already: `TX_PROVIDER_ETH`/`TX_PROVIDER_BSC`
  comma-selectors (first entry serves, extras validated+warned, unknown
  fails boot), boot checks (`GetSourceByCode` pin + `Budget>0`),
  `block_timestamp *time.Time` NULL-honest, migrations 000009–000013.
- Next migration number: 000014.
- Reference impls to follow: `internal/provider/nodereal`,
  `internal/provider/chainscan`, and now `internal/provider/blockscout`
  (drain/buffer), `internal/provider/covalent` (ascending cursor).
- Test DBs MUST end `_test`; parallel packages need private DBs
  (e.g. `check_wallet_txp_test`) + private Redis DB index.
- Postgres :5433 + Redis :6380 RUNNING; `.env` has ONLY ETHERSCAN +
  NODEREAL keys — all other keyed adapters live-verify BLOCKED.
- Blockchair's keyless tier means its probe CAN run unauthenticated.

## Remaining phases

| Phase | Work |
|---|---|
| 06 | tatum adapter (bsc) — keyed, merge-unverified path |
| 07 | bitquery probe → implement or NOT-VIABLE |
| 08 | oklink probe → implement or NOT-VIABLE |
| 09 | blockchair probe (keyless possible) → implement or NOT-VIABLE |
| 10 | tokenview probe → implement or NOT-VIABLE |
| 11 | chainbase probe → implement or NOT-VIABLE |
| 12 | design doc D1–D5 → schema re-key migration → ordered failover chain |

## Execution rules (same as original Cook mandate)

1. Strict TDD Red→Green per phase; docs-first contract verification;
   mocks parse real wire shapes; never weaken tests.
2. Per-phase conventional commit `feat(provider): add <name>
   transaction-history adapter`; phase-12 `feat(txindex): ...`.
3. Tier-2 STEP 0 probe first; probe-FAILED → `reports/<name>-viability.md`
   `NOT-VIABLE: <reason>` → phase DONE. Probe-BLOCKED ≠ NOT-VIABLE →
   merge-unverified path.
4. Phase-12: `reports/phase-12-design.md` answering D1–D5 BEFORE failover
   code. D1 pre-decided (a) re-key `(network_id, wallet_id, source_id)`;
   D2–D5 choices yours — document trade-offs. No halt needed under
   --auto; the design doc is the sign-off artifact.
5. Mark each phase frontmatter `status: completed`/`not-viable`; tick
   Success Criteria `[x]` only when literally true.
6. Gates after EVERY phase: `gofmt -l .` empty, `go vet`, `go build`,
   `go test` exit 0 — run them.
7. Invariants: disabled≠failed≠ok; failure never a clean result;
   ascending per-stream order; real UpstreamCalls; `Name()`==
   `data_sources.code` pinned; `Budget>0`; no secrets anywhere.
8. Update plan.md unverified-adapter tracking table as phases complete.
9. Work economically — batch writes, run full `go test ./...` at
   milestones only. If context runs low: commit finished work, update
   `reports/cook-report.md` checkpoint, end with Status honestly.

## Consolidated report

Extend `reports/cook-report.md`: per-phase outcome, commits, test
counts, live-verify evidence or BLOCKED, design doc link, deviations,
residual risks.

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
