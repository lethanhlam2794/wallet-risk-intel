# Devin Critic — Round 2: Transaction Tracing plan (post-amendment)

scope: `plans/20261104-0056-transaction-tracing/` (plan.md + 4 phase files
post-rewrite) verified against `rebuttal-round-1.md`, `docs/tailieu.md`
(§4.7, §7.13, §9.9–9.10, §11, §17), repo internals (resolver, provider.go,
sanctions three-bucket, singleflight, NormalizeEVM, wallets/data_sources DDL,
screening Coverage consumers, httpserver Deps, config TTLs), and **live
verification of NodeReal MegaNode docs** (docs.nodereal.io + API marketplace,
checked 2026-11-04).

Note: the plan directory is **untracked** (`git status` → `??`); the
pre-amendment plan.md exists nowhere — "unchanged"/"as before" claims cannot
be diffed, only taken on trust. See N-M1.

---

## Part 1 — Round-1 amendment verification

| # | Verdict |
|---|---|
| B1 | **Closed with new caveats** — premise now real (verified below) |
| B2 | **Mostly closed** — one endpoint shape still unhandled (N-M5) |
| B3 | **Mostly closed** — upstream window cap + cold-scan cost missing (N-M3) |
| M4 | **Closed** for /transactions; exposures root status is implicit (N-m9) |
| M5 | **Closed** — singleflight + test 14, deferral documented |
| M6 | **Reopened** — arithmetic is Etherscan-shaped; fails on NodeReal (N-M2) |
| M7 | **Closed** — bulk resolve + NormalizeEVM + bloat acknowledged |
| M8 | **Closed** — 60 s cooldown matches `EnrichmentNegativeTTL` (config.go:81) |
| M9 | **Mostly closed** — unmentioned consumers remain (N-m12) |
| M10 | **Closed** — flag-vs-category wording needs tightening (N-m11) |
| M11 | **Closed** — but hop-2 expansion order new gap (N-M6) |
| m12–m17 | **Closed except m15** — see N-M4 (schema contradiction + dead reorg claim) |
| n18–n21 | **Closed** |

### Verification (a) — BSC adapter premise: `nr_getAssetTransfers` is REAL

Verified at `docs.nodereal.io/reference/nr_getassettransfers` (updated
2026-04-02): JSON-RPC method, **"Supported on BSC and ETH mainnet only"**,
endpoint `https://{chain}-{network}.nodereal.io/v1/{API-key}` — so
`bsc-mainnet.nodereal.io` + `NODEREAL_API_KEY` is correct. MegaNode free
plan exists (3 keys, free CU quota, no card). The B1 premise is now sound.

**But the free-tier economics are materially different from Etherscan's**
and the plan does not state them:

- `nr_getAssetTransfers` costs **250 CU/request** (NodeReal API
  marketplace, Transaction Receipts-BSC listing). Free tier is
  **150 CUPS** per docs.nodereal.io/docs/pricing-plan (10M monthly CU) —
  though the marketplace page says 300 CUPS / 100M monthly CU. NodeReal's
  own docs disagree; cite the conservative figure. At 150 CUPS the
  sustained rate for *this exact method* is **0.6 calls/s** (1.2 at 300
  CUPS) — not ~3 req/s.
- `toBlock − fromBlock` must be **≤ 100,000 blocks** (hard upstream cap;
  omitting `toBlock` auto-fills +100000).
- Pagination is **`pageKey` (UUID cursor)** + `maxCount` (hex, max
  `0x3E8`=1000) — not `page`/`offset` numbers.
- Direction filters are **separate `fromAddress`/`toAddress`** params —
  full coverage needs two request streams per window.
- Category vocabulary is `"external"`, `"internal"`, `"20"`, `"721"`,
  `"1155"` — the plan writes `EXTERNAL`/`ERC20` (cosmetically wrong).
- Response fields per docs+example: `category, blockNum, from, to, value
  (hex), asset, hash, contractAddress, decimal, blockTimeStamp, gasPrice,
  gasUsed, receiptsStatus, erc721TokenId, erc1155Metadata` — **no
  `traceId`, no `logIndex`, no `uniqueId`, no confirmations count**.
  `receiptsStatus` (1/0) is the `isError` equivalent. `asset`/`decimal`
  may be `null`. `external` rows include zero-value contract
  interactions (Etherscan "normal tx" semantics; `excludeZeroValue`
  param exists).

### Verification (b) — `transfer_index` across endpoint shapes

- chainscan `txlist` → `n:0` always (one external row per tx — `n:{i>0}`
  is unreachable, harmless).
- chainscan `tokentx` → `t:{i}` ordinals work given asc prefix-stability.
- chainscan `txlistinternal` → `i:{traceId}` — *if* the plan even uses
  this endpoint (never stated — calls-per-index arithmetic is
  ambiguous, see N-M2).
- **nodereal `internal` → no `traceId` exists upstream** → `i:{traceId}`
  cannot be derived. The vocabulary is presented as closed but does not
  cover this shape. Either `i:{ordinal}` fallback or explicit
  internal-exclusion on nodereal is required (N-M5).
- Ordinal union subtlety: on nodereal the *same tx* can contribute rows
  to both the `fromAddress` and `toAddress` streams (e.g. a swap: W
  sends token A, receives token B in one tx). "Ordinal within the
  same-tx transfer group" must be computed over the **union of all
  direction/category streams in the window**, or the streams produce
  colliding `t:0`s / unstable ordinals across refetches. The plan's
  "complete fetched window" phrasing is close but should say "union of
  all streams" explicitly.

### Verification (c) — watermark + windowed paging arithmetic

Ascending watermark semantics are correctly specified, and the safe
ordering (upsert → then watermark update) is implied by §3.2 ordering.
Missing terms in the arithmetic:

- **The 100k-block window cap is absent.** On BSC, `(watermark+1 .. head]`
  must be `(watermark+1 .. min(head, watermark+100000)]`. Cold-start
  consequence: BSC head ≈ 60–70M blocks → a never-indexed wallet needs
  ~650 sequential windows × 2 direction calls ≈ **~1300 provider calls to
  converge to head even with zero transactions** (~20–40 min of limiter
  wait at free-tier CUPS — see N-M2). The plan must state this cost and
  the chosen posture (incremental lazy convergence is honest; the
  arithmetic must appear, or a different cold-start strategy declared).
- The "10k record cap → window splits" branch is **dead code at the
  stated defaults** (page_size 1000 × max_pages 5 = 5000 < 10000). The
  operative truncation is `max_pages` biting mid-window; the split only
  guards future configs — say so. Floor case unstated: a single block
  exceeding the record budget cannot be split below block granularity
  (declare the residual or the per-block progress rule).
- Head-fetch (`eth_blockNumber`/`eth_block_number`) is an extra provider
  call per `EnsureIndexed` not counted in "endpoints × pages".
- `history_truncated` needs one more line of definition: true iff the
  fetch stopped due to a bound (`max_pages`/window rules), distinct from
  the always-true "indexed_until < advancing head".

### Verification (d) — bound precedence + truncated enum

Precedence `deadline > max_provider_requests > max_nodes > max_edges >
response_cap` and the closed 5-value enum are concrete and consistent
across plan.md and phase-03. ✔ Two residues folded into findings: the
budget arithmetic is wrong on NodeReal (N-M2), and which hop-1 nodes get
expanded under a binding budget is unspecified → nondeterministic
result subsets (N-M6).

### Verification (e) — coverage on the wire, both endpoints

- `/transactions`: envelope explicitly lists `{index_status,
  provider_status, indexed_until_block, indexed_at, history_truncated,
  next_cursor}`. ✔ Closed. (`window_covered` exists in service-level
  coverage but not on the wire — acceptable since `indexed_until_block`
  conveys the watermark; note it.)
- `/exposures`: "items per §9.10 + coverage + truncated enum" — §9.10
  response fields do include `coverage` and `truncated`, so spec-wise it
  reaches the wire. ✔ But placement is ambiguous (item-level vs
  envelope — plan §3.3 item contract lists `coverage{}`/`truncated`
  *inside* the item contract while phase-03:8 enumerates aggregate
  stats) and the root wallet's `index_status`/typed `provider_status`
  has no explicit wire slot on exposures (§3.1's "DISABLED + reason
  inside coverage" implicitly covers it — make it explicit). N-m9.

### Verification (f) — `provider_reported_only` consumers

Decision (a) is honestly declared in §6 and phase-04 covers the OpenAPI
enum (`api/openapi.yaml:1316` `enum: [NOT_IMPLEMENTED]`). Value is
accurate — screenings remain provider-reported-only. **Unmentioned
consumers** (N-m12): `internal/screening/service.go:297`,
`mapper.go:323` (two hardcoded sites — implied but unlisted),
`service_test.go:354` (assertion), `model.go:171` (comment), and
**`docs/feature-maps/assets/*.json` ×5** captured-response fixtures
that will keep asserting `NOT_IMPLEMENTED` after phase 6 ships —
regeneration/refresh is unmentioned anywhere.

---

## Part 2 — New findings

## [Major] N-M1 — The rewrite deleted §1/§2 and tests 1–12 while claiming them "unchanged"/"as before"

plan.md now reads `§1 Scope — unchanged (see phase files)` —
**no phase file contains a scope section**; `§2 Invariants —
unchanged, plus:` — the base invariant list is gone from the artifact
set; `§5 — 1–12 as before, plus:` — **tests 1–12 are defined nowhere in
the current documents**, and phase-04:10's "full required list incl.
tests 13–20" cannot be satisfied from the artifact set. The directory is
git-untracked so nothing is recoverable. A Cook implementer cannot
reconstruct half the required test list reliably (the round-1 critique
references only tests #9 and #11 by number).

**Amendment:** restore §1 scope, §2 base invariants, and the
enumeration of tests 1–12 inline in plan.md — the plan must be
self-contained; cross-references to a document that no longer exists are
dangling pointers, not amendments.

## [Major] N-M2 — Bound arithmetic fails on the NodeReal adapter: 250 CU/call vs 25-call budget inside a 25 s deadline

§3.3 publishes `endpoints × pages ⇒ 4–12 nodes` — an Etherscan-shaped
model (per-request limiting ~3 req/s). On the BSC adapter:

- each `nr_getAssetTransfers` = **250 CU**; free tier 150 CUPS ⇒
  ~0.6 req/s sustained (1.2 req/s if the 300-CUPS figure is the real
  one). 25 `max_provider_requests` ⇒ **~21–42 s of limiter wait alone**
  — the deadline essentially always fires first on any BSC hop-2 of
  size, so the effective node bound on BSC is ~4–15 *calls* (fewer
  nodes) and `truncated=deadline` is the norm, not the edge.
- Direction streams double the call count (`fromAddress` + `toAddress`),
  and head-fetch adds +1/EnsureIndexed — neither is in
  `calls_per_index`.
- Whether chainscan indexes INTERNAL (`txlistinternal`) changes
  `endpoints` 2→3 — unstated; the nodereal hedge "INTERNAL only if the
  tier exposes it" leaves `endpoints` adapter-dependent without saying
  so.
- Monthly quota: 10M–100M CU ÷ 250 = 40k–400k calls/month — hop-2
  indexing burns this measurably; worth one line.

**Amendment:** state per-adapter `calls_per_index`
(chainscan: `endpoints{2 or 3, say which} × pages + 1 head`;
nodereal: `2 directions × pages + 1 head`), per-adapter sustained rates
(chainscan ~3/s; nodereal ~0.6–1.2/s under CU weighting), and either
per-adapter `max_provider_requests`/deadline defaults or an explicit
statement that BSC hop-2 operates deadline-bound by design. Cite the
NodeReal docs discrepancy (150 vs 300 CUPS; 10M vs 100M monthly) and
take the conservative figure.

## [Major] N-M3 — The 100k-block upstream window cap is missing from the windowing contract

`nr_getAssetTransfers` rejects ranges >100,000 blocks. §3.2's
`(watermark+1 .. head]` will emit rejected requests on BSC until the
window is clamped: `to = min(head, watermark + 100_000)`. The cold-start
consequence (Part 1c) — ~1300 calls for an empty BSC wallet to converge
to head — must appear in the plan with the chosen posture (lazy
incremental convergence + `history_truncated`/coverage honesty, or a
declared cold-start strategy).

**Amendment:** add the per-adapter window cap to §3.2 (chainscan: no
range cap, record-cap only; nodereal: 100k blocks) and publish the
convergence arithmetic for a cold wallet on each chain.

## [Major] N-M4 — m15 is re-opened: `isError` has no column under "§7.13 verbatim", and the reorg "revalidation" cannot work under `ON CONFLICT DO NOTHING`

Two contradictions the amendment introduced:

1. §3.4 claims `transactions` is "spec §7.13 verbatim" **and** that
   `isError=1` is "carried on a separate field". Spec §7.13
   (tailieu.md:888–913) has **no such column** — the fields are id,
   network_id, tx_hash, transfer_index, transfer_type, from/to,
   asset_identifier, amount_raw, block_number, block_timestamp,
   confirmation_status, created_at. A new column is a schema deviation
   undeclared in §6; dropping the signal contradicts "carried on a
   separate field". Pick one and declare it.
2. §3.4's reorg posture — "refresh re-fetches the watermark tail window
   (revalidates recent rows)" — **is dead on arrival**: every upsert is
   `ON CONFLICT DO NOTHING`, so a refetched row that changed
   (confirmation status, block_number/timestamp after a reorg) is
   *not* updated; a reorged-out tx persists forever. The stated
   mitigation does not perform revalidation. Spec §7.13 makes reorg
   handling a listed rule.

**Amendment:** (a) declare the deviation — add an
`is_error`/`exec_status` column (or move it into an evidence JSONB) —
or state `isError` is dropped; (b) for the tail window, upsert becomes
`ON CONFLICT DO UPDATE` on the mutable columns (confirmation_status,
block_number, block_timestamp — define the mutable set), or downgrade
the claim to "residual: stale rows possible within/behind the tail
window" as an honest limitation. Do not keep a claim the upsert rule
cannot fulfill.

## [Major] N-M5 — `i:{traceId}` has no upstream field on NodeReal internal rows — the identity vocabulary is still not closed over all endpoint shapes

The nodereal response schema carries no `traceId`/`logIndex`/`uniqueId`
(verified field list, Part 1a). If `internal` is fetched on BSC, no
`i:{traceId}` can be derived; skipping internal silently is the fake-
coverage shape B2 existed to kill.

**Amendment:** extend the vocabulary — `i:{ordinal}` when the upstream
row carries no trace identifier (ordinal computed over the tx's internal
group in the fetched-window union), or state `internal` is excluded on
nodereal with the coverage consequence (BSC indexes
external+token only) declared in §3.4/§6. Also state the union rule
explicitly: ordinals are computed over the union of **all**
direction/category streams for the tx within the fetched window, not
per-stream.

## [Major] N-M6 — Hop-2 expansion order is unspecified → non-reproducible exposure subsets under a binding budget

`max_provider_requests` binds before `max_nodes` at the stated defaults
(§3.3's own arithmetic: 4–12 effective nodes of 25). Which hop-1
counterparties get expanded — and therefore which exposures exist in the
response — depends on iteration order, which is unspecified; under
concurrency-4 + deadline, completion order is nondeterministic, so the
same request can return different matched items run-to-run even without
upstream change. Coverage counts `nodes_examined` but cannot express
"you saw the first N of a deterministic ordering".

**Amendment:** define a deterministic expansion order (e.g. hop-1
counterparties ordered by `(earliest edge block_timestamp, tx_hash,
normalized_address)`) and state that budget/deadline truncation emits a
**prefix** of that order, with coverage reporting `{nodes_examined,
nodes_remaining}`. Also state how a failed hop-2 `EnsureIndexed`
(FAILED/DISABLED node) surfaces — skipped-node count or per-node status
in coverage, never silent.

## [Minor] N-m7 — `cross_chain_listing`: "distinguished flag" vs the `category` field is undefined, as is whether cross-chain-only counterparties become items

§3.3.5 says cross-chain/unresolved listings "report as
`cross_chain_listing` on a distinguished flag" — but the item contract
defines `category`, not a flag field. Is `category` itself
`cross_chain_listing` (then it's a category, not a flag), or a new
boolean/enum field? And does a counterparty whose *only* match is a
cross-chain listing count as "matched" (item emitted) under "only
matched counterparties become items"? Both readings are defensible;
silence is not (M10's own standard).

**Amendment:** fix the item schema: name the closed `category` enum
(label categories + `SANCTIONED` + decide whether `cross_chain_listing`
is a category or a separate `listing_scope` flag on the item), and state
that a cross-chain-only listing does/doesn't produce an item. Also state
whether label `verification_status` (`reported` vs `verified`)
propagates onto matched items — the "reported stays reported" invariant
travels with the label, not just the label domain.

## [Minor] N-m8 — `TxProvider` seam typing: `Page` cannot express `pageKey`; `NextPage` type unstated

`opts{FromBlock, ToBlock, Kinds, Page, PageSize}` + `TransferPage.NextPage`
— on chainscan `Page` is an int page number; on nodereal it is a UUID
cursor. If `Page` is int-typed the seam cannot serve the BSC adapter.

**Amendment:** declare the pagination field an opaque token
(`PageToken string`/`Cursor string`; adapters encode/decode — chainscan
encodes its page number, nodereal passes `pageKey` verbatim; empty =
first page / no next).

## [Minor] N-m9 — Exposures envelope: coverage placement, root index status, and failed-node surfacing

plan.md §3.3's item contract puts `coverage{}`/`truncated` *inside* each
item while phase-03:8 describes aggregate request-level coverage
(`nodes_examined`, `provider_calls`, …). §9.10 lists them once — the
wire shape needs one decision: envelope-level `coverage` + `truncated`
(recommended; per-item `data_coverage` only if deliberately duplicated).
Also: the root wallet's `index_status`/typed `provider_status`
(ErrKind) and hop-2 node failures have no declared wire slot — §3.1's
"DISABLED + reason inside coverage" is the right idea; make the field
explicit (e.g. `coverage.provider_status`, `coverage.nodes_failed`),
matching the M4 honesty bar.

## [Minor] N-m10 — `/exposures` `category` filter (§9.10 param) has no stated semantics

Spec §9.10 lists `category` as a parameter. Post-assembly filtering is
fine (budget already spent — coverage stays honest) but must be stated,
alongside the closed category enum (N-m7). Also `max_hops` default
unstated (2?). Whether `refresh=1` applies to /exposures as well as
/transactions is unstated.

## [Minor] N-m11 — `Enabled("bsc")=false` hardcodes away the paid-key path; registry precedence unstated

chainscan `Enabled(net)` returning static false for chainid 56 is
correct for a free key but wrong for an operator with a paid plan — the
adapter cannot see the key's tier. Also the network→adapter registry
(config-driven per phase-01:12) needs one line: single adapter per
network, and what happens if two adapters claim the same net.

**Amendment:** drive `Enabled` by config (`enabled_chains` list) with
the documented default `bsc` excluded for free-tier keys, rather than a
baked constant.

## [Minor] N-m12 — `provider_reported_only` consumers not fully enumerated (see verification f)

Amendment: phase-04 should list all touch points: `service.go:297`,
`mapper.go:323`, `model.go:171` comment, `service_test.go:354`,
`openapi.yaml:1316` enum, and regeneration of the five
`docs/feature-maps/assets/*.json` captures. Also decide whether the new
enum stays closed (`[provider_reported_only]`) or documents future
values — a closed single-value enum just recreates this drift later.

## [Minor] N-m13 — Index-scope gaps never declared: zero-value `external` rows, NFT categories, token rows missing `contractAddress`, null `from`

- `external` includes zero-value contract interactions (approvals,
  calls) — indexed as NATIVE amount_raw=0, or excluded via
  `excludeZeroValue`? The /transactions contract changes either way;
  declare it.
- `"721"`/`"1155"` categories are out of scope — state that the index
  covers native + fungible-token transfers only, so a wallet whose
  activity is NFT-only reads as an honest empty index, not a bug.
- Spec rule "Token transfer phải có token contract": behavior for a
  TOKEN row with missing `contractAddress` unstated (skip+count or
  reject window).
- `from`/`to` can be absent on some internal/creation rows —
  NormalizeEVM failure ⇒ row skipped or NULL wallet id — unstated.

## [Minor] N-m14 — `index_status` / `wallet_index_state.status` vocabulary never enumerated

Envelope fields reference a `status`/`index_status` whose closed
vocabulary is never listed (`FRESH`/`STALE`/`PARTIAL`/`FAILED`/
`DISABLED`? — and how a mid-flight first index reports). Same for the
`provider` column type (data_sources FK vs code string) — phase-01
says "register tx source in data_sources" but not whether
`wallet_index_state.provider` references it.

## [Minor] N-m15 — Paths returning to root; self-edges at hop-2

Self-counterparties excluded (M11 ✔) but a 2-hop path `root→C→root`
is nonsense evidence and unstated; likewise hop-2 self-edges of C.
**Amendment:** exclude edges/paths that terminate at root or self-loop
at any hop (one line + a test).

## [Nit] N-n16 — Upsert→watermark ordering is crash-safe only because `DO NOTHING` dedups; state it explicitly (batch must commit before the watermark update; partial-batch ⇒ watermark must stay behind the last *contiguously* covered block).

## [Nit] N-n17 — `confirmation_status` PENDING is nearly unreachable (both upstreams only return mined txs; nodereal has no confirmations field — the adapter must compute `head − blockNum + 1`); spec DDL default is lowercase `'pending'` vs plan's uppercase vocabulary — align deliberately.

## [Nit] N-n18 — `data_sources` row count unstated (one tx source, or one per adapter for per-network provenance?).

## [Nit] N-n19 — `provider` column carryover on adapter switch (wallet_index_state written by adapter A later read under adapter B — accept or re-index; one line).

## [Nit] N-n20 — Snapshot version of the sanctions snapshot used for counterparty matching is not carried in exposure coverage (audit-trail nicety; sanctions lookup is snapshot-scoped already).

## [Nit] N-n21 — NodeReal docs field-name inconsistency `blockTimeStamp` (example) vs `blockTimestamp` (reference table) — parse tolerantly; metrics should reuse `provider_requests_total`/`provider_errors_total` labels rather than inventing a parallel counter family (plan says "tx fetch/duration counters (§17)").

## [Nit] N-n22 — `asset` filter vocabulary for NATIVE rows (`asset_identifier` NULL — what param value selects native transfers?) unstated; deadline-truncated responses must still emit valid JSON cut at item boundary (impl note for the ~256 KB cap).

---

## Verified-as-true plan claims (credit where due)

- `nr_getAssetTransfers` exists, is documented, supports BSC mainnet,
  and a real free tier exists — the B1 re-source is viable (with the
  rate/window caveats above).
- `chainid` model correct: seeds carry `eip155:1`/`eip155:56`
  (000001_init.up.sql:168–169) matching `net.ChainReference`.
- `Enabled(net)` per-network DISABLED-with-reason posture is the right
  honest shape; escalation clause retained.
- singleflight convention real (`enrichment/service.go:39`,
  `label/service.go:103`); advisory-lock helpers real
  (`ingestion/locks.go`); 60 s cooldown matches
  `EnrichmentNegativeTTL` (config.go:81).
- `LookupWallet` three-bucket semantics real
  (`sanctions/service.go:97–104`; `queries/sanctions.sql`) — only
  same-network direct yields SANCTIONED per M10.
- `GetOrCreateWallet` is single-row (queries/wallets.sql) — bulk
  resolve genuinely needed and specified.
- `wallets.address_type` default `'unknown'` exists (000001:20);
  `wallets.id` globally unique (PK) — n19 resolution sound.
- `data_sources` registration convention real (000005/000006 precedents);
  `ingestion_runs.source_id` FK exists.
- `ErrKind` closed vocabulary exists verbatim (provider.go:155–168);
  `httpserver.Deps` injection seam real (server.go:38–60); no
  conflicting server write-timeout for a 25 s deadline (only
  `ReadHeaderTimeout`; `/readyz`'s 3 s ctx is probe-scoped).
- `tracing_nodes_examined_total` is the §17-listed metric; §9.9 filters,
  §9.10 params/response fields, §11.4 constraints, §11.5 evidence
  fields all cited accurately; `refresh=1` correctly moved to §6.
- Migration 000008 is the next free number; `internal/transaction`/
  `internal/tracing` are new packages matching the AGENTS layout.

## Verdict

The B1 re-source held up — `nr_getAssetTransfers` on NodeReal's free
tier is real and the per-chain ENABLED matrix is the right posture. But
the rewrite left the plan **non-self-contained** (§1/§2/tests 1–12
dangle), re-broke the m15 area twice (schema contradiction; a
revalidation claim `DO NOTHING` cannot satisfy), kept a `transfer_index`
vocabulary that still does not cover every endpoint shape, and published
bound arithmetic that is Etherscan-shaped — on the BSC adapter the
stated defaults make `truncated=deadline` the norm and cold-start
convergence costs ~1300 calls, unstated. None of these kill the design;
all are specifiable amendments — but they are exactly the class of
silent-gap/hand-waved defects this review exists to catch.

**CHALLENGED** — 0 blockers, 6 majors (N-M1…N-M6), 9 minors, 7 nits.
