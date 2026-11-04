# Devin Critic — Round 3: Transaction Tracing plan (post-R2-amendment)

scope: `plans/20261104-0056-transaction-tracing/` (plan.md + 4 phase
files) verified against `rebuttal-round-2.md` (22 amendments),
`rebuttal-round-1.md`, `critique-round-2.md`, `docs/tailieu.md`
(§4.7, §7.13, §9.9–9.10, §11.1–11.5, §17, §19), and repo internals
(`queries/wallets.sql`, `queries/sanctions.sql`,
`queries/labels_read.sql`, `internal/sanctions/service.go`,
`internal/screening/{service.go,mapper.go}`,
`internal/provider/{provider.go,resilience/}`,
`internal/platform/metrics/metrics.go`, `migrations/000001/000007`,
`api/openapi.yaml:1316`, `docs/feature-maps/assets/*.json`).

Caveat carried from R2: the plan directory is still git-**untracked**
(`git status` → `??`) — no diff baseline exists; all "amended" claims
verified textually only.

---

## Part 1 — R2 amendment verification

All 22 amendments **textually present**. Five landed in a form that
creates or leaves a defect (cross-refs to Part 2).

| # | Verdict |
|---|---|
| N-M1 self-contained plan | **Landed** — §1 scope (plan.md:18–39), §2 invariants (41–65), tests 1–20 inline (232–264). |
| N-M2 per-adapter calls/rates; BSC deadline-bound; `bsc=8`; quota | **Landed** — plan.md:99–107. Scope of `max_provider_requests` unstated (n17). |
| N-M3 100k cap; ~1300-call convergence; lazy posture | **Landed** — plan.md:117, 128–136. |
| N-M4 `is_error` §6 deviation; `DO UPDATE` mutable set | **Landed — but the fetch that feeds revalidation is gone** → R3-M1; identity still unstable → R3-M2. |
| N-M5 INTERNAL excluded both adapters; `i:{ordinal}` future; union rule | **Landed** — plan.md:121–122, 186–188, 271–273. Union rule is the seed of R3-M2. |
| N-M6 deterministic expansion; prefix; nodes_examined/remaining/failed | **Landed — but "prefix" under concurrency-4 has no emission mechanism** → R3-M3. |
| N-m7 closed category enum; `cross_chain_listing` emits item; `verification_status` | **Landed** — plan.md:156–163. Multi-match precedence undefined → R3-m7. |
| N-m8 `PageToken` opaque; `NextPageToken` | **Landed** — plan.md:73–79. Two-stream encoding unstated → R3-m8. |
| N-m9 envelope coverage + `provider_status` + `nodes_failed` | **Landed** — plan.md:170–175, phase-03:8. Root index-completeness still missing on /exposures → R3-M5. |
| N-m10 `category` post-assembly; `max_hops` default 2; `refresh` on /exposures | **Landed** — plan.md:172–175. |
| N-m11 `enabled_chains` config | **Landed** — plan.md:83. |
| N-m12 consumers enumerated; enum `{provider_reported_only, bounded_bfs}` | **Landed** — plan.md:214–221, phase-04:8. Enum drops `NOT_IMPLEMENTED` atomically with the feature — acceptable since the contract change ships together; all 7 touch-points enumerated and confirmed real (`service.go:~297`, `mapper.go:~323` verified). |
| N-m13 zero-value excluded; NFT out; missing contract/from skipped+counted; `index_scope` | **Landed** — plan.md:52–53, 271–273, 279–281; phase-02:9. Skip counts have no wire field → R3-m6. |
| N-m14 `index_status` closed enum; `provider` = `source_id` FK | **Landed** — plan.md:189–194. |
| N-m15 root-returning + self-loop excluded | **Landed** — plan.md:151, test 20. |
| N-n16 batch commits before watermark (crash-safe) | **Landed** — plan.md:122–124. |
| N-n17 lowercase `pending\|confirmed`; nodereal computes confirmations | **Landed** — plan.md:183. Mapping rule `confirmations>0 → confirmed` was silently dropped → R3-m11. |
| N-n18/19 one `data_sources` row per adapter; provider-switch resets watermark | **Landed** — plan.md:137–140. |
| N-n20 sanctions snapshot version in coverage | **Landed** — plan.md:163, phase-03:8. Plumbing gap → R3-m12. |
| N-n21 tolerant timestamp parse; reuse `provider_*` metrics | **Landed** — plan.md:93, 34–35. |
| N-n22 `asset=native`; JSON cut at item boundary | **Landed** — plan.md:211, 175. |

Summary: 22/22 applied verbatim; 7 landings produce or leave defects
handled in Part 2 (N-M4→M1+M2, N-M6→M3, N-m9→M5, N-m7→m7, N-m13→m6,
N-n17→m11, N-n20→m12).

---

## Part 2 — New findings

## [Major] R3-M1 — Tail-window revalidation is unimplementable: no fetch ever covers blocks ≤ watermark

The R1 spec said it explicitly: "re-fetch covers the watermark tail"
(`rebuttal-round-1.md:97–99`). The R2 rewrite kept the *claim* but
dropped the *fetch*. Current §3.2 (plan.md:117):

> fetch ascending `(watermark+1 .. min(head, watermark+window_cap)]`

Every fetch starts **strictly above** the watermark. `refresh=1`
bypasses only the freshness gate, not the window (plan.md:174). The
only path that ever re-covers old blocks is provider-switch watermark
reset (plan.md:137) — a different mechanism. Consequences:

- §2's invariant "`confirmation_status` is a mutable column
  (revalidated upsert)" (plan.md:54–55) — nothing revalidates.
- §3.4's "this is what makes tail-window revalidation actually
  revalidate" (plan.md:198–202) — the DO UPDATE fires on stream-overlap
  dupes and provider-switch only; it never sees a refetched tail row.
- §6's "Deep-reorg revalidation: tail-window only" (plan.md:282) —
  describes a window that does not exist.
- **Test 9 cannot pass as written** (plan.md:245–246): "tail-window
  revalidation updates mutable columns" — a Cook implementer who writes
  this test faithfully will watch it fail, then invent the missing
  spec.

**Amendment:** restore the tail in §3.2 — every EnsureIndexed fetch
covers `(max(0, watermark − reorg_tail + 1) .. min(head,
watermark + window_cap)]` where `reorg_tail` is a per-chain config
(e.g., ETH 64 / BSC 100 blocks; cost ≪ window_cap — state the extra
calls in `calls_per_index`). Add one rule for the boundary: rows in
the re-covered tail **absent** from the refetched data are either (a)
left as-is with the residual declared honestly in §6, or (b) marked —
pick one; under the current `pending|confirmed` vocabulary, (a) is the
only option without a new enum value.

## [Major] R3-M2 — `transfer_index` is context-dependent but stored under a context-free key: cross-wallet same-tx indexing silently loses transfers and corrupts rows

§3.4 (plan.md:184–188) assigns `t:{i}` = "ordinal within the tx's
same-kind transfer group **over the fetched-window union**". §3.2
(plan.md:121–122) computes the union over "all direction/category
streams" — **for the indexed wallet**. A wallet-scoped stream cannot
see transfers in the tx that don't involve the wallet:

- `nr_getAssetTransfers` filtered `fromAddress=W`/`toAddress=W` returns
  only W-involving transfers; `tokentx`/`txlist` likewise. A tx's full
  transfer set is unobservable from these endpoints (no per-tx lookup
  exists on either upstream).

So the same tx is indexed under **different groups** by different
wallets — and hop-2 *guarantees* this happens. Concrete failure:
tx X = {W→C tokenA, C→D tokenC, D→W tokenB}.

- W's index: group {W→C, D→W} → say `t:0`=W→C, `t:1`=D→W.
- C's hop-2 index later: group {W→C, C→D} → `t:0`=W→C (lucky),
  `t:1`=C→D. Upsert of `t:1` hits W's existing `t:1` row →
  `ON CONFLICT DO UPDATE` touches **only mutable columns**
  {confirmation_status, block_number, block_timestamp, is_error} →
  row `(X, t:1)` keeps D→W's immutable fields; **C→D is never stored —
  silently lost.** §2's "ON CONFLICT never silently drops a real
  transfer" (plan.md:57) is violated by construction.

- Same mechanism within one wallet: stream A covers tx X's block,
  stream B truncated before it (independent `max_pages` truncation) →
  partial-union ordinals upserted; a later refetch with the complete
  union reassigns ordinals → same collision/loss.

Also: a W→W self-transfer row arrives via *both* direction streams;
whether the "union" dedups it before ordinal assignment is unstated —
content-keying removes the question.

**Amendment (recommended):** make `transfer_index` content-derived and
context-free — `{kind}:{h}` where `h` = truncated hash (e.g.,
16 hex chars of SHA-256) over the **immutable stored content**
`(transfer_type, asset_identifier‖∅, from_normalized, to_normalized,
amount_raw)` — never over mutable columns. Identical-content duplicate
transfers within one tx get `.{k}` occurrence suffixes (`t:{h}.1`,
`t:{h}.2`); identical rows are indistinguishable by definition, so the
suffix order is unobservable, and multiplicity is consistent across
fetch contexts (any party to one copy is party to all). This (a) gives
every real transfer one identity regardless of which wallet's stream
fetched it — true cross-wallet dedup; (b) makes "immutable fields
never change" actually true — the key *is* the content, so DO UPDATE
can only ever refresh mutable columns; (c) dissolves the partial-union
problem — self-identifying rows upserted early are harmless; and (d)
satisfies spec §7.13's "Cần định danh riêng cho event log" without an
upstream log index. Keep `n:`/`t:` prefixes for grep-ability; future
INTERNAL becomes `i:{h}` (drop the `i:{ordinal}` note). Declared
residuals: cryptographic collision (negligible) and a pagination-bug
double-return inflating `.{k}` by ≤1 (bounded, honest). Adapt test 13
to assert `.{k}` survival; **add a test: same tx ingested via two
different wallets' streams → one row per real transfer, zero loss.**

(Rejected alternative: ordinals over DB-known-rows ∪ fetched with
append-only assignment — identifying "already stored" requires
content-matching anyway, i.e., it reduces to the hash scheme with extra
locking.)

Independent of the fix: state that the watermark advance =
**min over all streams' contiguous coverage** (a page boundary
mid-block ⇒ watermark = boundary_block − 1) — coverage honesty, and
required by the ordinal scheme if it were kept.

## [Major] R3-M3 — "Truncation emits a prefix" has no emission rule under bounded-concurrency-4 hop-2 — reproducibility is still not guaranteed

plan.md:149–150: "Hop-2 `EnsureIndexed` per node (bounded concurrency
4)". plan.md:147–148: "truncation emits a **prefix**". With 4 workers
sharing one deadline and one request-scoped `max_provider_requests`
pool, at cutoff the resolved positions are `{1..k} ∪ {in-flight that
finished} ∪ {in-flight unfinished}` — **not** a prefix. Whether the
emitted set is a deterministic prefix depends on assembly mechanics
the plan does not constrain: an implementer emitting results in
completion order produces a timing-dependent subset — the exact
nondeterminism N-M6 existed to kill, now laundered through a word.

**Amendment:** state the mechanism explicitly: workers pull positions
strictly in the declared order; the assembler emits results **in
position order**; emitted evidence = the maximal contiguous prefix of
*resolved* positions (a FAILED node counts as resolved — it was
attempted — and does not break contiguity; it lands in `nodes_failed`);
results resolved **beyond** the first unresolved position are computed
but excluded from items — never silently dropped: coverage reports
`nodes_examined` (all attempted, including discarded beyond-prefix
work), `nodes_emitted`/`prefix length`, `nodes_failed`,
`nodes_remaining` (never attempted; `examined + remaining =
counterparties_total`). Add a determinism test: same fixtures, N runs
under a tight deadline → identical emitted sets.

## [Major] R3-M4 — `is_error=true` rows are usable as BFS edges: reverted "transfers" fabricate exposure evidence

Both upstreams return failed transactions (`txlist` `isError=1`;
nodereal `receiptsStatus=0`), and the plan rightly indexes them with
the declared `is_error` column (plan.md:179–183) — a reverted tx moved
**no value**: it has no token transfers (no events) and its native
"transfer" is phantom. But nothing anywhere says graph edges exclude
`is_error` rows. As written, a scammer's *failed* tx to the root
wallet produces a hop-1 counterparty and a `SANCTIONED` exposure item
for a transfer that never executed — fabricated evidence, the exact
class of defect the is_error flag exists to prevent.

**Amendment:** one line in §3.3 — hop-1 counterparty discovery and
every edge source query filter `is_error = false` (and §3.5:
/transactions still lists the rows, flag exposed — forensic value,
declared). Add a test: failed tx ⇒ no edge, no counterparty, tx row
still listed with `is_error=true`.

## [Major] R3-M5 — `/exposures` coverage omits the root wallet's index completeness — the partial-index false-clean shape survives on the flagship endpoint

phase-03:8 enumerates the envelope coverage: `{nodes_examined,
nodes_remaining, nodes_failed, edges_examined, provider_calls,
provider_status, sanctions_snapshot_version, per-node indexed_until}`.
Missing vs /transactions (plan.md:208–211):

- `index_status` — a PARTIAL/FAILED root index has no wire slot
  (`provider_status` is the ErrKind, not completeness);
- `history_truncated` — on BSC the cold-start posture makes a bound-
  truncated root index the *common* case (~1300-call backlog,
  plan.md:128–131): an `items: []` response reads as "no exposure",
  not "index 3% converged" — **"no data ≠ safe" violated on the
  endpoint that exists to answer the risk question;**
- `index_scope` — declared for /transactions (phase-02:9) but absent
  from the exposures envelope: a wallet whose risky flows are
  INTERNAL-only produces empty items with no scope disclaimer — the
  same silent-gap class N-M5/B2 killed;
- `indexed_until_block`/`indexed_at` for the root ("per-node
  indexed_until" is ambiguous whether it includes the root — say so).

**Amendment:** exposures envelope gains `coverage.root =
{index_status, provider_status, indexed_until_block, indexed_at,
history_truncated, index_scope}`; clarify `per-node indexed_until`
covers hop-1/hop-2 expanded nodes (map keyed by normalized address);
update §3.5 + phase-03 + OpenAPI task.

## [Minor] R3-m6 — "Skipped + counted in coverage" has no wire field on either endpoint; `index_scope` missing from §3.5

§2 (plan.md:52–53) promises rows missing contract/valid addresses are
"skipped + counted in coverage (never silently dropped)"; §3.2's
service-level coverage `{rows_indexed, window_covered, status}` has no
skip counter either; §3.5's /transactions envelope lists neither
`index_scope` (it lives only in phase-02:9) nor any skip field.
Result: the invariant is unverifiable on the wire — a consumer cannot
distinguish "0 rows skipped" from "not counted".

**Amendment:** wire coverage gains `rows_indexed` +
`rows_skipped{missing_contract, invalid_address}` on both endpoints;
add `index_scope` to the §3.5 envelope list.

## [Minor] R3-m7 — Multi-match precedence for the single `category` field is undefined

A counterparty can simultaneously be label-matched (e.g., `SCAM`),
same-network-sanctioned, and cross-chain/unresolved-listed.
plan.md:158–162 defines the enum and the cross-chain-only case but
not which of several simultaneous matches `category` reports — or
whether the others are dropped (post-assembly `category` filtering
then can't find a `SCAM` counterparty whose emitted category is
`cross_chain_listing`).

**Amendment:** one line — either `category` = highest-precedence match
(state the order, e.g., `SANCTIONED` > label categories >
`cross_chain_listing`) plus a `matched_signals[]`/`categories[]`
detail list, or declare precedence-only loss explicitly. Also state
the filter matches against *all* signals, not only the emitted
`category`.

## [Minor] R3-m8 — Two direction streams, one `NextPageToken`: the encoding and `max_pages` accounting are unspecified

nodereal paginates `fromAddress` and `toAddress` streams with
**independent `pageKey` cursors**. The seam has one opaque
`NextPageToken` — implementable (encode `{fromKey,toKey}` in the
token; empty when both exhausted; a finished stream's side drops out)
but unstated, as is whether `max_pages` counts per-stream pages or
combined call pairs — a 2× difference in `calls_per_index`
(plan.md:100). Also state the head-fetch counts toward
`max_provider_requests` like every other call.

## [Minor] R3-m9 — §11.5 edge/path fields are not pinned to the item schema

§11.5 requires per-path-edge `{from_address, to_address, network,
tx_hash, transfer_type, asset, amount_raw, block_number,
block_timestamp, confirmation_status}` and per-path metadata
`{hop_count, path_verified, temporal_order_valid, data_coverage,
truncated, tracing_method}`. plan.md:164–169 defines `path[]` as
"ordered edges" with no field list and mentions
`temporal_order_valid`/`path_verified` without placement — under
`max_paths_per_pair=3` they must live **per path entry** (an item can
hold mixed-validity paths). `tracing_method` at envelope level
(phase-03:8) vs §11.5's path level is a defensible placement —
declare it; per-path `data_coverage`/`truncated` need a stated answer
(path-level constants or a declared envelope-only deviation).

## [Minor] R3-m10 — `limit`-truncated items have no `truncated` reason; item emission order is unspecified

/exposures has `limit≤100` and **no cursor** (spec §9.10 has none).
Two gaps: (a) the closed truncated-reason enum `{deadline,
max_provider_requests, max_nodes, max_edges, response_cap}` has no
value for "more matched items than `limit`" — either add `limit` to
the enum or state the response always emits all matched items with
`limit` only capping... pick the semantics; (b) with no cursor, the
emitted subset under a cap must be a **deterministic** ordering (e.g.,
`hops` asc, then the §3.3.2 expansion order) — unstated.

## [Minor] R3-m11 — Lifecycle gaps: refresh-vs-cooldown, stale INDEXING, dropped confirmation mapping, tail-absent rows

- `refresh=1` bypasses freshness — must it also bypass the FAILED
  60 s cooldown? If yes, `refresh` loops hammer dead providers; if no,
  say so (recommended: refresh bypasses freshness only).
- `INDEXING` (plan.md:192) persisted pre-crash stays `INDEXING`
  forever — state the recovery (stale `INDEXING` older than X ⇒ treat
  as FAILED/retry).
- R1's "`confirmations>0 → CONFIRMED`, else PENDING" mapping rule
  (rebuttal-round-1.md:95–96) did not survive the rewrite — restore it
  (lowercased) in §3.4.
- Under the R3-M1 fix: tail rows absent from the refetch — declared
  residual or a marker (see R3-M1 amendment).

## [Minor] R3-m12 — `sanctions_snapshot_version` in coverage has no stated source

`LookupWallet` returns `(*WalletLookup, dataStatus, lastSynced, err)`
(service.go:49–50) — the snapshot version is used internally for the
cache key but **not returned**. Coverage needs the version: either
fetch `LatestSnapshotPublished` (queries/sanctions.sql:95) once per
request in the tracing service, or extend the LookupWallet signature.
One line in §3.3/phase-03.

## [Minor] R3-m13 — No bulk label-match query exists; `ListWalletLabelsSummary` is `LIMIT 5`

phase-03:6 matches counterparties against `wallet_labels(revoked IS
NULL)`. The only reads are `ListAllWalletLabels` (per
normalized+network — N+1 over all counterparties) and
`ListWalletLabelsSummary` (per wallet_id, `LIMIT 5` — a screening
summary, labelsRead.sql:26–35, silently wrong for matching). Add a
batch query (`WHERE w.network_id = $1 AND w.normalized_address =
ANY($2) AND wl.revoked_at IS NULL`) alongside the existing bulk wallet
resolve; same for sanctions if desired (per-node `LookupWallet` is
cache-backed and node-bounded — acceptable, but say which).

## [Minor] R3-m14 — Adapter resilience wiring is unstated

AGENTS §7 mandates per-provider timeout, concurrency limit, rate
limit, bounded retry, circuit breaker, failure metrics.
`internal/provider/resilience/` exists (`NewClient`, breaker, limiter,
retry) and every current adapter presumably wraps it — but phase-01
never says so; a green-field implementer could ship raw http calls
into a deadline-bound hop-2 loop. One line in phase-01: adapters wrap
the resilience client with per-adapter limiter config (chainscan ~3/s;
nodereal 0.6/s + CU-aware accounting or the conservative rate).

## [Nit] R3-n15 — Bulk resolve `INSERT … SELECT unnest() ON CONFLICT → map` needs the no-op `DO UPDATE` for a complete `RETURNING`

`ON CONFLICT DO NOTHING … RETURNING` omits pre-existing rows →
incomplete address→id map. The convention already exists
(`queries/wallets.sql:5` `DO UPDATE SET updated_at =
wallets.updated_at`) — reference it in phase-01.

## [Nit] R3-n16 — Etherscan proxy action name is `eth_blockNumber`, not `eth_block_number` (plan.md:83, phase-01:9) — cosmetic but wrong in the published contract.

## [Nit] R3-n17 — `max_provider_requests` scope: request-global pool shared across hop-0 + all hop-2 `EnsureIndexed` calls (vs per-node) — one line; it changes the bsc=8 arithmetic meaning entirely (~1 node/request).

## [Nit] R3-n18 — /transactions unspecified details: item sort direction (declare DESC; keyset already handles), `direction` values (`in|out` vs queried wallet), `asset=<contract-address>` form for TOKEN rows, `from_timestamp`/`to_timestamp` semantics — all in spec §9.9's filter list, none written down.

## [Nit] R3-n19 — `cross_chain_listing` name also covers `unresolved_network_listings` (LookupWallet's third bucket — an unknown-network listing is not demonstrated cross-chain); naming is slightly misleading — accept with a one-line doc or rename (`non_direct_listing`). Separately: items carry no match reference (entity/program/label source) — spec §9.10 doesn't require it; flag as an optional evidence nicety, not a requirement.

## [Nit] R3-n20 — `wallet_index_state.status` enum is UPPERCASE while the DDL convention it coexists with uses lowercase (`'pending'`); `provider` column name holds a `data_sources.id` FK (`source_id` reads truer) — cosmetic, pick deliberately.

## [Nit] R3-n21 — Second-order determinism: per-pair edge pick needs a final `(tx_hash, transfer_index)` tiebreak (same ts + same amount can differ); which 3 of N paths `max_paths_per_pair` keeps; per-node edge iteration order for `max_edges` truncation — one line each keeps responses reproducible.

---

## Verified-as-true plan claims (credit where due)

- All 22 R2 amendments textually landed; plan.md is self-contained
  again (§1, §2, tests 1–20 inline).
- NodeReal facts carried correctly: 250 CU/call, 150–300 CUPS
  discrepancy + conservative figure, ≤100k window, `pageKey`+`maxCount`,
  direction streams, `receiptsStatus`, confirmations computed.
- `LatestSnapshotPublished` exists (sanctions.sql:95) for the coverage
  version; `ErrKind` vocabulary reused correctly; `NOT_IMPLEMENTED`
  emission sites verified (`service.go:~297`, `mapper.go:~323`);
  openapi.yaml:1316 enum confirmed; exactly 5 `screening-*.json`
  fixtures exist; `provider_requests_total` family exists
  (metrics.go:93); resilience client exists (`provider/resilience/`).
- wallets unique key `(network_id, normalized_address)` +
  `updated_at` present (000001:14–24) — bulk resolve convention ready.
- Crash-safe ordering (batch→watermark), provider-switch reset,
  opaque `PageToken`, closed enums, spec §7.13 deviation properly
  declared, `path_verified=false` honesty — all correctly stated.

## Verdict

Round-2's amendments all landed, and several previously-challenged
areas are now genuinely sound (budget arithmetic, DISABLED posture,
windowing honesty). But two mechanisms remain **unimplementable as
written** — the tail-refetch that revalidation claims (R3-M1) and the
context-dependent `transfer_index` that loses/corrupts rows under
hop-2's normal cross-wallet operation (R3-M2) — plus prefix-emission
under concurrency (R3-M3), phantom edges from reverted txs (R3-M4),
and the missing root-completeness fields on /exposures (R3-M5). All
have concrete specifiable fixes; none require redesign — the
content-hash identity in R3-M2 actually *simplifies* the plan.

**CHALLENGED** — 0 blockers, 5 majors (R3-M1…R3-M5), 9 minors, 7 nits.
