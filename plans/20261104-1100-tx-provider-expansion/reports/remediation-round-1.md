# Remediation Report — Review Round 1

Remediation for `review-round-1.md` (verdict CHANGES_REQUESTED). Each
finding mapped to its fix, file, and regression test.

## Findings → Fixes

### B1 (Blocker) — claim-release paths wiped per-source coverage state

Both release sites (`head < fetchFrom` head-lag path and the raced-drain
prepay path) upserted only status/timestamps — silently zeroing
`indexed_until_block` and `history_truncated`.

**Fix** (`internal/transaction/service.go`): new `releaseClaim(ctx,
netID, walletID, srcID, st)` helper builds the upsert from the
previously-read row, restoring `IndexedUntilBlock`, `IndexedAt`,
`Error`, and `HistoryTruncated` verbatim. Status reverts to the prior
state — except prior `indexing` → `not_indexed` (no phantom claims).
Rows with no prior state (`!st.exists`) revert to an honest empty
`not_indexed` row. Both release paths now call it.

**Regression tests** (`internal/transaction/failover_test.go`):
- `TestFailover_HeadLagReleasePreservesCoverage` — integration: seeded
  `(watermark=400, truncated, PARTIAL, error=degraded)` row survives a
  head-lag release verbatim.
- `TestService_ReleaseClaimRestoresPreclaimRow` — unit: verbatim restore
  on a prior-state row; honest empty row for a claim-created row.

### M1 (Major) — Tokenview catch-up cap faked joined coverage

Reaching the upstream 50-page cap set `st.head = opts.ToBlock` → `fin`
claimed proven coverage and emitted rows under an unfetched hole.

**Fix** (`internal/provider/tokenview/adapter.go`): a `headStalled`
boundary marker replaces the fake join — the scan records the unprovable
head segment, reports streams unfinished, and issues no continuation
token. `headBlocked()` (entry + drain-loop guard) prevents re-burning
the 50 pages on later calls at the same or higher `ToBlock`; a smaller
window below the stalled boundary can still honestly join. `fin` now
requires `st.head >= opts.ToBlock` where `st.head` is only advanced by
a *proven* join (boundary row or empty page).

**Regression test** (`adapter_test.go`,
`TestHeadCatchupCapDeadEnd`): 4-call sequence — initial join at head
100; drift to 4000 drains 50 pages → zero emissions, no token, all
streams unfinished, `UpstreamCalls=50`; same window again →
`UpstreamCalls=0` (no re-burn); smaller window (150) → joins in 1 page
and emits only the proven row.

### m1 — Bitquery NFT rows leaked into the token stream

**Fix** (`internal/provider/bitquery/mapper.go`): token-stream rows with
`!Currency.Fungible` are skipped (GraphQL already requests the field).
Fixture updated to real wire shape (`Fungible: true` on ERC-20 rows) +
an explicit NFT row pinned as dropped in `TestMapping`.

### m2 — `GetSourceByCode` errors silently swallowed → srcID=0

**Fix** (`service.go`): lookup failures now return
`fmt.Errorf("source lookup %q: %w", name, err)` — visible, not skipped.

### m3 — Moralis `atoi` accepted numeric prefixes ("12x" → 12)

**Fix** (`internal/provider/moralis/mapper.go`): strict all-digits
parser returning `(int64, bool)`; mappers skip invalid block numbers.
Both call sites (`adapter.go`) handle the validity flag.

### m4 — Cook report checkpoint table stale

**Fix** (`reports/cook-report.md`): header and phase table reconciled —
continuation section is authoritative for phases 06–12; checkpoint table
explicitly labeled "phases 01–05 session checkpoint".

### n1 — `TransferLookupOpts.Kinds` unused by callers

**Fix** (`service.go`): lookup options now pass
`Kinds: []string{TransferKindNative, TransferKindToken}` explicitly —
callers opt in, adapters that partition on `Kinds` (tatum, chainbase)
receive the signal.

### n2 — missing regression tests

Covered by B1/M1 tests above (three new tests total).

## Verification

- `gofmt -l .` — empty
- `go build ./...` — clean
- `go vet ./...` — clean
- `go test -count=1` on touched packages — green
- Full `go test -count=1 ./...` — see gate log
