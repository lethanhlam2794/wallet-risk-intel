# Devin Critic — Round 7: Transaction Tracing plan (post-R6-amendment)

scope: `plans/20261104-0056-transaction-tracing/` (plan.md + 4 phase
files) verified against `rebuttal-round-6.md` (6 amendment groups +
13 nits), `docs/tailieu.md` (§7.13, §9.9–9.10, §11.4–11.5), and repo
internals (`migrations/000001/000005`, `queries/{wallets,labels_read,
sanctions,ingestion_runs}.sql`, `internal/{sanctions,screening,scoring,
enrichment,label,provider,wallet}`, `internal/provider/resilience`,
`internal/platform/metrics`, `internal/httpserver`, `internal/config`,
`api/openapi.yaml`, `docs/feature-maps/assets/*.json`).

Carried caveat (R2–R6, still true today): the plan directory remains
git-**untracked** (`git status` → `??`). All "amended" claims verified
textually only.

---

## Part 1 — R6 amendment verification

All 7 amendment groups **landed**. One stale parenthetical survives
inside R6-m2's own fix site (→ R7-n4); everything else is clean.

| # | Verdict |
|---|---|
| R6-m1 `stale` on `/transactions` envelope (derived: `indexed_at` vs `IndexingFreshTTL`; NULL ⇒ true) | **Landed** — `stale` in §3.5 field list (plan.md:392); NULL⇒true (139); derived-not-stored (359–362); phase-02:9 `stale(derived)`. |
| R6-m2 `max_hops=1` ⇒ examined/emitted/failed=0, remaining=total, sum preserved | **Landed** — explicit enumeration plan.md:287–290; phase-03:9 "remaining=total, other nodes_*=0". A stale "all nodes_*=0" parenthetical survives at :268 → **R7-n4**. |
| R6-m3 one item per counterparty — dedup across depths, hops=min, min-hop paths, dedup before category selection | **Landed** — §3.3.9 verbatim (plan.md:302–309); phase-03:6. |
| R6-m4 `indexed_at`=last terminal write (one upsert w/ status+error) = cooldown+stale clock; gate={fresh,partial}∧≤TTL; indexing never satisfies; not_indexed→fetch; provider_status total on terminal states | **Landed** — plan.md:127–141 covers every clause incl. "`provider_status` … the rule is total"; phase-01:6, phase-02:6–8. |
| R6-m5 `head_block` nullable on both envelopes; head-behind self-describing | **Landed** — coverage (279) + `/transactions` envelope (392); self-describing clause `head_block < indexed_until_block` (156–157); phase-02:9, phase-03:9. |
| R6-m6 phase sync (matched_signals schema + LEGAL + kind rule; labels_snapshot_version + root.stale; direction="self"; head<fetch_from; join set named; headers drop round metadata) | **Landed** — phase-03:7 (full per-kind schema, `{id,name}`/`{code,name}`/`ds.code` shapes, LEGAL-first, kind-distinguishes-SANCTIONED); phase-03:9 (`sanctions_snapshot_version+labels_snapshot_version`, `root{index_status,stale,…}`); phase-02:9 ("self-row once w/ direction='self'"); phase-02:6 ("head < fetch_from ⇒ NO upstream call … head_block on wire"); phase-01:7 names join set. All 4 headers: `status: pending`, no round metadata. |
| R6-n7 13 nits | **Landed** — all 13 verified: item ordering "min contributing position index" (296–297, phase-03:9); `node_indexed_until` attempted-only (277–279); self-row always `"self"` (404–405); `max_edges` order named (209–211); matched_signals shapes (239–241); `direction` filters before derivation (290–291); one picked edge per hop position (257–258); `truncated`=null untruncated (299); partial+flag same upsert / cleared on unbounded success (356–359); `block_timestamp` mined-rows-only (316–317); `internal/transaction` + `internal/tracing` named (§3.3, §4); tests 21b extended (474–478); `limit` default 50 both endpoints (298, phase-02:9). |

Summary: 7/7 landed. The plan's coverage arithmetic, state lifecycle,
and wire fields are now internally consistent **except** the residue
and new findings below.

---

## Part 2 — New findings

## [Minor] R7-m1 — Item ordering has no final tiebreak: "deterministic order" is unsatisfiable, and `limit`/`response_cap` cuts become nondeterministic

§3.3.8 pins item order as "hops asc, then **min contributing position
index**" (plan.md:296–297; §3.3.4's "then expansion order" is the same
rule loosely restated; phase-03:9 restates the pinned form). Two
distinct counterparties discovered through the **same** hop-2 position
share `(hops=2, min_position_index=P)` — the declared key does not
separate them. Hop-1 items are safe (each hop-1 counterparty *is* a
position, so indices are unique); the tie is hop-2-items-per-position.

Consequences, none covered by any other clause:

- Emission order between tied items is implementation-accidental →
  violates the plan's own "deterministic order" claim.
- `limit`/`response_cap` "cut at item boundary" drops a
  nondeterministic subset of the tied run.
- Test 22 ("emitted items … identical given identical resolution
  sets") is unsatisfiable without an implementation-imposed — i.e.
  unspecified — ordering.

**Amendment:** append a final tiebreak to the order key — e.g.
"(hops asc, then min contributing position index, then `counterparty`
normalized_address asc)". One clause; any total-order choice works —
the requirement is that the key be total.

## [Minor] R7-m2 — `/transactions` `direction` **filter** enum contradicts itself across contract surfaces: plan says `{in,out}`, phase files + OpenAPI task say `{in,out,self}`

§3.5 pins the filter vocabulary: "`direction` values `in|out` relative
to the queried wallet" (plan.md:397), with `"self"` pinned as an
emitted-only "third wire value … not a filter echo" (404–406, §6).
phase-02:9 lists the filter as "direction in|out|self" and phase-04:8
puts "direction in|out|self" into the OpenAPI task. Read literally:

- `direction=self` validates against the phase-authored OpenAPI enum
  but the plan-authoritative service contract doesn't define what it
  returns (self-rows only? 400? silently ignored?).
- If one enum is shared across endpoints, `direction=self` on
  `/exposures` — where self-edges are excluded by construction
  (plan.md:189–190) — is an accepted-but-vacuous value, or the
  endpoints need separate enums (unstated either way).

This is exactly the R6-m1 class of defect: three surfaces, two
vocabularies, one consumer-visible parameter.

**Amendment:** pick and pin per-endpoint filter enums in §3.5 —
recommended: `/transactions` `{in,out,self}` (self ⇒ self-rows only —
it is the natural reading of phase-02/04 and costs nothing: self-rows
already satisfy both in/out predicates); `/exposures` `{in,out}` —
then align phase-02:9 and phase-04:8 to match.

## [Nit] R7-n3 — `coverage.head_block` attribution on `/exposures` unstated

"Populated when head fetched; null on no-fetch serves" (279, 392) is
unambiguous for `/transactions` (one wallet, one EnsureIndexed). On
`/exposures`, hop-2 node EnsureIndexed calls **also** fetch heads —
whose lands in the singular envelope field: the root's observed head,
the last-observed node head, a max? §3.2's self-describing use case
(`head_block < indexed_until_block`, 156–157) implies the root's head.
Pin it: "`coverage.head_block` = the root wallet's observed head this
request — null when the root made no head call; per-node heads remain
internal (per-node honesty is `node_indexed_until`'s job)."

## [Nit] R7-n4 — Stale `max_hops=1` parenthetical at plan.md:268

"`counterparties_total` (…; `max_hops=1` ⇒ all `nodes_*` = 0)" —
literal `nodes_*` includes `nodes_remaining`, contradicting the
explicit "nodes_remaining=counterparties_total" at :288–289 and
phase-03:9's "remaining=total, other nodes_*=0". The explicit rule
wins, but the surviving phrasing is how the next literal-implementer
bug gets filed. Reword to "no expansion ⇒ `nodes_examined`=
`positions_emitted`=`nodes_failed`=0" (same fix applied twice already
— finish the job).

## [Nit] R7-n5 — Matched sentinel counterparties: items or not?

Sentinels `0x0`/`0x…dead` are "excluded from expansion but still list
on /transactions" (196–198) — the exclusion clause addresses
*positions* only. A labeled sentinel (burn addresses do appear in
public label sets) is still a hop-1 counterparty with edges — literal
reading emits a hop-1 item, while `counterparties_total` deliberately
excludes it. Consistent arithmetic either way (items ≠ positions),
but "excluded" is doing two jobs ambiguously. One clause: e.g.
"sentinels may emit items when matched — they are counterparties, just
not expansion positions" (or the opposite — pin one).

## [Nit] R7-n6 — plan.md header critic line is stale

plan.md:6–7 lists R1–R3 only; R4–R6 happened (reports exist, ~110
findings applied). Cosmetic — update or drop; it decays every round
(the R6-m6 fix correctly removed the equivalent from phase headers).

---

## Verified-as-true plan claims (credit where due)

- **Every R6 amendment present and coherent** — see Part 1. The
  lifecycle rules compose into a total state machine: gate covers
  {fresh,partial}; `indexing` joins-or-retries; `not_indexed` fetches;
  `disabled` re-evaluates `Enabled()`; `failed` cools down —
  `provider_status` totality holds because served envelopes can only
  ever carry terminal states (fetch path always ends terminal;
  singleflight join returns the holder's terminal result).
- **All referenced infrastructure exists:** `provider.ErrDisabled`
  (provider.go:164); `resilience.NewClient`/`WithClock`/`OnResult`
  (client.go:46–47,71,91 — `OnResult` correctly identified as
  latency-free, "call-site or OnDuration" is accurate);
  `LookupWallet`'s three buckets + `Observation{NetworkCode,
  ListedAddress,SourceCode}` (model.go:64–72) → listing-kind signals
  servable incl. `network=null` on unresolved; `SanctionEntry` +
  `ProgramRef{Code,Name}` (model.go:31–36,48–56) → `entity={id,name}`,
  `programs[]={code,name}` servable; `LatestLabelSnapshotPublished` +
  `data_source_snapshots.version` → `labels_snapshot_version`
  servable; `LatestSnapshotPublished` (sanctions.sql:95) →
  `sanctions_snapshot_version`; `wallet_labels` carries `entity_id`,
  `verification_status`, `category_id` and `labels_read.sql:6–16`
  already selects `rc.code+rc.category_type, e.id+e.name` — the R6-m6
  join set matches an existing query shape; `risk_categories` seeds
  `('SANCTIONED','LEGAL')` (000005:17) making LEGAL-first load-bearing;
  `scoring.Kind*` vocabulary exists; `INVALID_CURSOR` exists
  (errors.go:20); `singleflight.Group` convention (enrichment
  service.go:39); `wallets.sql` `DO UPDATE updated_at… RETURNING`
  verbatim (wallets.sql:5); `EnrichmentNegativeTTL`=60s,
  `EnrichmentDeadline`=25s (config.go:53,55,81,83 — plan's "≠"
  disclaimers accurate); `NormalizeEVM` (address.go:28);
  `provider_requests_total`/`provider_errors_total` exist,
  no provider duration family (metrics.go:93,97) — "added, not
  reused" is accurate; `httpserver.Deps` optional-service wiring is
  the established injection pattern.
- **Spec/contract touch-points exact:** `transactions` per §7.13
  (is_error declared deviation legit); §9.9 filters
  direction/asset/from_timestamp/to_timestamp/cursor/limit all
  mapped; §9.10 fields all mapped; §11.4 bounds all present;
  §11.5 edge fields + transfer_index deviation declared;
  `tracing: enum:[NOT_IMPLEMENTED]` at openapi.yaml:1316, stale
  description at :14, four Go emission sites + `service_test.go:354`
  + exactly 5 screening-*.json captures — the phase-04 touch-list is
  accurate to the line.
- **Arithmetic re-verified:** advance `100,001−100=99,901`/window;
  ~1300-call BSC cold convergence consistent with ~65M head;
  10M CU÷250=40k calls/month quota note accurate; nodereal 100k
  `toBlock−fromBlock` cap vs `window_cap` span bound consistent;
  coverage `examined+remaining=total` holds under the explicit
  max_hops=1 rule; max-per-stream `.{k}` counting is cross-adapter
  consistent (nodereal self-transfer `max(1,1)=1`; chainscan
  kind-stream degeneracy) and page-boundary self-healing via tail
  refetch is sound.
- **`path_verified=false`, INTERNAL exclusion, deep-reorg residual,
  BSC deadline-bound posture** — all declared deviations, none hidden.

## Assessment

The R6 amendments landed cleanly in plan.md and all four phase files —
the ~110-finding accumulation has converged: providers, window
arithmetic, content-derived identity, contiguous-prefix BFS, coverage
honesty, and the index-state machine are all implementable as written.
Two genuine contract slips remain: one undefined mechanism (item-order
key is not total → nondeterministic emission/cuts, breaking the plan's
own determinism claim and test 22) and one contradicted contract
(`/transactions` `direction` filter vocabulary differs between plan.md
and the phase files feeding OpenAPI). Both are one-clause amendments;
the four nits are one-line pins or residue cleanup. None touches the
design. After they land, the plan is implementation-ready.

CHALLENGED
