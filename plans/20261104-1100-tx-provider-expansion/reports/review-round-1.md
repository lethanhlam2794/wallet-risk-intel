## Independent Code Review — `914185d..8f41439`

**Verdict: CHANGES_REQUESTED**

### Executive Summary

The expansion is structurally sound and unusually honest for adapter work: all ten adapters gate on `Enabled()` with zero traffic when disabled, pin ascending order (server-side `ASC`, deterministic composite `orderBy`, or local sort for undocumented upstream order), count real `UpstreamCalls` including internal drain pages, and keep amounts exact end-to-end (`big.Int`/`big.Rat` → decimal string → NUMERIC). The failover walk in `ensure()` implements the phase-12 design faithfully — closed five-kind whitelist, terminal non-failover kinds, partial-batch discard before upsert, per-try token restart, per-source state rows, last-failure persistence. Migration 000019 is verified live in both directions on the dedicated test DB.

Two defects block approval:

1. **New-in-7e9e730 claim-release paths silently wipe `wallet_index_state` coverage bookkeeping** — the exact per-source state this commit exists to protect — on routine conditions (lagging upstream head, or a CAS race on the shared budget pool under concurrent frontier indexing).
2. **Tokenview's head catch-up declares a joined head segment at the upstream 50-page cap without draining to the boundary** — emitting rows under an unproven coverage claim, the one thing the drain/buffer model exists to prevent.

Both are localized and fixable without redesign. Supporting minors: a bitquery NFT misclassification, a swallowed `sourceID` error, and a stale cook-report checkpoint.

### Verification Evidence

| Command | Result |
|---|---|
| `git log 914185d..8f41439` | 14 commits confirmed, matching scope |
| `git diff --stat` | 97 files, +11,657/−198 |
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `gofmt -l .` | empty |
| `go test -count=1 ./internal/provider/... ./internal/transaction/...` | **all ok** — 19 pkgs (tokenview 26s, transaction 9.8s incl. real-DB failover suite) |
| `go test -count=1 ./...` | **all ok** — incl. `tests` 45.8s (dedicated test DB) |
| Migration 000019 live check (rolled-back tx on `check_wallet_test`) | UP re-executes; DOWN collapses 3 source rows → deterministic single survivor (max `updated_at`, `source_id` tiebreak), preserves `history_truncated`, restores 2-col PK |
| `grep -rn ResetWalletIndexSource` | 0 hits — dead path fully eradicated |
| Call-site sweep (`GetWalletIndexState`/`MarkWalletIndexing`/`UpsertWalletIndexState`) | all re-keyed to `source_id`; no stray users |
| Secrets/`t.Skip` sweep | none committed; only 2 opt-in live-data gates (`OFAC_REAL_XML`, `REGEN_CAPTURES`) |

### Detailed Findings

**[Blocker] Claim-release upserts wipe the per-source watermark and truncation truth** — `internal/transaction/service.go:337-346` (prepay-fail release) and `service.go:377-387` (`head < fetchFrom` release), both added by 7e9e730. Each calls `UpsertWalletIndexState` with `IndexedUntilBlock` and `HistoryTruncated` unset → zero `pgtype.Int8`/`false` → and the generated upsert assigns `indexed_until_block = EXCLUDED.indexed_until_block` unconditionally (`queries/transactions.sql:108-121`). Result: the source row's watermark becomes NULL, `history_truncated` reverts false, status → `not_indexed`. Triggers: (a) any provider head ≥64 blocks (ETH tail) / ≥100 (BSC tail) below the stored watermark — routine for lagging explorer backends and post-reorg provider views; (b) the CAS race between `opts.Budget.Exhausted()` (`model.go:114`) and `prepay()` under concurrent BFS frontier indexing — `Budget` is deliberately atomic, so this race is designed-in. After the wipe, the same response still fills from the pre-claim `st` — the wire reports the old coverage while the DB now says `not_indexed`, and the next request re-indexes from genesis. `writeDisabled` (`service.go:574-584`) demonstrates the correct preservation pattern (carries `st.row.IndexedUntilBlock`/`HistoryTruncated`); the release paths forgot it. Also untested — which is how it slipped past an otherwise thorough suite.

**[Major] Tokenview catch-up force-joins at the page cap, emitting under unproven coverage** — `internal/provider/tokenview/adapter.go:402-404`. When `st.headPage` reaches `maxPage` (50) without hitting a row `≤ st.head`, the code sets `st.headPage = 0; st.head = opts.ToBlock` — declaring the head segment joined. Rows between `st.head` and the lowest scanned catch-up row sit inside the emitted `[FromBlock, ToBlock]` window but were never fetched; `fin` then emits buffered rows claiming contiguous coverage. This is a silent evidence hole — the invariant "emission only on proven coverage" is violated for wallets with >~2500 new txs between refreshes (rare, but hot wallets are exactly what this product inspects). The main scan treats the same cap honestly as `st.capped` dead-end (adapter.go:409-411); the catch-up path must behave identically. Blockscout's catch-up (`blockscout/adapter.go:430-456`) is unbounded cursor-based and has no such hole. No test exercises the catch-up cap.

**[Minor] Bitquery admits NFT transfers into the token stream** — the query selects `Currency.Fungible` (`bitquery/adapter.go:276`) but `mapRow` (`bitquery/mapper.go:83-113`) never filters it; `Currency.Native=false` includes ERC-721 transfers, which emit as TOKEN rows — misclassified, though not invented.

**[Minor] `sourceID` errors swallowed** — `service.go:209-213` discards `s.sourceID` errors; a runtime DB failure resolves to `srcID=0` → silent skip → stale verdict instead of a surfaced error. Rare (boot validates rows exist) but masks infrastructure failure.

**[Minor] Moralis `atoi` truncates malformed input** — `moralis/mapper.go:40` stops at first non-digit (`"12x"` → 12) instead of skipping the row.

**[Minor] Cook report is internally stale** — `cook-report.md:3-21` still declares phases 06-12 "not started" while §"Phase 12 mechanics (landed)" documents them shipped. The closeout commit appended but never reconciled the header.

**[Nit] `TransferLookupOpts.Kinds` is a dead contract field** — `provider.go:250`; no caller populates it, no adapter honors it. Either honor or drop.

**[Nit] Test coverage gap mirrors the defects** — the failover suite is exemplary (real DB rows, per-source assertions, all four non-failover kinds, disabled-skip, batch-discard, all-fail-last-kind, budget semantics), yet no test covers `head < fetchFrom` or the tokenview catch-up cap — precisely the two broken paths.

### Required Actions to Approve

1. Make both claim-release upserts preserve `indexed_until_block`, `history_truncated` (and avoid clobbering unrelated fields) — carry `st.row` values through exactly as `writeDisabled` does, or revert the row to its pre-claim content.
2. Tokenview: on `st.headPage >= maxPage` without a boundary row, treat the head segment as unprovable (dead-end like `capped`: streams unfinished, no join, no emission) — never set `st.head = ToBlock` unearned.
3. Add regression tests for both paths.
4. Filter `Currency.Fungible` in bitquery's token stream (or document NFT inclusion deliberately).
5. Surface `sourceID` resolution errors instead of silent skip.
6. Reconcile the cook-report checkpoint table with the landed sections.

Status: DONE_WITH_CONCERNS

Summary: A disciplined, honest expansion — contract-conformant adapters, exact amount handling, and a faithful failover implementation verified end-to-end — but commit 7e9e730 introduces claim-release paths that wipe the per-source coverage state it exists to create, and tokenview's catch-up can emit under unproven coverage. Two targeted fixes with regression tests will make this approvable.

Concerns/Blockers: Blocker — `service.go` release paths NULL out `indexed_until_block`/`history_truncated` on head-lag and budget-race conditions. Major — tokenview catch-up force-join at the 50-page cap over-claims coverage.
