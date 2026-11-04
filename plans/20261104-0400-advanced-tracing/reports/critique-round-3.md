# Devin Critic — Round 3: Advanced Tracing plan challenge (v3)

Scope: `plans/20261104-0400-advanced-tracing/` (plan.md post-rebuttal
content + 2 phase files + `reports/rebuttal-round-2.md`), verified
against `internal/tracing/{service,model}.go`,
`internal/transaction/service.go`, `queries/transactions.sql`,
`internal/db/transactions.sql.go`, `internal/scoring/{policy,engine}.go`,
`internal/httpserver/transactions.go`, `internal/screening/
{service,mapper,handler}.go`, `internal/sanctions/service.go`,
`internal/config/config.go`, `internal/network/resolver.go`,
`cmd/api/main.go`, `migrations/000005_labels.up.sql`,
`api/openapi.yaml`, `tests/transactions_api_test.go`,
`internal/tracing/service_test.go`, and
`docs/guidebook-transaction-tracing.md`.

Note: plan.md's title still reads "plan v2" but the body carries all
round-2 fixes (≤ d−2 emitted-prefix visited, two-domain minPos, lazy
merge, `ANY($2)`, tests 22–28) — it is the v3 content under a stale
banner.

## Round-2 findings — resolution status

- **B1′ RESOLVED.** §2.4 now scopes the counterparty clause to
  "emitted-prefix node's scanned edges at scan levels ≤ d−2". The
  producing scan (level d−1) is strictly outside the clause; the
  asymmetry that vacated every frontier is gone. Verified against the
  indexing: frontier_d candidates are counterparties of emitted
  frontier_{d−1} scans (level d−1). Sentinel hop-1 cps are covered by
  the level-1 (root-scan) counterparty clause; non-sentinel hop-1 cps
  are covered by the queued-position clause — Phase-6's `cps`+`root`
  dedup is reproduced for the default direction case (see M1″ for the
  direction-set case).
- **M1′ RESOLVED.** `max_nodes` is scoped to SCHEDULED frontiers
  (d ≤ max_hops); frontier_2 built-but-unscheduled at max_hops=1 never
  fires it — preserves the `maxHops == 2` guard. Test 22 pins it.
- **M2′ RESOLVED.** Two minPos domains pinned: hop-1 = cpOrder rank
  (sentinels included, `agg.rank` — unchanged); hop-d≥2 = min first-via
  index in frontier_2. Domains never mix (sort compares minPos only
  within equal `hops`). Test 24 pins the sentinel-interleaved case.
- **M3′ PARTIALLY RESOLVED.** Output semantics fixed: no parent-map
  pre-truncation, "first MaxPathsPair sequences in tuple order over the
  FULL sequence set". But the described mechanism still cannot produce
  that order (new M3″), and §5 carries the stale v2 bound (m3″).
- **M4′ RESOLVED.** Evaluator consumes evidence rows (`category_type`
  + `confidence` projected; `wl.confidence` added to both queries in
  phase-02); SANCTIONED code-exception precedence is explicit; `vf×cf`
  mirrors `weighted()`. See m2″ for the residual rounding wording.
- **M5′ RESOLVED.** Counterparty clause scoped to emitted-prefix scans;
  the beyond-prefix glimpse is a declared residual recorded via
  `positions_emitted`; test 28 pins emit-at-hop-3.
- **M6′ RESOLVED.** `w.network_id = ANY($2::bigint[])` over resolver
  foreign IDs — resolver has a real runtime role; deactivated/absent
  networks can never match (resolver is built from
  `ListActiveNetworks`). Empty-set semantics verified safe (§Q5).
- **M7′ MOSTLY RESOLVED.** Frontier-index attempted reading is pinned
  (0 = none; ceiling = max_hops); one gloss remains ambiguous (m5″).
- Minors m1′–m12′: all addressed in text or phase files. Test gaps:
  tests 22–28 cover each flagged case, though no phase file owns them
  (m4″).

## Fresh hunt — the seven prompt questions

1. **§2.4 formula.** Correct as written under the literal reading for
   the default-direction case: candidates (level d−1) escape the
   ≤ d−2 clause; a node queued as a position can never re-enter a
   deeper frontier, so min-depth holds; every cycle edge targets an
   earlier-depth node, which is necessarily in `visited` (position
   clause or earlier counterparty clause) — no cycle slips through.
   Within the emitted graph a node cannot emit later than its min; the
   only later-than-data emit is the beyond-prefix glimpse, which is the
   declared residual (test 28). HOWEVER the clause swallows
   direction-filtered root counterparties — see M1″.
2. **minPos two-domain rule.** Evaluable: sequence tuples retain via₂
   ∈ frontier_2 through the parents chain; via₂'s frontier_2 index is
   stored on its position; domains are only compared within equal hops
   — no leakage. "Exactly today's viaPos" pins post-cut semantics
   (today's min is over truncated vias, service.go:398-411).
3. **Lazy best-first enumeration.** The pinned OUTPUT is sound and
   test-23-discriminated, but the described mechanism cannot emit tuple
   order over `parents` (backward adjacency) — it needs forward
   adjacency + cp-reachability. M3″.
4. **Scoring over evidence rows.** Evaluable: `signals()` receives the
   rows (`labelIdx[cp]`, `lookups[cp]`); the evaluator consumes the
   same bundle at item-build. Foreign rows carry `category_type`,
   `verification_status`, `confidence`, `n.code` — `vf`/`cf`/`decay`
   apply identically; parity is declared (§3). Per-term rounding +
   literal Σ + min(100) is evaluable given `*int` output forces integer
   terms — but "half-up in hundredths" is imprecisely worded (m2″).
5. **`ANY($2)` empty set.** Safe both ways: `[]int64{}` → `'{}'` →
   `= ANY('{}')` false for all → zero rows; `nil` → NULL → `= ANY(NULL)`
   NULL → zero rows. No false positives. (Single-network test stacks —
   `network.NewResolver([]network.Network{net})`, service_test.go:101 —
   exercise exactly this.)
6. **Coverage consistency.** Verified: visited guarantees single
   frontier membership, so attempts (⊆ positions) can never exceed the
   Σ-frontier total; `examined + remaining = total` holds; the ≥0 floor
   is defensive only. The "attempted at d but counted in an earlier
   frontier" scenario is impossible by construction.
7. **Tests 22–28.** Each is discriminating for its target (22→M1′,
   23→tuple-order, 24→minPos domain, 25→foreign parity, 26→
   max_depth_reached, 27→per-term rounding, 28→beyond-prefix residual).
   Gaps remain — Part 3.

## Part 1 — Majors

### M1″. The terminal candidate set's status is unpinned — and under the uniform-frontier reading, §2.4 breaks "bit-identical at max_hops≤2" whenever `direction` is set

Where do hop-{max_hops} item candidates live? Two readings, each
contradicting something:

- **Scheduled-only reading:** frontiers exist only for d ≤ max_hops
  (§2.7 "positions queued for expansion"; counterparties_total = "today's
  hop-1 count" at max_hops≤2). Then the terminal candidate set (e.g.
  hop-3 counterparties at max_hops=3) is NOT a frontier — and §2.4,
  which only applies "before building frontier_d", never dedups it.
  A hop-3 candidate that is already a frontier_3 position would emit
  items at BOTH hop 2 and hop 3 unless the implementer invents a
  second, unpinned dedup rule for the terminal level.
- **Uniform reading:** frontier_{max_hops+1} IS built (§2.1's
  construction covers it — it is the emitted-prefix scan product) but
  is merely unscheduled. Then counterparties_total must not count it
  (unpinned — "all built frontiers" says otherwise), and §2.4's
  counterparty clause now applies at every level — including the
  hop-2 candidate set at max_hops=2.

The concrete divergence under the uniform reading — `direction=out`,
`max_hops=2`, root edges {root→A, X→root}:

- Phase 6 (service.go:129-134, 309): the direction filter drops X from
  `cps` BEFORE cpAgg creation; hop-2 dedup checks `cps[cp2]`, so X —
  never a recorded hop-1 cp — is a legitimate hop-2 candidate via A's
  scan and emits an item if matched.
- v3 §2.4: visited includes "every addr discovered as a counterparty of
  an emitted-prefix node's scanned edges at scan levels ≤ d−2". X→root
  IS a scanned root edge (level 1); the filter applies only "before
  position derivation" (§2.8). So X ∈ visited → excluded → no hop-2
  item. Different `items`, different `counterparties_total`,
  different `nodes_remaining` — the pinned "all Phase-6 wire fields
  keep meaning at max_hops ≤2 (bit-identical behavior)" fails whenever
  `direction` is non-default.

Under the scheduled-only reading the same case produces a semantic
cliff: X emits at hop-2 when max_hops=2 but is visited-suppressed at
hop-2 when max_hops=3 (frontier_3 is then a real build). Either pin
the terminal set's dedup AND scope the level-1 counterparty clause to
the post-direction-filter candidate domain (preserving Phase-6), or
declare the divergence explicitly — §2.8's residual covers deep
EDGES, not filtered-out counterparties.

### M2″. §2.2's edge-exclusion drops edges to sentinels — deep sentinel items get no paths, contradicting §2.1 and test 7

plan.md:50-52: "an edge is dropped when its far endpoint is: the root,
the node itself (self-edge), a **sentinel**, or already in the
**visited set**". Read literally, a depth-3 sentinel counterparty gets
zero recorded (parent, edge) contributions — the `parents` map IS the
path-evidence store ("every (parent, edge) contribution is recorded
for path assembly"). The item then either emits with empty `paths` or
is skipped entirely (the `len(ps)==0 → continue` rule,
service.go:420-422, generalizes) — contradicting §2.1 ("sentinel addrs
… may still emit items if they carry signals") and test 7 ("still
emits if signaled"). The hop-1 model the plan is generalizing keeps
two books: `cps` tracks every counterparty's edges (items + paths),
`positions` is the non-sentinel subset (service.go:135-165). The plan
needs the same split at depth ≥3 — sentinel/visited far endpoints are
excluded from POSITION seeding while remaining item/path candidates.
As written, the fix requires the implementer to read past the formula.

### M3″. "Sorted parent lists + best-first merge" cannot emit tuple order — and the declared bound omits the reachability term

§2.9: "parent lists per position sorted lexicographically; sequences
emitted lazily via best-first merge in tuple order". Tuple order is
lexicographic over (via₂,…,via_d) — via₂ MOST significant — while
`parents` gives backward adjacency: expansion must choose via_d (the
least significant element) first. Best-first over suffix-partials emits
the wrong order. Constructive: hop-3 item, cp's parents {p,q} with
p<q; p's parents {z}, q's parents {a}, with a<p<q<z. Correct tuples:
(a,q) < (z,p). Suffix-first best-first pops (p) (via_d: p<q), expands
to (z,p), then pops (z,p) — (z,p) emitted FIRST, wrong. The plan pins
the correct output ("first 3 sequences in tuple order over the FULL
sequence set", test 23) but describes a mechanism that produces a
different one.

The honest lazy enumeration is lexicographic DFS in tuple order over
FORWARD adjacency — i.e., children lists derived during frontier
build plus a cp-reachability mask (reverse BFS over the parents DAG);
neither is mentioned. The bound O(MaxPathsPair×d×P) per item holds
only under that pruning — an unpruned forward DFS pays dead-end
exploration up to O(Σ frontier parents edges) per item, so the
declared bound needs the reachability term to be honest. Related: §5
still declares "Path assembly O(MaxPathsPair² × MaxNodes)" — the exact
formula M3′ invalidated (wrong axis: emitted items are bounded by
edges_examined ≤5000, not MaxNodes; wrong exponent vs §2.9's
O(MaxPathsPair×d×P)). Two normative sections disagree.

## Part 2 — Minors

- **m1″ `*_by_hop` map key domain unpinned.** `frontier_by_hop`,
  `nodes_examined_by_hop`, `frontier_emitted_by_hop`,
  `nodes_remaining_by_hop` are written `{d:n}` — frontier index
  (2..max_hops) or item hop (1..)? `max_depth_reached` is explicitly
  frontier-index; the by-hop fields are not pinned, and whether the
  terminal set appears is tied to M1″.
- **m2″ "PER-TERM half-up in hundredths" vs `*int`.** `weighted()`
  rounds per-finding to an INTEGER (engine.go:172). If terms round to
  hundredths (e.g. 70×0.0625=4.375→4.38), the literal Σ is
  non-integer and cannot populate `exposure_score *int` without a
  second unstated rounding. Test 27 expects an int — pin "per-term
  integer half-up" (decay as sixteenths: 16,8,4,2,1).
- **m3″ §5 stale bound** contradicts §2.9 (folded into M3″'s fix —
  restate the honest global bound).
- **m4″ Tests 22–28 own no phase.** phase-01 lists "1–9, 12–19, 21";
  phase-02 lists "6, 7, 10, 11, 20". The seven new pinned tests are
  assigned nowhere.
- **m5″ `max_depth_reached` gloss ambiguity.** "frontier built but
  never attempted or attempted-with-empty-prefix → not counted": if
  "attempted-with-empty-prefix" means "≥1 position attempted (examined)
  but emitted prefix 0" (deadline mid-first-attempt), it contradicts
  the primary "deepest ATTEMPTED" rule; if it means "scheduled with
  zero positions attempted", it's consistent — the sentence supports
  both.
- **m6″ `MaxPagesFor(frontierIdx)`**: idx 0→root fallback, 2→HopN,
  ≥3→Hop3 — idx 1 is undefined (unused in practice; frontier indices
  start at 2) and phase-01 names the param `depth` vs plan's
  `frontierIdx`.
- **m7″ Resolver foreign-ID set** — exclusion of the request network is
  implicit ("foreign"); pin `$2 = resolver IDs ∖ {request net ID}` or a
  `w.network_id <> $1` belt, and pin the query's parameter numbering
  (`$1` addrs vs `$2` net IDs) since it inverts BulkMatchLabels'
  convention.
- **m8″ Test 10's `listed_address` assertion is weak.** m6′ pinned
  foreign `wallets.address` (original representation); the test asserts
  `listed_address`=normalized addr — only discriminating if the fixture
  seeds a non-normalized original (checksum case), unstated.
- **m9″ Plan title still "plan v2"**; phase-02 attributes the
  `Request.MaxHops` doc comment to `service.go::New` — it lives in
  model.go:46.

## Part 3 — Test gaps (new)

1. **Direction-filtered cp at depth ≥2** — `direction=out`, in-only
   hop-1 cp X reachable via an emitted deep edge: emits or not? The
   discriminating test for M1″'s resolution whichever way it's pinned.
2. **Deep sentinel item path evidence** — test 7 asserts emission, not
   that `paths[]` carries the contributing edges (M2″).
3. **Per-hop coverage VALUES** — test 9 asserts presence/consistency of
   `counterparties_total`/`positions_emitted`; the `{d:n}` maps' keys
   and per-depth values are unpinned by test.
4. **Single-network resolver → zero foreign rows, no error** — guards
   the `ANY('{}')` path (Q5) and the "deactivated never matches" claim.

## Part 4 — Positive confirmations

The B1′ repin is the correct off-by-one and verified against both
indexing conventions; min-depth and cycle exclusion follow from the
formula as written for the default case. minPos's two-domain rule is
evaluable (the parents chain reaches via₂'s frontier_2 index) and the
"same meaning as today" claim now holds by construction. Scoring over
evidence rows is fully grounded — the row bundle (`labelIdx`,
`lookups`, foreign rows) is in scope at item-build and `signals()`
consumes the same inputs; SANCTIONED precedence is explicit;
`vf`/`cf`/`decay` factor structure mirrors `weighted()`; the null-vs-0
rule is safe (minimum non-zero term is 20×0.0625=1.25→1, so a zero sum
⟺ all-zero-weight ⟺ null — `0` truly never appears). `ANY($2)` is safe
on empty/NULL. `examined+remaining=total` is invariant under single
frontier membership. Screening is confirmed decoupled —
`coverage.tracing` is `provider_reported_only` and exposures come from
provider reports (screening/mapper.go:322-353), so `options.max_hops`
staying 0–2 is structurally safe, not just pinned. `Concurrency`,
provider budget, edge CAS, and `nodeUntil` uniqueness all generalize
cleanly across depth loops.

## Assessment

The v3 revision lands the round-2 intent almost everywhere: the
visited formula is now correct for the default case, minPos is
properly two-domain, the scoring formula is evaluable end-to-end, and
the resolver now earns its injection. But three mechanism-level holes
remain at the candidate-set boundary: the terminal level's frontier
status is undefined AND the counterparty clause silently extends
Phase-6's dedup domain to direction-filtered cps (M1″ — the
bit-identical pin fails under `direction`); sentinel edges are dropped
where the pinned behavior requires retaining them for item evidence
(M2″); and the enumeration mechanism is described in a form that emits
the wrong order, with a stale contradictory bound surviving in §5
(M3″). All three are recoverable — the pinned tests would force the
right answers — but the plan's bar is "implementable without
guessing", and §2.2/§2.4/§2.9 as written each contradict a pinned
behavior.

**CHALLENGED** — 0 blockers, 3 majors, 9 minors, 4 test gaps.
