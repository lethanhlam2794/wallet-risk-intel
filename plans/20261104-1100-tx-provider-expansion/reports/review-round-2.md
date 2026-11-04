# Devin Review — Round 2 Report

Scope: `914185d..0726414` (round-1 remediation included)
Session: independent fresh Devin CLI session (dangerous mode, read-only
mandate + tree-hash verified — established downgrade path)
Verdict: **APPROVED** (DONE_WITH_CONCERNS)
Raw log: `review-round-2-raw.log`

## Executive Summary

Commit `0726414` remediates all 8 round-1 findings faithfully.
`releaseClaim` restores the per-source coverage row verbatim on both
release paths; tokenview `headStalled` eliminates the unearned join;
regression tests are honest and pass on real DB/wire shapes. One new
advisory Minor (pre-existing advisory-claim TOCTOU, fail-safe direction)
plus nits — none blocking.

## Round-1 finding verification

| Finding | Verdict | Evidence |
|---|---|---|
| B1 claim-release wipes coverage | FIXED | `releaseClaim` restores all fields verbatim (service.go:573-582); `rows_*` preserved via `+EXCLUDED` accumulation; both sites use it; regression tests pass on real DB |
| M1 tokenview cap fake-joins | FIXED | `headStalled` records unprovable boundary; `fin` requires proven `head>=ToBlock`; `headBlocked` guards entry/drain/token; test pins 50 drains + zero re-burn + smaller-window join |
| m1 bitquery NFT leak | FIXED | `!Currency.Fungible` → skip; NFT fixture row pinned dropped |
| m2 sourceID errors swallowed | FIXED | error returns through `EnsureIndexed` → never a clean verdict |
| m3 moralis atoi prefix | FIXED | strict all-digits `(int64,bool)`; both call sites handle `ok` |
| m4 stale cook-report | FIXED | header + checkpoint table reconciled |
| n1 `Kinds` unused | FIXED | `[NATIVE,TOKEN]` explicit; verified all consumers map identically to empty default — semantic no-op |
| n2 missing regression tests | FIXED | 3 honest tests asserting defect conditions |

## New findings

- **[Minor]** `releaseClaim` inherits the advisory-claim TOCTOU:
  `MarkWalletIndexing` is an unconditional upsert, not a CAS. A racing
  process's release can stale-restore over a concurrent terminal write,
  regressing (never inflating) the watermark — self-heals on next
  refresh; multi-replica deployments only. Pre-existing hazard class;
  the old code NULL-wiped in the same race (strictly worse). Optional
  hardening: conditional release `UPDATE ... WHERE status='indexing'`.
- **[Nit]** Moralis strict `atoi` lacks a dedicated regression test.
- **[Nit]** `headBlocked` calls still consume 1 prepaid budget unit for
  0 upstream calls — conservative over-charge, bounded.
- **[Nit]** `case srcID == 0: continue` unreachable defensive residue.
- **[Nit]** Remediation report n1 names tatum/chainbase as `Kinds`
  consumers incorrectly (actual consumers: alchemy/nodereal/chainscan).
- **[Nit]** Bitquery native-row fixture marks `Fungible:false` — inert
  (filter gates token stream only).

## Verification Evidence (reviewer-executed)

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean
- `go test -count=1 ./internal/provider/... ./internal/transaction/...` — all ok
- `go test -count=1 ./...` — all ok (40 pkgs, `tests/` 45.7s on dedicated test DB)
- New regression tests run explicitly — PASS
- `git status` — no tracked modifications; read-only mandate honored
- Tree integrity: md5 tree-hash before/after session identical

## Required Actions

None — approval granted. Advisory items may be scheduled for hardening
at owner discretion.
