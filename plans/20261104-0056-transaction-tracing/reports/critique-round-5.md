# Devin Critic — Round 5: Transaction Tracing plan (post-R4-amendment)

scope: `plans/20261104-0056-transaction-tracing/` (plan.md + 4 phase
files) verified against `rebuttal-round-4.md` (12 amendment groups),
`docs/tailieu.md` (§7.13, §9.9–9.10, §11.4–11.5), and repo internals
(`migrations/000001/000005/000006`, `queries/{wallets,sanctions,
labels_read}.sql`, `internal/sanctions/service.go`,
`internal/screening/mapper.go`, `internal/scoring/`,
`internal/provider/{provider.go,resilience/client.go}`,
`internal/platform/metrics/metrics.go`,
`internal/config/config.go`, `internal/httpserver/server.go`,
`internal/enrichment/service.go`, `internal/wallet/`).

Carried caveat (R2–R4): the plan directory and the Phase-5 code remain
git-**untracked** (`git status` → `??`, confirmed again today). All
"amended" claims verified textually only.

---

## Part 1 — R4 amendment verification

All 12 amendment groups **textually present** in plan.md and phase
files. Five landings produce or leave a defect handled in Part 2.

| # | Verdict |
|---|---|
| R4-M1 window `fetch_from=max(0,w−reorg_tail+1)`, `fetch_to=min(head,fetch_from+window_cap)`, span-capped, ≈99,901 advance, inclusive `[a..b]` | **Landed** — plan.md:133–139, phase-02:6. Arithmetic now correct (verified below); head-behind-watermark edge unstated → **R5-m3**. |
| R4-M2 `history_truncated`+`rows_skipped_*` persisted; `stale` derived; `rows_indexed` cumulative; `provider_status` persisted outcome | **Landed** — plan.md:290–301, phase-01:6, phase-02:6/8/9/10. Three consistency gaps on serve paths → **R5-M1** (+ nits R5-n6). |
| R4-M3 `.{k}` max over per-stream counts; assembled-batch counting; both-streams-truncated residual; test 13 + fixtures extended | **Landed** — plan.md:271–285, test 13:378–383, phase-01:9/13. Correctness holds for kind-streams (verified below); stated premise needs generalizing → **R5-n6**. |
| R4-m4 test 22 restated timing-free (pool/nodes truncation, pre-expired ctx, WithClock; deadline asserted only as truncated=deadline) | **Landed** — plan.md:403–407. |
| R4-m5 `limit` last in precedence; pipeline assemble→filter→order→cut | **Landed** — plan.md:195–202, phase-03:9. |
| R4-m6 intra-tier `MALICIOUS>SERVICE>ENTITY>OTHER` code asc; `matched_signals` schema; classification-only emits items | **Landed** — plan.md:204–217, phase-03:7. Schema diverges from the suggested field set and from real columns; LEGAL tier unranked → **R5-m2**. |
| R4-m7 bounds govern hop-2 positions only; resolved=index+scan+match; every counterparty a position | **Landed** — plan.md:173–183, phase-03:6. |
| R4-m8 refresh every EnsureIndexed; `direction in|out` default both; `max_hops` 1=hop-1-only, 0|>2→400; W→W matches in AND out | **Landed** — plan.md:245–250, 335–336, phase-02:9, phase-03:9. Unfiltered self-row emission + keyset interaction unstated → **R5-m4**. |
| R4-m9 sentinels excluded from expansion, still listed; ascending-index earliest-history bias declared | **Landed** — plan.md:178–179, 192–194, phase-03:6. |
| R4-m10 tests 18b/18c (provider-switch reset; refresh-vs-cooldown; stale-INDEXING); self-transfer folded into 13 | **Landed** — plan.md:389–395, 378–383. Remaining filter-behavior gaps → R5-n6. |
| R4-n11 `counterparties_total`; `positions_emitted`; `node_indexed_until` null; snapshot null; `{streamA,streamB}`; `max_edges` request-global; `provider_status="ok"` | **Landed** — plan.md:231–242, 190–191, phase-01:8, phase-03:8–9. `counterparties_total` domain unstated → R5-n6. |
| R4-n12 `rawContract.value`/`value` hex; tokentx isError doc; `value>0` scope exclusion; tx_hash lowercase + canonical hash inputs; `source_type`+`address_type`; UNION listing; NEW duration metric | **Landed** — phase-01:8/10/11, plan.md:264–270, 227–228, 334–336, 34–37, phase-04:9. `source_type='provider'` breaks the established `'api'` convention; duration hook has no latency param → R5-n6. |

Summary: 12/12 applied; five landings produce or leave defects
(R4-M1→R5-m3, R4-M2→R5-M1, R4-M3→nit, R4-m6→R5-m2, R4-m8→R5-m4).

---

## Part 2 — New findings

## [Major] R5-M1 — DISABLED lifecycle is self-contradictory, and the `provider_status` rule emits a false `"ok"` on the default BSC serve path

Two contradictions in the R4-M2 landing:

**(a) `status=FAILED` vs `status=disabled`.** §3.2 (plan.md:147–149)
still reads:

> Provider DISABLED/typed failure ⇒ `index_state.status=FAILED` +
> ErrKind recorded

— while §3.4's own enum carries `disabled` as a distinct value
(plan.md:292–293) and phase-02:8 says `disabled: status=disabled +
reason; zero calls`. Literally implemented, §3.2 records a config
state as a transient failure: wrong wire `index_status` and wrong
lifecycle semantics (a disabled provider enters the 60 s
FAILED-cooldown it never earned).

**(b) `provider_status` "empty ⇒ ok" is a false-ok for any
no-attempt state.** The pinned rule (plan.md:300–301):

> `provider_status` on no-fetch responses = persisted last-attempt
> outcome (`error` ⇒ ErrKind, empty ⇒ `"ok"`)

— a DISABLED provider made no attempt; unless `error` is populated,
the wire reports `provider_status="ok"` **alongside**
`index_status=disabled`. This is not an edge case: the default
deployment posture is `NODEREAL_API_KEY` empty ⇒ DISABLED
(plan.md:87) and chainscan-bsc paid-gated — every BSC `/transactions`
response on a default install reports `"ok"` for a provider that
never ran. That is the "provider failure → clean result" shape §2
and AGENTS §5 exist to kill — now laundered through a default rule
instead of a code path.

**(c) disabled → enabled transition unstated.** `error=disabled` is
not persisted, nothing says whether `status=disabled` re-evaluates
`Enabled()` per request or pins the wallet dark until manual DB
intervention. Keys are env-static so a restart re-registers the
adapter either way — but the freshness gate must say disabled rows
re-check `Enabled()` (zero calls, zero cost) and resume indexing
when a key appears.

**(d) `stale` × `disabled`/`failed` stacking.** The derived-stale fix
kept two facts for `partial`; the same collision exists for
`disabled` (TTL-lapsed + disabled) and `failed` — a single
`index_status` enum can't carry both unless `stale` is a separate
derived field.

**Amendment:**

1. §3.2 → "Provider DISABLED ⇒ `status=disabled`, `error='disabled'`
   (`provider.ErrDisabled` exists — provider.go:164), zero upstream
   calls; typed failure ⇒ `status=failed` + ErrKind." Fixes (a).
2. Pin `provider_status` on every no-fetch response: `error`
   non-empty ⇒ its ErrKind (`disabled` included); else `"ok"` —
   i.e., `"ok"` is derivable **only** after a succeeded attempt
   (`status ∈ {fresh, partial}` + empty error). Fixes (b).
3. Freshness gate: `status=disabled` re-evaluates `Enabled()` each
   request — enabled now ⇒ normal indexing path; still disabled ⇒
   serve persisted facts + `provider_status="disabled"`. Fixes (c).
4. Wire `stale` as a **derived boolean** beside `index_status`
   (persisted enum unchanged) — `stale=true` composes over any
   status, not a seventh enum value. Fixes (d).

## [Minor] R5-m2 — `matched_signals` schema diverges from the R4-m6 suggestion and can't carry real columns: dropped `label`, singular `program?`, missing `network`/`listed_address`, unranked `LEGAL` tier

The landed schema (plan.md:213–215, phase-03:7):

```
{kind: sanctions_direct|label|listing, category, entity?,
 program?, label_source, verification_status?}
```

vs the amendment's suggested `{kind, category, entity?, program?,
label?, label_source, verification_status?}` and vs actual columns:

- **`label` silently dropped.** `wallet_labels.label` (000005:47)
  holds the specific attribution under the category — entity_id
  doesn't cover it (nullable, 000005:44). A SCAM-category signal
  with no `label` can't say *which* label matched.
- **`program?` is singular; the source is a list.**
  `SanctionEntry.Programs []ProgramRef` (service.go:144–157) — an
  OFAC entity commonly carries several programs. Needs `programs[]`.
- **`listing` kind can't carry its own content.** `Observation`
  gives `NetworkCode` + `ListedAddress` (service.go:165–188) — the
  entire point of a cross-chain/unresolved listing is *which*
  network and *what literal listed form* (the provenance-preserved
  representation AGENTS §6 requires). Neither field exists in the
  schema → a `cross_chain_listing` signal is unverifiable on the
  wire.
- **`LEGAL` category_type unranked.** `risk_categories` seeds
  `('SANCTIONED','LEGAL')` (000005:17), and upstream mappers emit
  SANCTIONED categories (goplus:19, publicaml:15, chainabuse:25).
  The intra-tier order `{MALICIOUS_ACTIVITY > SERVICE_TYPE >
  ENTITY_TYPE > OTHER}` omits `LEGAL` → a label-kind SANCTIONED
  signal's rank is undefined, and "SANCTIONED (same-network direct
  bucket only)" doesn't say what a label-kind SANCTIONED winner
  emits. The codebase already answers the wire question:
  `Category="SANCTIONED"` with `Kind≠SanctionsDirect` is normal —
  scoring emits `KindCrossChainListing/KindProviderObservation`
  under Category SANCTIONED (mapper.go:118–119, 132, 144); `kind`
  is the provenance carrier. Apply the same rule.
- **`label_source`** maps to `data_sources.code` — works for all
  kinds (`ofac_sdn` for sanctions kinds) but the name is
  label-flavored; rename `source` or document per-kind.
- **Bulk match query under-specified** (phase-01:7): only the WHERE
  is pinned; the SELECT needs the `ListAllWalletLabels` join set
  (`risk_categories` code+category_type, `data_sources` code, LEFT
  `entities` name — labels_read.sql:6–18) to populate these fields.

**Amendment:** pin signal entries
`{kind: sanctions_direct|label|listing, category, label?, entity?,
programs[]?, source, verification_status?, network?,
listed_address?}` with per-kind applicability documented; add
`LEGAL` first in the intra-tier order (a sanctions claim outranks
other label classifications) and state: `category=SANCTIONED` on an
item requires `matched_signals[].kind` to distinguish
`sanctions_direct` from a label-kind sanctions claim (same
convention as `scoring.Kind`).

## [Minor] R5-m3 — Inverted window on head-behind-watermark is unstated: provider node-lag or a >tail reorg emits an out-of-contract call → FAILED loop

`fetch_to = min(head, fetch_from + window_cap)` (plan.md:133–136) is
correct while `head ≥ fetch_from`. When it isn't — an RPC fleet
returning a lagging head, or a reorg deeper than `reorg_tail` —
`fetch_to < fetch_from` yields an **inverted** `[a..b]` range. The
literal reading emits `fromBlock > toBlock` upstream → 4xx →
`status=failed` → cooldown → repeat — the R4-M1 failure shape in
miniature, and node-lag is a *routine* occurrence, not a corner
case.

**Amendment:** one line — `head < fetch_from ⇒ no upstream call;
serve persisted facts; coverage reports the head-behind-watermark
condition` (e.g., no status transition; optionally a coverage note).
Cheap insurance against a normal-world trigger.

## [Minor] R5-m4 — Unfiltered `/transactions` self-row: the UNION emits it twice, and the `(block_timestamp, id)` keyset silently drops the twin at a page boundary

§3.5 (plan.md:334–336): "Unfiltered listing = UNION of the
from-index and to-index scans with keyset over the union; a W→W
self-row matches `direction=in` and `direction=out` (it is both)."

- The filter semantic is pinned; the **unfiltered emission** is not.
  The from-scan yields the row with `direction='out'`, the to-scan
  with `direction='in'` — tuples differ, so UNION/UNION ALL both
  emit **two rows for one real transfer**.
- Keyset over `(block_timestamp DESC, id)`: the twins share
  `(ts, id)` — whichever falls below the cursor is excluded by the
  `(ts,id) < cursor` predicate → **every page boundary landing
  between the twins silently loses one**. A pagination hole on the
  wire, exactly the "never silently dropped" class.

**Amendment (recommended):** unfiltered listing emits the self-row
once with `direction="self"` — pin the third wire value in OpenAPI
(`in|out|self`; `direction=in`/`=out` filters still both match it).
Alternative if double-emission is wanted: extend the cursor key to
`(block_timestamp, id, direction)` with direction order pinned —
strictly worse (two items per transfer). Pick one and declare it
(§6 if `self` is a new wire value).

## [Minor] R5-m5 — Unpinned knobs: index freshness TTL source and the stale-`indexing` threshold are unnamed

- "Fresh index-state (≤10 min…)" (plan.md:126–127): **no config key
  exists** — `EnrichmentCacheTTL=15min`, `EnrichmentNegativeTTL=60s`,
  `EnrichmentRefreshCooldown=60s` (config.go:80–83); none is 10 min.
  Phase-01's config task (line 12) lists registry/keys/pages/
  reorg_tail/pool but no TTL. Pin `IndexingFreshTTL` (default 10m)
  or name the reused constant and fix the number.
- "`indexing` rows older than **the pipeline deadline**"
  (plan.md:129–131): unnamed. It must not reuse
  `EnrichmentDeadline=25s` — a BSC index attempt legitimately
  outlives it. Pin `IndexingStaleAfter` (sized vs the bounded
  EnsureIndexed duration, e.g., deadline + pool-bound headroom) and
  add it to the phase-01 config task.

## [Nit] R5-n6 — Coverage vocabulary, schema, and doc nits

- `window_covered` (plan.md:148–149) is dead vocabulary — absent
  from the §3.5 envelope; drop it or map it to a real field.
- `status=partial` ⟺ `history_truncated=true` is the same fact
  stored twice — pin the relation (or state the distinction, if
  `partial` means something else).
- `rows_skipped_*` are "last-fetch facts" while sibling
  `rows_indexed` is cumulative — asymmetric; pin semantics
  (recommend cumulative, or document last-fetch intent).
- `counterparties_total` domain: pin "expansion positions after
  sentinel exclusion" so `examined + remaining = total` holds; for
  `max_hops=1` there are no positions → all `nodes_*=0`.
- `sanctions_snapshot_version` is fetched once per request, but
  `LookupWallet` resolves its own version internally
  (service.go:52) — a mid-request publish yields coverage claiming
  V while some node matched under V+1. Plumb the version through
  the match or declare the bounded drift.
- No `labels_snapshot_version` beside `sanctions_snapshot_version`
  — `LatestLabelSnapshotPublished` exists (labels_read.sql:20);
  label-evidence freshness is unverifiable on the wire.
- `data_sources.source_type='provider'` (phase-01:6) coins a third
  value — the convention is `'api'` for on-demand providers
  (000006:28–33) and `'snapshot'` for file sources. Use `'api'`.
- `provider_request_duration_seconds` — `Hooks.OnResult(source,
  result)` carries no latency (client.go:46; `Result.Latency`
  exists): observe at the call site or add an `OnDuration` hook.
- `api/openapi.yaml:14` top-level description still says
  "Transaction tracing remains intentionally absent" — stale.
- R4-M3's justification "each stream lists each real copy once" is
  nodereal-shaped. The correct cross-adapter premise: **no stream
  lists a real copy more than once, and ≥1 stream covered the
  block** — for chainscan each `(tx_hash,h)` is confined to exactly
  one kind-stream (txlist holds `n:` only, tokentx holds `t:` only;
  a W→W self-tx appears once in txlist), so `max` degenerates to
  the single-stream count. Restate the premise — the rule is right,
  the reason as written is false for kind-streams.
- "min over all streams' contiguous coverage": pin that an
  empty/finished stream is complete-**to `fetch_to`**, not
  coverage 0 — a literal reading stalls the watermark forever on
  token-only wallets (txlist empty every window).
- `error` column: pin cleared-on-success.
- `/transactions` `limit` bound unpinned (spec §9.9 has `limit`;
  `/exposures` pins ≤100) — and no response cap declared for
  `/transactions` at all (cursor-bounded is a defensible answer;
  say it).
- `provider_status` casing: lowercase `"ok"|ErrKind` vs
  enrichment's uppercase `statusFor` vocabulary (service.go:506) —
  a deliberate divergence worth one doc line.
- `max_paths_per_pair=3` "lexicographically-first node sequences" —
  pin the ordering domain (normalized address strings).
- Edge `asset` value for native rows — `"native"` vs null (align
  with the `asset=native` filter vocabulary).
- `transfer_index` absent from wire edge fields — dedup key
  `(tx_hash, transfer_index)` is internal-only; identical-content
  copies are indistinguishable to consumers. Add it to the edge
  schema (declare in §6) or declare the ambiguity.
- Test-list additions warranted by R4-era mechanics still
  uncovered: `direction`/`category` filters on /exposures,
  `max_hops=0|1|3` edges, sentinel-expansion exclusion, unfiltered
  self-row emission (with R5-m4's fix).

---

## Prompt's targeted questions — direct answers

**Window arithmetic, once more:** now correct. `fetch_to −
fetch_from = window_cap ≤ 100,000` satisfies the upstream span cap
`toBlock − fromBlock ≤ 100,000`; warm advance = `cap − tail + 1 =
99,901` matches the declared figure; cold start `w=0` gives
`[0..100,000]` — legal. The remaining gap is `head < fetch_from`
(R5-m3).

**Persisted-vs-derived coverage on every serve path:** one real
defect and one ambiguity — `provider_status` "empty ⇒ ok" is
false-ok for disabled/no-attempt responses (R5-M1b), and
`status=FAILED` vs `disabled` is self-contradictory (R5-M1a).
Everything else on the no-fetch path (history_truncated,
rows_skipped_*, indexed_until, index_scope, stale overlay) is
servable from the persisted columns.

**Occurrence counting vs stream shapes:** the max-per-stream rule
yields correct k on both adapters — on chainscan each `(tx_hash,h)`
is confined to exactly one kind-stream so `max` degenerates to the
single-stream count; on nodereal a W→W self-transfer counts
`max(1,1)=1` and a W→X transfer `max(1,0)=1`. Cross-adapter
identity also holds (same tx → same k under either stream shape →
identical `.{k}` on provider-switch reindex). Only the *stated
premise* needs generalizing (R5-n6) — the rule itself is
stream-shape-agnostic.

**`matched_signals` vs actual columns:** `category`↔`rc.code`,
`entity?`↔`entities.name`/`se.primary_name`,
`verification_status?`↔`wl.verification_status`,
`label_source`↔`ds.code` all map. `label` (dropped from the
suggestion), `programs` (list, not singular), `network` +
`listed_address` (listing kind's actual content) don't (R5-m2). The
intra-tier order omits `LEGAL` — which `risk_categories` seeds and
three upstream mappers emit.

**§3 self-contradictions after the fourth pass:** three —
DISABLED⇒`FAILED` vs the `disabled` enum/phase file (R5-M1a);
`window_covered` dead vocabulary vs the §3.5 envelope; and the
"each stream lists each real copy once" premise vs kind-streams.
Plus the unstated-but-real self-row emission question (R5-m4).

## Verified-as-true plan claims (credit where due)

- All 12 R4 amendment groups textually landed; plan.md remains
  self-contained.
- Window span arithmetic is now correct: span = `window_cap` ≤
  100,000 upstream cap; BSC warm advance 99,901; tail consumes
  forward reach as declared.
- `LookupWallet` three-bucket signature confirmed
  (service.go:49–50); `Programs` is a list (service.go:144–157);
  `Observation{NetworkCode, ListedAddress}` confirmed
  (service.go:165–188); `LatestSnapshotPublished` +
  `snap.Version` confirmed (sanctions.sql:95, service.go:213).
- `provider.ErrDisabled` exists (provider.go:164) — the
  `provider_status="disabled"` fix needs no new vocabulary.
- `wallet_labels` columns confirmed (`label`, `verification_status`
  CHECK set, `entity_id`, `source_id`, `source_url`,
  `upstream_category` — 000005:39–59); `risk_categories` seeds
  `LEGAL`/`SANCTIONED` confirmed (000005:10,17); scoring's
  Kind/Category provenance convention confirmed
  (mapper.go:117–144).
- `provider_observations` is append-only audit-shaped
  (payload JSONB, no normalized category columns — 000006) —
  wallet_labels-only matching is the *right* scope, not a gap.
- `source_type` convention confirmed: `'api'` for on-demand
  providers (000006:31), `'snapshot'` for file sources.
- No provider duration metric exists (metrics.go:92–107) —
  "added, not reused" claim exact; `Hooks.OnResult` signature
  confirmed (client.go:46).
- `EnrichmentNegativeTTL=60s` (config.go:81) — cooldown claim
  exact; `INVALID_CURSOR` exists (errors.go:20); `Deps` wiring
  pattern confirmed (server.go:38–60); `NormalizeEVM` +
  `address_type='evm'` convention confirmed
  (wallet/service.go:25, ofac_publish.sql.go:58).
- `NOT_IMPLEMENTED` touch-points all re-verified: service.go:297,
  mapper.go:323, model.go:171, service_test.go:354,
  openapi.yaml:1316, exactly 5 `screening-*.json` captures.
- Content-hash identity, prefix-emission mechanics, bound
  precedence incl. `limit`, refresh scope, sentinel rule,
  persisted coverage columns — all correctly stated.

## Verdict

Every R4 amendment landed and the two headline fixes check out:
the window arithmetic is now sound, and the persisted coverage
columns make the no-fetch paths servable. But the R4-M2 landing
has a live wire-value defect — `provider_status` defaults to
`"ok"` precisely on the disabled path that is the default BSC
posture — plus a status-enum contradiction (R5-M1); the
`matched_signals` schema lost real columns and leaves the LEGAL
tier unranked (R5-m2); two small unstated edges can still emit an
inverted upstream call or drop a page-boundary row (R5-m3, R5-m4);
and two config knobs are unnamed (R5-m5). All fixes are small and
specifiable — no redesign needed.

**CHALLENGED** — 0 blockers, 1 major (R5-M1), 4 minors
(R5-m2…R5-m5), 1 nit group (R5-n6; ~17 items).
