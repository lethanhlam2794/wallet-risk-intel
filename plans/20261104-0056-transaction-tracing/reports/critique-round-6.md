# Devin Critic — Round 6: Transaction Tracing plan (post-R5-amendment)

scope: `plans/20261104-0056-transaction-tracing/` (plan.md + 4 phase
files) verified against `rebuttal-round-5.md` (6 amendment groups),
`docs/tailieu.md` (§7.13, §9.9–9.10, §11.4–11.5), and repo internals
(`migrations/000003/000005/000006`, `queries/{wallets,labels_read,
sanctions}.sql`, `internal/sanctions/service.go`,
`internal/screening/{service.go,mapper.go,model.go,service_test.go}`,
`internal/scoring/model.go`, `internal/enrichment/service.go`,
`internal/provider/{provider.go,resilience/client.go}`,
`internal/platform/metrics/metrics.go`, `internal/config/config.go`,
`internal/httpserver/server.go`, `internal/wallet/address.go`,
`api/openapi.yaml`, `docs/feature-maps/assets/*.json`).

Carried caveat (R2–R5, still true today): the plan directory and the
Phase-5 code remain git-**untracked** (`git status` → `??`). All
"amended" claims verified textually only.

---

## Part 1 — R5 amendment verification

All 6 amendment groups **textually landed in plan.md**. Phase files did
**not** receive the full sync — three stale lines survive (→ R6-m6) —
and one rebuttal claim is overstated (the ListAllWalletLabels join set
is pinned nowhere).

| # | Verdict |
|---|---|
| R5-M1a DISABLED ⇒ `status=disabled` + `error='disabled'`, zero calls, no unearned cooldown; typed failure ⇒ `failed`; error cleared-on-success | **Landed** — plan.md:155–158, phase-02:7–8. |
| R5-M1b `provider_status` = persisted ErrKind when non-empty; `"ok"` only after succeeded attempt | **Landed** — plan.md:330–334. Two residual undefined states (`indexing`/`not_indexed` serve paths) → **R6-m4**. |
| R5-M1c `status=disabled` re-evaluates `Enabled()` per request | **Landed** — plan.md:134–135. |
| R5-M1d `stale` = derived boolean beside `index_status`, composable over any status | **Landed in §3.4/§3.3.8/phase-02** — plan.md:326–329, coverage.root:266, phase-02:9. **Absent from the §3.5 `/transactions` field list** → **R6-m1**. |
| R5-m2 matched_signals per-kind schema `{kind,category,label?,entity?,programs[]?,source,verification_status?,network?,listed_address?}`; LEGAL first; kind distinguishes SANCTIONED; ListAllWalletLabels join set pinned | **Partial** — schema + per-kind applicability + LEGAL + kind rule landed (plan.md:223–236). **Join-set pin absent** (plan.md:341–345 still pins only the WHERE) and **phase-03:7 retains the old schema** → **R6-m6**. |
| R5-m3 `head < fetch_from` ⇒ no upstream call; serve persisted; coverage note | **Landed** — plan.md:146–149. The promised coverage note has no wire field → **R6-m5**. Phase-02:6 lacks the clause (minor sync). |
| R5-m4 self-row emitted once with `direction="self"`; in\|out filters both match | **Landed** — plan.md:369–372, §6:474–475, test 21b:439–443. Phase-02:9 still describes filter-matching only → R6-m6 nit-line. |
| R5-m5 `IndexingFreshTTL=10m` + `IndexingStaleAfter` named (≠ EnrichmentDeadline) | **Landed** — plan.md:126–133, phase-01:12. |
| R5-n6 17 nits (window_covered dropped; partial⟺flag; cumulative rows_skipped; counterparties_total domain; labels_snapshot_version + drift; source_type='api'; duration via call-site/OnDuration; openapi.yaml:14; max-count premise; empty stream = complete-to-fetch_to; error cleared-on-success; /transactions limit≤100 + cursor cap; lowercase divergence; normalized-address path ordering; edge asset="native"; transfer_index on edge; tests 21b) | **Landed in plan.md** — verified at 151–153, 171–172, 240–241, 243–244, 254–268, 300–306, 322–334, 367–368, 439–443, §6:475–476, phase-01:12, phase-04:8–9. Two landings expose follow-ons → **R6-m2** (counterparties_total arithmetic at max_hops=1), **R6-m6** (labels_snapshot_version + root.stale missing from phase-03:9). |

Summary: 6/6 landed in plan.md; three produce or leave defects
(R5-M1d→R6-m1, R5-m2→R6-m6, R5-m3→R6-m5; R5-n6's coverage-domain nit
→ R6-m2).

---

## Part 2 — New findings

## [Minor] R6-m1 — `stale` is unservable on `/transactions`: the §3.5 closed field list omits the field §3.4 declares "on the wire"

R5-M1d made `stale` a derived boolean "beside `index_status` …
composable over any status" (plan.md:327–329) precisely so TTL-lapsed
`disabled`/`failed`/`partial` states stay distinguishable. It landed in
`coverage.root` (plan.md:266) and phase-02:9 (`index_status(+stale
derived)`) — but the `/transactions` envelope enumeration at
plan.md:358–361 reads:

```
{index_status, provider_status, indexed_until_block, indexed_at,
 history_truncated, index_scope, rows_indexed,
 rows_skipped{missing_contract, invalid_address}, next_cursor}
```

No `stale`. A literal implementation of §3.5 drops the field on the
higher-traffic endpoint, recreating the R5-M1d gap (a TTL-lapsed
`disabled` wallet reads `index_status=disabled` with no way to say
"and also stale"; conversely a `fresh` row past TTL is
indistinguishable from a truly-fresh one).

**Amendment:** add `stale` to the §3.5 envelope between `index_status`
and `provider_status` (same derived rule — `indexed_at` vs
`IndexingFreshTTL`, NULL `indexed_at` ⇒ `stale=true`). One word; the
derivation is already pinned.

## [Minor] R6-m2 — `max_hops=1` breaks the declared coverage invariant `examined + remaining = counterparties_total`

plan.md:254–259 pins:

- `counterparties_total` = expansion positions after sentinel exclusion
- `nodes_remaining` = never attempted; **`examined + remaining =
  counterparties_total`** (restated in phase-03:9 as `∑=total`)
- "`max_hops=1` ⇒ all `nodes_*` = 0"

At `max_hops=1` with N>0 counterparties the literal reading yields
`counterparties_total=N`, `nodes_examined=nodes_remaining=0` →
`0+0 ≠ N`. Every reading contradicts something: zeroing the position
set makes `counterparties_total` lie (the counterparties exist and
emit hop-1 items); keeping it breaks the sum. This is an arithmetic
contradiction on a first-class documented parameter — verifiable on
the wire.

**Amendment:** replace "`max_hops=1` ⇒ all `nodes_*` = 0" with:
"`max_hops=1` ⇒ `nodes_examined`=`positions_emitted`=`nodes_failed`=0
and `nodes_remaining`=`counterparties_total` — positions exist
(counterparties were discovered) but expansion is skipped, so every
position is by definition 'never attempted'." Preserves both the
invariant and the information. (Acceptable alternative:
`counterparties_total`=0 — positions never materialize. The
`remaining=total` reading is strictly more informative; pick one.)

## [Minor] R6-m3 — Counterparty dedup across hop depths is unpinned: one address can emit as both `direct_counterparty` and `indirect_hop`

Nothing states what happens when the same normalized address is both a
hop-1 counterparty (direct edge from root) and reachable at hop-2
(root→X→Y with Y also a direct counterparty). Under the current text
an implementation may emit **two items for one counterparty** —
`hops=1`/`direct_counterparty` and `hops=2`/`indirect_hop` — or, if it
dedupes by first-seen assembly order, label a **direct** counterparty
as `indirect_hop` (a relationship mislabel the §2 vocabulary exists to
prevent, and a double-count for any consumer aggregating by
counterparty). `max_paths_per_pair` only bounds paths *within* an
item; it doesn't define item identity across depths.

**Amendment:** pin one item per counterparty — "emitted items dedupe by
`counterparty` (normalized address) across hop depths: `hops` = the
minimum depth at which the counterparty is evidenced, `relationship`
follows `hops`, and `path[]`/`transaction_evidence` are restricted to
min-hop sequences. Hop-2 paths terminating at an already-emitted
counterparty contribute nothing (its edges may still appear inside
other items' paths). Dedup precedes category selection —
`matched_signals` are depth-independent so precedence is unaffected."

## [Minor] R6-m4 — Index-state lifecycle under-pinned: `indexed_at`'s clock role, and `indexing`/`not_indexed` have no gate membership or `provider_status`

Two linked gaps around `wallet_index_state` reads:

**(a) `indexed_at` is the cooldown clock — but reads as
"last-success".** The FAILED cooldown ("60 s per
`EnrichmentNegativeTTL`", plan.md:127–129) and the `stale` derivation
(plan.md:327–329) both measure from `indexed_at` — the only timestamp
on the row. If `indexed_at` = last *successful* index, a freshly
failed row keeps its old `indexed_at` and the cooldown has no clock to
measure (or measures from the wrong event and never engages / always
engages). The only schema-consistent reading is "last terminal write",
and nothing says it.

**(b) `indexing`/`not_indexed` rows are outside every declared
rule.** The freshness gate covers "fresh index-state" (undefined
membership), the FAILED branch covers `failed`, the DISABLED branch
covers `disabled`. A fresh `indexing` row (indexed_at within TTL —
e.g., a mid-index row read cross-process, the advisory lock being
explicitly deferred) either satisfies the gate — serving DB-only with
`provider_status` hitting the empty-error path, which the R5-M1b
ok-gate now *forbids* (`"ok"` requires `status ∈ {fresh,partial}`) →
**undefined wire value**, and under a naive else→"ok" reading the
exact false-ok R5-M1b killed — or doesn't satisfy it and falls to the
fetch path, which is probably the intent but is unstated. A
`not_indexed` row with empty error has the same undefined
`provider_status` if it ever serves.

**Amendment:**
1. "`indexed_at` = the last EnsureIndexed **terminal** write
   (`fresh|partial|failed|disabled`), written in the same upsert as
   `status`/`error` — it is simultaneously the FAILED-cooldown clock
   and the `stale` clock; never-indexed rows hold NULL ⇒ `stale=true`."
2. "The freshness gate's 'fresh index-state' = `status ∈ {fresh,
   partial}` ∧ `indexed_at` ≤ `IndexingFreshTTL`. `indexing` rows never
   satisfy it: within `IndexingStaleAfter` an in-process caller joins
   via singleflight (same `(network_id,wallet_id)` key); past it, the
   row is retryable per the existing clause. `not_indexed` always takes
   the fetch path. `provider_status` therefore only ever appears on
   rows with `status ∈ {fresh,partial,failed,disabled}` — the pinned
   rule is total."

## [Minor] R6-m5 — The R5-m3 "coverage notes the head-behind-watermark condition" has no carrier: no declared field can hold the note

plan.md:146–149 lands the no-call rule correctly, then promises
"coverage notes the head-behind-watermark condition (no status
transition)". Neither envelope contains a field for this:
`/transactions` has `{index_status, provider_status, indexed_until_block,
indexed_at, history_truncated, index_scope, rows_indexed,
rows_skipped, next_cursor}` — all closed/pinned values; `provider_status`
is ErrKind|`"ok"`; `index_status` is the enum. The condition is real
(the head *was* fetched — that call counts toward the pool, so the
value is in hand) but unservable as written — a consumer sees
`indexed_until_block` with nothing to compare it against.

**Amendment:** add `head_block` (nullable integer — populated whenever
a head fetch ran this request: fresh window fetch and the
head-behind path; null on no-fetch serves) to both envelopes.
Head-behind is then self-describing on the wire:
`head_block < indexed_until_block`. Cheaper alternative if a new field
is unwanted: drop the "coverage notes" clause to "(no status
transition)" — but the field is one line and makes the R5-m3 condition
*verifiable*, which was the point.

## [Minor] R6-m6 — Phase files still carry pre-R5 text: phase-03's stale `matched_signals` schema can silently resurrect the R5-m2 defect in code

plan.md is authoritative, but the phase files are the task list Cook
executes — and three lines contradict/miss R5 amendments:

- **phase-03:7** still reads `matched_signals[] {kind:…, category,
  entity?, program?, label_source, verification_status?}` — singular
  `program?`, `label_source` name, no `label?`/`network?`/
  `listed_address?`, no per-kind applicability, no LEGAL in the
  intra-tier order, no kind-distinguishes-SANCTIONED clause. Implement
  this line literally and the wire loses exactly the columns R5-m2
  re-added (which label matched; which network/address a listing
  names; multi-program OFAC entities).
- **phase-03:9** envelope lacks `labels_snapshot_version` and
  `root.stale` — both landed in plan.md §3.3.8 (262–268) but not here.
- **phase-02:9** says "W→W self-row matches direction in AND out" —
  filter semantics only; the R5-m4 emission rule (once, with
  `direction="self"`) is absent.
- **phase-02:6** omits the `head < fetch_from` no-call clause.
- **phase-01:7 / join set:** rebuttal-round-5 claims the bulk label
  query "pins ListAllWalletLabels join set" — it is pinned nowhere
  (plan.md:341–345 and phase-01:7 carry only the WHERE). The
  matched_signals fields force the joins (`rc.code`+`category_type`,
  `e.name`, `ds.code` per labels_read.sql:6–16), so this is a
  rebuttal-accuracy gap more than a design one — but "claims pinned,
  is not" is how the next stale-schema slips through.
- All four phase headers still read `amended R4`.

**Amendment:** sync the phase files —
phase-03:7 → `matched_signals[] {kind: sanctions_direct|label|listing,
category, label?, entity?, programs[]?, source, verification_status?,
network?, listed_address?}` + intra-tier `LEGAL > MALICIOUS_ACTIVITY >
SERVICE_TYPE > ENTITY_TYPE > OTHER` + "SANCTIONED item distinguished
by kind";
phase-03:9 → `…sanctions_snapshot_version + labels_snapshot_version
(null ok)… root{index_status, stale, provider_status, …}`;
phase-02:9 → "self-row emitted **once** with `direction="self"`;
in|out filters both match";
phase-02:6 → append "head<fetch_from ⇒ no upstream call (serve
persisted; head_block on wire)";
phase-01:7 → name the join set `(risk_categories code+category_type,
entities.name LEFT, data_sources.code)` or drop the rebuttal claim;
headers → `amended R5` (or drop round metadata entirely — it decays
every round).

## [Nit] R6-n7 — Ordering, domain, and shape nits

- Hop-2 item ordering: "expansion order" is defined for *positions*,
  not items — a counterparty discovered via positions `i<j` needs a
  sort key. Pin "min contributing position index".
- `node_indexed_until` domain: pin "attempted (examined) positions
  only" — never-attempted positions are `nodes_remaining`, not map
  entries, or the map doubles as an unbounded address list.
- Self-row under `direction=in|out` filters: pin the emitted value is
  always `"self"` (the row's fact), not the echo of the requested
  filter — otherwise the same row serializes differently per filter.
- `max_edges` "declared edge order": name it — e.g., per-node scan
  ordered `(block_timestamp, tx_hash, transfer_index)`, node order =
  expansion order.
- `matched_signals` field shapes: `programs[]` element shape
  (`ProgramRef {code,name,description,source_url}` as served by
  sanctions? or codes only?), `source` shape (`data_sources.code`
  string vs `{code,name}`), `entity?` = `entity_name` vs
  `{id,name}` — pick and pin once for the OpenAPI author.
- `/exposures` `direction`: state that it filters the hop-1 edge set
  *before* position derivation (positions derive from filtered edges)
  — inferable, but it decides `counterparties_total`.
- `edges[]` per path: pin "one picked edge per hop position" (the
  edge-pick rule operates per position along the sequence, not per
  path).
- `truncated` when untruncated: pin `null` vs field-absent
  (recommend `null` — closed enum stays closed).
- `history_truncated` lifecycle: the ⟺ pin implies it clears on an
  unbounded successful fetch — one clause makes it airtight.
- `partial`-vs-flag write discipline: pin "one upsert writes
  `status='partial'` ∧ `history_truncated=true` together; the flag is
  authoritative on read" — kills the drift path the "stored once"
  phrasing gestures at.
- `block_timestamp` NULLs: the column is nullable (spec §7.13) while
  ordering/pagination/`temporal_order_valid` all consume it — declare
  "indexed rows always carry block data (both adapters return mined
  transactions only)" or pin NULLs-last.
- Indexing service package: spec §6 layout puts indexing in
  `internal/transaction/`, BFS in `internal/tracing/` — plan pins
  `internal/tracing` for §3.3 only; name the service's package.
- Required-test additions matching R5 landings: head-behind-watermark
  no-call serve; `stale` present on `/transactions`; self-row
  `direction="self"` value under `direction=in` filter.
- `limit` default on both endpoints (cap ≤100 is pinned; default
  isn't).

---

## Verified-as-true plan claims (credit where due)

- **Every substantive R5 amendment is present in plan.md** — DISABLED
  lifecycle (155–158), the "ok"-only-after-attempt provider_status
  rule (330–334), per-request `Enabled()` re-evaluation (134–135),
  derived `stale` (326–329), the full per-kind `matched_signals`
  schema + per-kind applicability + LEGAL-first + kind-distinguishes-
  SANCTIONED (223–236), head-behind no-call (146–149), single-emission
  `direction="self"` (369–372, §6:475), `IndexingFreshTTL`/
  `IndexingStaleAfter` as named keys (126–133), and all 17 nits —
  including the corrected max-count premise (300–306), cumulative
  skip counters (322–324), and the empty-stream-complete-to-fetch_to
  watermark clause (151–153). `window_covered` is gone.
- **Codebase claims all check out:** `provider.ErrDisabled` exists
  (provider.go:164); `Hooks.OnResult(source,result)` carries no
  latency — `Result.Latency` does (client.go:46,58) so the plan's
  "call-site or OnDuration" hedge is accurate; `resilience.WithClock`
  exists for test 22 (client.go:71); `LookupWallet`'s three buckets +
  `Programs []ProgramRef` + `Observation{NetworkCode,ListedAddress}`
  confirmed (service.go:49–50,144–157,165–188) — the new
  `matched_signals` fields are servable end-to-end;
  `ListAllWalletLabels`'s join set confirmed (labels_read.sql:6–16)
  and `LatestLabelSnapshotPublished` exists (labels_read.sql:20) with
  `data_source_snapshots.version` (000005:87–94) — `labels_snapshot_
  version` is servable; `risk_categories` seeds `('SANCTIONED','LEGAL')`
  (000005:17) so LEGAL-first is load-bearing, not cosmetic;
  `scoring.Kind*` provenance vocabulary confirmed (model.go:13–36);
  enrichment's `statusFor` is uppercase (service.go:506–528) so the
  documented lowercase divergence is real; `singleflight.Group` is
  the established convention (enrichment/service.go:39);
  `wallets.sql`'s `DO UPDATE updated_at=wallets.updated_at RETURNING`
  convention exists verbatim (wallets.sql:5); `source_type='api'` is
  the on-demand convention (000006:28–33); `NormalizeEVM` exists
  (address.go:28); `ProviderRequestsTotal{source,result}` +
  `ProviderErrorsTotal{source,reason}` exist (metrics.go:36–37,92–97);
  `httpserver.Deps` is the injection vehicle (server.go:38–60);
  openapi.yaml:14's stale "Transaction tracing remains intentionally
  absent" confirmed verbatim; `tracing: enum:[NOT_IMPLEMENTED]` sits
  at ~openapi.yaml:1316 with all four emission sites enumerated
  correctly (service.go:297, mapper.go:323, model.go:171,
  service_test.go:354) and exactly **5** screening-*.json captures
  contain `NOT_IMPLEMENTED` — the phase-04 touch-list is accurate.
- **Arithmetic re-verified:** warm-window advance `100,001 − 100 =
  99,901` (span = `window_cap` ≤ NodeReal's 100k `toBlock−fromBlock`
  cap); cold-start `[0..100,000]` legal; head-lag edge handled;
  tail-consumes-reach reasoning sound.
- **`direction="self"` UNION mechanics work:** normalizing the
  self-row's direction to `'self'` in both scans makes the UNION
  twins byte-identical → dedupe kills both the double-emission and
  the keyset page-boundary hole.
- **`.{k}` identity is context-free and cross-adapter stable** under
  the restated premise; copy-index keys survive partial-group
  rescans (`.1` re-keys identically).

## Assessment

The architecture has converged — providers, indexing windows, identity
derivation, BFS assembly, and coverage honesty are internally
consistent and grounded in verified code. What remains is a
concentrated residue of **wire-contract slips**: one unservable field
(R6-m1), one broken coverage identity at a documented parameter value
(R6-m2), one unpinned item-identity rule (R6-m3), one ambiguous
lifecycle clock + a `provider_status` undefined-value corner that can
re-open the exact false-ok R5-M1b closed (R6-m4), one promised note
with no carrier (R6-m5), and phase files that would re-introduce an
already-fixed schema defect if implemented literally (R6-m6). All six
amendments are one-line to one-paragraph fixes; none changes the
design. After they land, the plan is implementation-ready.

CHALLENGED
