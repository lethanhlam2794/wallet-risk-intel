# Devin Critic — Round 4: Transaction Tracing plan (post-R3-amendment)

scope: `plans/20261104-0056-transaction-tracing/` (plan.md + 4 phase
files) verified against `rebuttal-round-3.md` (21 amendments),
`docs/tailieu.md` (§4.7, §7.13, §9.9–9.10, §11.4–11.5), and repo
internals (`migrations/000001/000003/000005`, `queries/wallets.sql`,
`queries/sanctions.sql`, `queries/labels_read.sql`,
`internal/sanctions/service.go`, `internal/screening/{service.go,
mapper.go,model.go}`, `internal/enrichment/service.go`,
`internal/config/config.go`, `internal/provider/{provider.go,
resilience/client.go}`, `internal/platform/metrics/metrics.go`,
`internal/httpserver/server.go`, `cmd/api/main.go`,
`api/openapi.yaml:1316`, `docs/feature-maps/assets/*.json`,
`internal/network/resolver.go`).

Carried caveat (R2, R3): the plan directory remains git-**untracked**
(`git status` → `??`) — no diff baseline; all "amended" claims verified
textually only. The working tree also carries uncommitted Phase-5 code
— verified file contents, not committed state.

---

## Part 1 — R3 amendment verification

All 21 amendments **textually present** in plan.md and phase files.
Four landings produce or leave a defect handled in Part 2 (R3-M1→R4-M1,
R3-M2→R4-M3, R3-M3→R4-m4, R3-M5/m6→R4-M2).

| # | Verdict |
|---|---|
| R3-M1 tail refetch `(max(0,w−reorg_tail+1)..cap]`; ETH 64/BSC 100; tail-absent left as-is | **Landed** — plan.md:130–134, phase-02:6, §6:364–365. Arithmetic violates the upstream span cap → **R4-M1**. |
| R3-M2 `transfer_index` = `{kind}:{sha256(immutable)[0:16]}` + `.{k}`; test 13 extended | **Landed** — plan.md:233–246, tests 13:326–330, phase-01:8/12, phase-02:6. Occurrence-counting domain unpinned → **R4-M3**. |
| R3-M3 prefix-emission mechanics; FAILED=resolved; nodes_emitted; test 22 | **Landed** — plan.md:169–176, 207–209, test 22:344–345, phase-03:6/9/12. Determinism claim overreaches under wall-clock deadline → **R4-m4**. |
| R3-M4 `is_error=false` edge/counterparty filter; test 21 | **Landed** — plan.md:163–164, test 21, phase-01:7, phase-03:6/12. |
| R3-M5 `coverage.root{index_status,provider_status,indexed_until_block,indexed_at,history_truncated,index_scope,rows_indexed,rows_skipped}` | **Landed** — plan.md:211–214, phase-03:9. Fields unservable on no-fetch responses → **R4-M2**. |
| R3-m6 `rows_skipped{missing_contract,invalid_address}` + `index_scope` both envelopes | **Landed** — plan.md:213–214, 279–281, phase-02:7/9. Same persistence gap → **R4-M2**. |
| R3-m7 precedence SANCTIONED>label>cross_chain; matched_signals all; filter all signals | **Landed** — plan.md:183–191, phase-03:7. Intra-label precedence + classification-tier emit decision open → **R4-m6**. |
| R3-m8 PageToken `{fromKey,toKey}`; max_pages stream-pair steps; head counts | **Landed** — plan.md:90–94, phase-01:8/11. Cursor naming nit → R4-n11. |
| R3-m9 §11.5 fields per path entry; data_coverage/truncated envelope-only declared | **Landed** — plan.md:193–199, 215–218, §6:370–371, phase-03:8. |
| R3-m10 `limit` in truncated enum; deterministic item order | **Landed** — plan.md:178–180, 219–222, phase-03:9. `limit` absent from precedence chain → **R4-m5**. |
| R3-m11 refresh=freshness-only; stale INDEXING→retry; confirmations>0→confirmed; tail-absent residual | **Landed** — plan.md:126–129, 231–232, phase-02:6. Refresh scope on /exposures open → R4-m8. |
| R3-m12 `LatestSnapshotPublished` once/request | **Landed** — plan.md:265–266, phase-03:9. No-snapshot case → R4-n11. |
| R3-m13 bulk label-match query | **Landed** — plan.md:260–263, phase-01:7. Needs category/entity joins for match detail — implementable as written. |
| R3-m14 `resilience.NewClient` wrap + per-adapter limiters | **Landed** — plan.md:87–88, phase-01:9/10; verified `resilience.Client{Config,Hooks,WithClock}` exists (client.go:26–91). |
| R3-n15 bulk resolve `DO UPDATE updated_at=wallets.updated_at` full RETURNING | **Landed** — plan.md:254–258, phase-01:7; convention confirmed (wallets.sql:5). |
| R3-n16 `eth_blockNumber` proxy name | **Landed** — plan.md:84–85, phase-01:9/10. |
| R3-n17 `max_provider_requests` request-global pool | **Landed** — plan.md:161–162, phase-01:11, phase-03:6. |
| R3-n18 DESC sort; `direction in|out`; `asset=native|<contract>`; inclusive timestamps | **Landed** — plan.md:281–286, phase-02:9. Self-row direction open → R4-m8. |
| R3-n19 `cross_chain_listing` covers unresolved-network; documented | **Landed** — plan.md:185–187, §6:367–369. |
| R3-n20 lowercase status enum; `source_id` column | **Landed** — plan.md:249–252, phase-01:6. Enum overlap stale-vs-partial → R4-M2. |
| R3-n21 edge-pick final tiebreak; lexicographic-first 3 paths; per-node edge order | **Landed** — plan.md:197–203, phase-03:8. |

Summary: 21/21 applied; four landings carry follow-on defects
(R3-M1→R4-M1, R3-M2→R4-M3, R3-M3→R4-m4, R3-M5/m6→R4-M2).

---

## Part 2 — New findings

## [Major] R4-M1 — Fetch-window arithmetic violates the nodereal 100k hard cap: every warm BSC fetch is rejected

§3.2 (plan.md:130–131):

> fetch ascending `(max(0, watermark − reorg_tail + 1) ..
> min(head, watermark + window_cap)]`

`window_cap` is defined as "nodereal 100k blocks (**hard upstream**)"
— the upstream cap binds the *request span* (`toBlock − fromBlock ≤
100,000`, verified R2). The formula anchors the upper bound to the
watermark, not the window start:

```
span = (w + window_cap) − (w − reorg_tail + 1) = window_cap + reorg_tail − 1
     = 100,000 + 100 − 1 = 100,099  >  100,000
```

Every BSC fetch with `watermark > 0` emits an out-of-cap request →
upstream 4xx → `index_state.status=FAILED` → cooldown → FAILED again.
Only the cold first window (w=0: `from=0, to=100,000`, span exactly
100k) is legal — **BSC indexing converges for exactly one window then
stalls permanently**. The `(`/`]` half-open notation also leaves the
tail off-by-one ambiguous: read exclusively it covers
`w−tail+2..w` = tail−1 blocks and skips `w−tail+1` outright.

**Amendment:** write explicit inclusive bounds — `fetch_from =
max(0, watermark − reorg_tail + 1)`; `fetch_to = min(head, fetch_from +
window_cap)` where `window_cap` bounds `fetch_to − fetch_from`
(upstream semantics; chainscan `window_cap=∞` ⇒ `fetch_to=head`).
Declare the tail consumes forward reach (BSC effective advance ≈
99,901 blocks/window — the ~1300-call convergence arithmetic is
unchanged at this precision). Drop the half-open notation for an
inclusive `[fetch_from .. fetch_to]` with the block count stated.

## [Major] R4-M2 — `wallet_index_state` cannot serve the declared wire coverage on no-fetch responses; the stale/partial single-enum collapses truncation honesty

The wire requires on **every** /transactions response (§3.5) and in
`coverage.root` on /exposures (§3.3.8): `history_truncated`,
`rows_skipped{missing_contract, invalid_address}`, `rows_indexed`,
`index_status`, `provider_status`. But §3.2 serves DB-only when the
index is fresh, and DISABLED/cooldown-FAILED perform no fetch — so
these per-fetch facts must come from `wallet_index_state`, which
persists only `{source_id, indexed_until_block, indexed_at, status,
error, rows_indexed}` (plan.md:247–253). Missing:

- `history_truncated` — a converged-but-truncated index (the *normal*
  BSC posture, plan.md:145–151) serving from cache, DISABLED, or
  cooldown has no way to report truncation. Serving `false`/absent is
  the false-complete shape R3-M5 + N-m13 existed to kill.
- `rows_skipped_*` — same problem; §2's "skipped + counted in coverage
  (never silently dropped)" is unverifiable on no-fetch responses.
- `status` is single-valued but must express two orthogonal facts:
  `stale` (TTL overlay on indexed_at) and `partial` (last-fetch
  bound-truncated). A TTL-lapsed partial index can store only one —
  whichever loses, the wire loses with it.
- `provider_status` on no-fetch responses: derivable for
  failed (state.error) and disabled (evaluated now) but the
  fresh-serve value ("ok"/null) is unstated; and on /exposures a
  single `coverage.provider_status` vs N node attempts needs an
  aggregation rule.
- `rows_indexed` semantic (cumulative vs last-fetch) unspecified.

**Amendment:** pick one — (a) add `history_truncated BOOL NOT NULL
DEFAULT false` + `rows_skipped_missing_contract BIGINT` +
`rows_skipped_invalid_address BIGINT` (or one `last_fetch_coverage
JSONB`) to `wallet_index_state`, and define `provider_status` on
no-fetch responses as the persisted last-attempt outcome (state.error,
empty ⇒ `"ok"`); (b) store status ∈ {not_indexed, indexing, partial,
fresh, failed, disabled} and derive `stale` at read time from
`indexed_at` vs TTL — this resolves the enum collision while keeping
both facts. Either way: state `rows_indexed` = cumulative rows stored
for the wallet, and `coverage.provider_status` = the root
EnsureIndexed outcome (node-level failures already surface via
`nodes_failed`).

## [Major] R4-M3 — `.{k}` occurrence counting domain unspecified → phantom self-transfer rows on nodereal, silent copy loss on page boundaries

§3.4 (plan.md:236–240) assigns `.{k}` occurrence suffixes to
byte-identical duplicates "within one tx" but never says **over what**
occurrences are counted. Two deterministic failure modes under the
natural "count in the fetched union" implementation:

- **Phantom duplicates:** a W→W self-transfer satisfies both
  `fromAddress=W` and `toAddress=W` filters → nodereal returns it in
  **both direction streams** → the union multiset holds 2 copies of 1
  real transfer → rows `t:{h}.1`, `t:{h}.2` for one real transfer
  (k real copies ⇒ 2k union records ⇒ 2k rows). This is not an edge
  case — every self-transfer on BSC double-counts. The correct rule:
  real multiplicity per `(tx_hash, h)` = **max over per-stream
  occurrence counts** (each stream lists each real copy once; an
  absent or truncated stream counts 0, so `max` still yields the true
  count whenever at least one stream covered the block). chainscan's
  kind-streams (txlist/tokentx) are single-stream — count within the
  stream. Set-dedup is *not* a substitute: identical-content copies
  are byte-identical records, so dedup collapses real duplicates.
  Max-count is also the only rule consistent across adapters for the
  provider-switch-reindex path.
- **Page-boundary loss:** counting must run over the assembled batch
  scan per `(tx_hash, h)`, not per page. A page boundary inside a
  tx's identical-copy group re-counts `.1` on the next page → upsert
  collides with the committed `.1` under the mutables-only DO UPDATE →
  the copy is silently lost — the exact "never silently drops a real
  transfer" violation R3-M2 was written to end. Per-scan counting +
  tail refetch of the boundary block (whole block re-scanned, recount
  yields `.1`+`.2`) is self-healing — but only if the counting domain
  is pinned.

**Amendment:** pin it — occurrences counted per `(tx_hash,
content-hash)` as `max` over stream-local counts within the assembled
scan; rows upserted from a page-boundary-truncated block are
reconciled by the tail refetch (self-healing, since the whole block is
re-counted). Declare the residual: both direction streams truncated
inside the same identical-copy group is undetectable and bounded (same
honesty class as the declared pagination-bug residual). Extend test 13
+ phase-01 fixtures: a W→W self-transfer via nodereal ⇒ **exactly one
row**; two identical W→W transfers ⇒ two rows.

## [Minor] R4-m4 — Test 22's determinism claim is not implementable under a wall-clock deadline

"Same fixtures, N runs under a tight deadline ⇒ identical emitted item
sets" — the prefix rule makes the emitted set deterministic
*conditional on the resolved set*; under a real deadline the resolved
set is timing-dependent (a node resolving at t=4.99 s vs cutoff at
4.98 s flips the prefix). N runs can legitimately yield different
prefixes → the test flakes by construction or proves nothing.

**Amendment:** restate the property — "emitted items are always a
contiguous prefix of the declared order, and identical given identical
resolution sets" — and exercise it over deterministic bounds:
`max_provider_requests`/`max_nodes` truncation with fixed stub
call-counts is timing-free; a pre-expired ctx gives the degenerate
(empty-prefix) case deterministically; `resilience.WithClock`
(client.go:70) exists for virtual time. Deadline-as-wall-clock belongs
in coverage assertions (truncated=deadline fires), not set-equality
assertions.

## [Minor] R4-m5 — `limit` missing from bound precedence; filter-vs-cap order unstated

`limit` is in the truncated enum (landed R3-m10) but absent from the
precedence chain `deadline > max_provider_requests > max_nodes >
max_edges > response_cap` (plan.md:178–180) — a response hitting
deadline *and* limit has no stated reason. `category` post-assembly
filter vs `limit`/`response_cap` order is also unstated (cap-then-
filter loses matched items that filter would have kept).

**Amendment:** place `limit` last in precedence (a shaping cap; hard
bounds win the reported reason); pin the pipeline: assemble → category
filter (over all `matched_signals`) → deterministic order (hops asc,
then expansion order) → `limit`/`response_cap` cut at item boundary.

## [Minor] R4-m6 — Label intra-tier precedence, classification-tier emit decision, signal schema unpinned

`category = SANCTIONED > label categories > cross_chain_listing`
(plan.md:183–186) leaves two holes:

- A counterparty holding two label categories (e.g., SCAM + MIXER —
  `risk_categories` seed, 000005:16–37) — intra-tier rule unstated;
  emitted `category` becomes nondeterministic.
- A counterparty whose *only* match is a SERVICE_TYPE/ENTITY_TYPE
  label (GAMBLING, MIXER, EXCHANGE) — does it emit an item? AGENTS §5:
  these are classifications, not crimes — but a MIXER counterparty is
  exactly what exposure analysis exists to surface. Recommend: emit
  all matches (informational, consistent with `cross_chain_listing`
  emitting informational items); either way, state the decision.
- `matched_signals[]` schema needs a `kind`/`category` per signal —
  "category filter matches all signals" is unimplementable if signals
  don't carry categories.

**Amendment:** intra-tier precedence = `category_type` order
MALICIOUS_ACTIVITY > SERVICE_TYPE > ENTITY_TYPE > OTHER, tiebreak
category code asc (deterministic); pin signal entries
`{kind: sanctions_direct|label|listing, category, entity?, program?,
label?, label_source, verification_status?}`; declare
classification-only matches emit items.

## [Minor] R4-m7 — Emission/resolution semantics gaps in §3.3

- Do hop-1 matched items emit even when their expansion position was
  never attempted (max_nodes/budget truncation)? The consistent
  reading is yes — hop-1 evidence is local to the root index and costs
  no provider calls — but nothing says "bounds govern hop-2 expansion
  positions only; hop-1 items are unaffected", so a reader can
  conclude `max_nodes` truncates hop-1 items.
- "Position resolved" needs one line: resolved = index attempt +
  edge scan + counterparty match complete; a FAILED/DISABLED index
  resolves with empty contribution + `nodes_failed++` (else
  "resolved" is ambiguous between index-done and fully-processed).
- "Hop-2 `EnsureIndexed` per node" — state every hop-1 counterparty
  is an expansion position regardless of match status (expanding only
  matched nodes launders the clean-intermediary case the feature
  exists to catch).

## [Minor] R4-m8 — Refresh scope on /exposures; `direction`/`max_hops` edge semantics

- `refresh=1` "same bounds" (plan.md:219–220): does it force-refresh
  every attempted hop-2 node or root only? Recommend: applies to every
  `EnsureIndexed` performed this request (consistent semantics; the
  FAILED cooldown still holds per R3-m11) — and declare the quota
  consequence on BSC (~40k calls/month budget).
- `direction` on /exposures: default (both?) + value vocabulary
  unstated (presumably `in|out` mirroring /transactions).
- On /transactions, a W→W self-row matches `direction=in`, `=out`, or
  both? State (suggest: matches either — it's both directions).
- `max_hops=0`/`=1`: 1 ⇒ hop-1 items only, no expansion (state it);
  0 ⇒ 400 or hop-0-only? Pick.

## [Minor] R4-m9 — Sentinel counterparties and ascending-index evidence bias undeclared

- A burn/mint makes `0x0` a hop-1 counterparty → it becomes an
  expansion position → `EnsureIndexed(0x0)` indexes every burn on the
  chain (bounded by hop-2 `max_pages=2` + pool, but pure waste of a
  scarce budget). Recommend excluding sentinel addresses
  (`0x000…000`, `0x…dead`) from expansion positions — they still list
  on /transactions; or declare they're expanded like any other node.
- Ascending indexing means a mega-hub hop-2 node's discovered edges
  are its *earliest* history (a router indexed to 2017 blocks yields
  2017-era hop-2 evidence). `node_indexed_until` carries the honesty —
  declare the bias so consumers don't read hop-2 evidence as recent-
  activity coverage.

## [Minor] R4-m10 — Test-list gaps

Mechanisms in §3 with no required test: provider-switch watermark
reset (plan.md:154–155); refresh bypassing freshness while the FAILED
cooldown still holds (test 7 covers the cooldown, not the refresh
path); stale-`INDEXING` → retryable recovery (plan.md:127–129);
self-transfer dedup (R4-M3); `direction`/`category` filter behavior
on /exposures. Fold into existing tests where natural (7, 13) or add.

## [Nit] R4-n11 — Coverage vocabulary nits

- `counterparties_total` is referenced by the `examined + remaining =
  total` invariant but never emitted — emit it or the arithmetic is
  unverifiable on the wire.
- `nodes_emitted` = "prefix length" (positions), while `limit` caps
  *emitted items* — two senses of "emitted"; rename
  (`positions_emitted`) or document.
- `node_indexed_until` entries for failed/unindexed nodes: absent vs
  explicit null — state.
- `sanctions_snapshot_version` when no snapshot is published
  (`LatestSnapshotPublished` :one → no rows) → null; state.
- PageToken cursor fields `{fromKey,toKey}` are nodereal-flavored —
  chainscan's pair is `{txlistPage, tokentxPage}`; name them
  stream-cursor A/B or document the mapping.
- `max_edges`: §3.3.6 reads per-node-iteration; the bound enum treats
  it as a request bound — clarify (recommend a request-global running
  count across edge scans, truncated mid-scan in declared order).
- `provider_status` wire value on a successful/no-attempt response
  (null? `"ok"`?) — ErrKind is failure-only vocabulary; state the
  success value on both envelopes.

## [Nit] R4-n12 — Adapter/schema nits

- nodereal `amount_raw` source field: pin it by name. The
  Alchemy-compatible schema carries `value` (top-level) +
  `rawContract.value` (hex string); R2's verified list noted
  "value (hex)", but the float-vs-hex shape must be pinned in the
  plan — whichever field is raw integer, `amount_raw` reads it and
  never a float-typed field (test 1 catches a wrong pick, but the
  fixture shouldn't be the spec).
- chainscan `tokentx` carries no `isError` field → token rows get
  `is_error=false` inherently (a reverted tx emits no Transfer
  events) — one doc line so nobody hunts for the flag.
- chainscan `value>0` is a client-side post-filter (Etherscan has no
  server param); scope-excluded rows aren't counted in `rows_skipped`
  — say so explicitly (they're scope exclusions, not skips).
- Pin `tx_hash` stored lowercase (UNIQUE key is case-sensitive) and
  hash inputs = **stored column values** with fixed field order +
  separator (normalized addresses, NUMERIC-canonical amount string,
  lowercase contract) — protects identity across the provider-switch
  reindex.
- `data_sources.source_type` value for the two new rows (e.g.,
  `'provider'`); bulk-resolve `address_type` (`'evm'`?) — name both.
- Unfiltered /transactions listing is a UNION of the from/to index
  scans with the keyset applied over the union — one line saves a
  query-shape debate.
- §1 metrics: there is **no provider `duration` family** —
  metrics.go has `provider_requests_total`, `provider_errors_total`,
  `provider_rate_limited_total`, `provider_circuit_open` only. Add
  `provider_request_duration_seconds` or drop the word "duration".

---

## Prompt's targeted questions — direct answers

**Content-hash identity × watermark/tail-refetch:** the interaction is
*correct and is a genuine simplification* — refetched rows produce
identical keys so DO UPDATE can only ever touch mutable columns;
cross-stream overlap regions and truncated boundary blocks refetch
without corruption; partial upserts self-heal; cross-wallet same-tx
dedup holds by construction. The residual is the unpinned counting
domain (R4-M3) — fixable in two lines — plus the span arithmetic that
breaks the refetch loop itself (R4-M1).

**Prefix-emission under bounded concurrency 4:** the mechanism
(ordered position pull + maximal contiguous resolved-prefix assembly +
FAILED=resolved + beyond-prefix accounting) is implementable and kills
the completion-order nondeterminism R3-M3 targeted. What it cannot
deliver is cross-run equality under a wall-clock deadline — that's a
property-of-the-bound issue, not a mechanism issue (R4-m4, plus the
resolution/position definitional pins in R4-m7).

**Anything in §3 still unimplementable:** yes — the window formula
as literally written emits out-of-cap requests on every warm BSC
fetch (R4-M1), and the wire coverage fields are unservable from the
declared schema on no-fetch responses (R4-M2). R4-M3 is implementable
but produces wrong data under the natural reading.

**Do tests 1–22 cover what they claim:** mostly — T2/T9 now have a
real revalidation mechanism; T13 needs the self-transfer case added
(R4-M3); T22 needs its property restated (R4-m4); T18 depends on the
R4-M2 persistence fix to be meaningful on cached serves. Mechanisms
with no coverage at all: provider-switch reset, refresh-vs-cooldown,
stale-INDEXING recovery, exposure filters (R4-m10).

## Verified-as-true plan claims (credit where due)

- All 21 R3 amendments textually landed; plan.md remains
  self-contained (§1–§6 + tests 1–22 inline).
- `LookupWallet` three-bucket signature confirmed
  (sanctions/service.go:49,88–105); `LatestSnapshotPublished`
  exists (sanctions.sql:95) with `version` column for
  `sanctions_snapshot_version`.
- `EnrichmentNegativeTTL` = 60 s (config.go:81) — FAILED cooldown
  claim is exact; singleflight convention real
  (enrichment/service.go:39); `resilience.NewClient` +
  Hooks/WithClock confirmed (client.go:26–91).
- `INVALID_CURSOR` exists (middleware/errors.go:20); `ChainReference`
  eip155:1/56 confirmed; wallets `(network_id, normalized_address)`
  unique + `updated_at` convention confirmed (000001:14–24,
  wallets.sql:5).
- `NOT_IMPLEMENTED` touch-points all verified: service.go:297,
  mapper.go:323, model.go:171, service_test.go:354,
  openapi.yaml:1316, exactly 5 `screening-*.json` captures.
- migrations `//go:embed *.sql` auto-registers 000008 — no manifest
  step needed (migrations.go:8).
- Crash-safe ordering (batch → watermark), min-of-streams contiguous
  coverage, provider-switch reset, opaque PageToken, closed enums,
  spec §7.13 `is_error` deviation properly declared,
  `path_verified=false` honesty, deadline-bound-BSC posture — all
  correctly stated.

## Verdict

Round 3's amendments all landed, and the two biggest mechanisms are
architecturally right — content-hash identity dissolves the
partial-window/cross-wallet corruption class, and prefix-emission has
a real implementation shape. But three defects remain in the
load-bearing layer: the window arithmetic rejects every warm BSC
fetch (R4-M1), the wire coverage is unservable without schema additions
(R4-M2), and the occurrence-counting domain produces phantom rows and
lost copies under the natural implementation (R4-M3). All have
concrete, small specifiable fixes — no redesign needed.

**CHALLENGED** — 0 blockers, 3 majors (R4-M1…R4-M3), 7 minors
(R4-m4…R4-m10), 2 nit groups (R4-n11, R4-n12; ~12 items).
