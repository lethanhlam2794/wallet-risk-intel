# Devin Critic — Round 2: Advanced Tracing plan challenge (v2)

Scope: `plans/20261104-0400-advanced-tracing/` (plan.md v2 + 2 phase
files + `reports/rebuttal-round-1.md`), verified against
`internal/tracing/{service,model}.go`,
`internal/transaction/service.go`, `internal/scoring/{policy,engine}.go`,
`internal/httpserver/transactions.go`, `internal/screening/handler.go`,
`internal/sanctions/service.go`, `internal/config/config.go`,
`internal/network/resolver.go`, `cmd/api/main.go`,
`queries/transactions.sql`, `migrations/000001_init.up.sql`,
`migrations/000005_labels.up.sql`, `api/openapi.yaml`,
`tests/transactions_api_test.go`, `internal/tracing/service_test.go`,
Phase-6 plan `plans/20261104-0056-transaction-tracing/plan.md`, and
`docs/guidebook-transaction-tracing.md`.

Bar per prompt: implementation-ready, self-contained; flag anything
unimplementable or self-contradictory as written, plus NEW defects the
v2 rewrite introduced.

## Round-1 claims — resolution status

B1 resolved in intent (Reading A + `position{addr,parents}` +
multi-parent `firstEdge` union) but the v2 mechanics carry a new
blocker (B1′) and a new ordering defect (M2′). B2 partially resolved —
re-keyed on `(kind, code/tier)` and the SERVICE_TYPE contradiction is
gone, but the formula still cannot be evaluated as specified (M4′).
B3 resolved. M1 partially resolved — fields defined, but the
`max_nodes` guard regresses at `max_hops=1` (M1′) and
`max_depth_reached`'s gloss contradicts itself (M7′). M2, M3 resolved
in intent — M3's fix overshot into B1′. M4 mostly resolved — new
resolver/query mismatch (M6′). M5 partially resolved — temporal +
tuple order pinned; the enumeration bound is wrong and conflicts with
the truncation rule (M3′). M6 mostly resolved — remaining test gaps
listed in Part 4. m1–m8 largely resolved; residual minors in Part 3.

## Part 1 — Blocker

### B1′. The visited-set formula excludes every frontier candidate — all frontiers ≥3 are empty as written

plan.md:64-66 (§2.4):

> `visited` = root ∪ every addr queued as a position in frontiers ≤ d
> ∪ every addr discovered as a counterparty of any scanned edge at
> depths < d (sentinel or not).

Frontier_d's candidates are *by construction* the counterparties of the
immediately-preceding scan level. Anchoring via §2.7
(`counterparties_total` = "today's hop-1 count" at max_hops≤2, i.e.
frontier_2 = hop-1 positions): frontier_3 candidates are counterparties
of frontier_2 nodes' scanned edges, discovered at depth 2. "depths < d"
for d=3 covers depths 1 **and 2** — the depth-2 scan level that
produces the candidates. Every candidate is therefore "already in
visited" and "never enters a deeper frontier": frontier_d ≡ ∅ for all
d ≥ 3. The formula directly contradicts §2.1 ("Clean intermediaries
seed deeper frontiers — that is what deeper hops exist to reach
through").

The indexing is robust — no convention rescues it. If "depth" numbers
edge-scan levels (e₁ from root, e₂ from frontier_2 nodes, …), the
producing level for frontier_d is d−1 < d. If "depth" numbers node
graph-depth, candidates sit at depth d−1 < d. The producing level is
always inside "depths < d". (The sibling clause "queued as a position
in frontiers ≤ d" is correct — frontier_d does not exist at build
time, so ≤ d effectively means ≤ d−1; the asymmetry ≤ vs < is exactly
backwards on the counterparty side.)

Required fix: the counterparty clause must cover scans strictly
shallower than the candidate-producing level — "at depths < d−1"
(equivalently, visited is snapshotted before the e_{d−1} scan that
produces frontier_d's candidates). Intent is recoverable from tests 4/5
and "dedup keeps min depth", but the pinned formula as written makes
the entire feature unreachable — the same class of defect as
round-1's B1.

## Part 2 — Majors

### M1′. `max_nodes` now fires at `max_hops=1` — breaks the "bit-identical at max_hops≤2" pin

plan.md:113-115: "`max_nodes` fires when ANY built frontier_d exceeds
MaxNodes_d". Frontier_2 is built at `max_hops=1` — positions are
derived from rootEdges regardless of expansion (service.go:160-166;
Phase-6 plan §3.3.8: "positions exist but expansion is skipped").
Today's guard is literally `len(positions) > s.cfg.MaxNodes &&
maxHops == 2` (service.go:512) — a 60-counterparty hop-1 set at
`max_hops=1` produces no `max_nodes` reason today and `max_nodes`
under v2. The rule must be scoped to scheduled frontiers (`d ≤
max_hops`, i.e. frontiers actually attempted), not merely built ones.
plan.md:261 test 21 ("Phase 6 suite unchanged and green — max_hops≤2
bit-identical") and the new §2.7 rule cannot both hold.

### M2′. `Item.minPos` cannot express hop-1 ordering — sentinel items have no contributing position; non-sentinel indices change domain

plan.md:78-81 (§2.5): "`Item.minPos` = the contributing position's
index in its own depth's frontier order (same meaning as today:
hop-2 item's viaPos in the hop-1 position list)". Today uses two
different index domains per hop level:

- Hop-1 items: `minPos = agg.rank` — the counterparty's rank in
  `cpOrder`, an ordering over ALL hop-1 counterparties **including
  sentinels** (service.go:156-158, 374).
- Hop-2 items: `minPos = seq.viaPos` — the via's index in
  `positions`, which **excludes sentinels** (service.go:160-165,
  319, 401-411). The domains never mix because the sort key
  compares minPos only within equal `hops` (service.go:451-460).

Under the v2 rule: (a) a hop-1 sentinel item has no contributing
position — sentinels never enter a frontier — so its minPos is
undefined; (b) a non-sentinel hop-1 item's "position index" differs
from its cpOrder rank whenever a sentinel precedes it in cpOrder, so
the item order changes vs Phase 6 (e.g. cpOrder [0x…dead, A, B]:
today minPos 0/1/2 → order dead,A,B; under position-index minPos the
sentinel is undefined and A,B shift to 0/1 → different wire order).
The parenthetical claims "same meaning as today" but the stated rule
reproduces only the hop-2 domain. Either pin hop-1 items to keep the
cpOrder-rank semantics (sentinels ranked, positions and non-positions
in one ordering) or declare the reorder — "bit-identical" is not
achievable from the current text.

### M3′. Path enumeration: the declared bound is inconsistent with the pinned truncation rule, and parent-map truncation can drop true top-3 sequences

plan.md:139-141 (§2.9): "before building sequences, each depth's
parent map is truncated to the MaxPathsPair lexicographically-first
parent addrs; enumeration emits at most MaxPathsPair sequences —
assembly work is O(MaxPathsPair² × MaxNodes) worst case, declared."

- **Bound arithmetic.** Truncating each node's parent map to
  MaxPathsPair parents bounds sequences by MaxPathsPair^{d−1}, not
  MaxPathsPair²: at d=5, MaxPathsPair=3 → up to 81 sequences
  enumerated (then cut to 3), not 9. And the item axis isn't
  MaxNodes — deepest-hop items are bounded by discovered
  counterparties (≤ edges_examined = 5000), not by the expansion cap.
  The honest form is O(MaxPathsPair^{d−1} × d × emitted_cps) unless
  enumeration is lazy-in-tuple-order (in which case the truncation
  rule is pointless and needs restating).
- **Truncation vs tuple-order conflict.** "MaxPathsPair keeps the
  first 3 sequences in tuple order" is not preserved by truncating
  parent maps first at d ≥ 4. Constructively: for sequence
  (v₂,v₃,v₄,cp), let v₄'s parent map drop p (4th parent) keeping
  k₁<k₂<k₃<p. A sequence (z, p, v₄, cp) where z is a tiny parent
  address outranks every (anc(k_j), k_j, v₄, cp) whose ancestors sort
  higher — the true first tuple can route through a dropped parent.
  At d≤3 this cannot happen (v₂ has empty prefix, so kept siblings
  always outrank a dropped one — which is also why today's
  via-truncation is safe). The plan never states whether "tuple
  order" is evaluated over the full or truncated sequence set — two
  implementable readings emit different paths.

### M4′. `exposure-v1` still has unevaluable terms: `category_type` isn't on the signal, SANCTIONED's LEGAL tier collides with the zero-tier clause, and the dropped confidence factor isn't even queryable

- **`base(label, code→tier)` over `matched_signals` is unevaluable
  as specified.** The formula is "Σ_signals base(kind, code) × …"
  (plan.md:174), but `MatchedSignal` carries only the category
  *code* (`model.go:65-75` — no category_type field). The tier lives
  on `db.BulkMatchLabelsRow.CategoryType` (transactions.sql:139),
  available at item-build time but not on the wire object the
  formula names. An implementer must either thread label rows
  alongside signals or add a field — unpinned either way.
- **SANCTIONED precedence vs its own tier is unstated.** `SANCTIONED`
  is seeded `category_type='LEGAL'` (000005_labels.up.sql:17). The
  base table assigns "code SANCTIONED label-kind → 85" AND
  "LEGAL → 0" — a SANCTIONED-code label matches both. Code-exception
  precedence is the obvious intent (Phase-5 `baseWeightsV1["SANCTIONED"]=85`,
  policy.go:40) but the rule as ordered never says code beats tier;
  the two readings produce 85×conf vs 0 on the second-highest weight.
- **`conf(sig)` drops Phase-5's confidence factor silently — and the
  data isn't even projected.** Phase-5 label weight is
  `base × verification × confidence` (engine.go:159-172 —
  `weighted()` multiplies two factors). v2's `conf` maps only
  verification statuses (verified/reported/unverified/disputed →
  1.0/0.7/0.5/0.0); the `wl.confidence` column (000005:48) is absent
  from `BulkMatchLabels` (transactions.sql:137-148) — so the Phase-5
  second factor cannot be applied without a query change no phase
  file lists. "Phase-5 semantics" is claimed while half the factor
  structure is unreachable. Either declare the single-factor
  deviation or add `wl.confidence` to the select list.

### M5′. Visited's counterparty clause scopes to "any scanned edge" — nodes glimpsed only through beyond-prefix scans become permanently invisible

Frontier_d is built from **emitted-prefix** nodes' edges only (§2.1),
but visited (§2.4) swallows "any scanned edge" — and beyond-prefix
positions ARE attempted and scanned (service.go:284-287 emits only
the contiguous prefix; resolved-beyond-prefix `nodeResult.edges` are
populated, Phase-6 plan §3.3.3 "computed but excluded"). A
counterparty discovered *solely* via a beyond-prefix node's scan: (a)
emits no item at depth d (prefix rule), (b) is in `visited`, so edge
exclusion at every later frontier build drops edges to it — it can
never become a position *or* emit an item at depth d+1 even through
a fully emitted path. A legitimately reachable matched node vanishes
from all output with no coverage field that records it. Scope the
counterparty clause to emitted-prefix scans (the frontier-build
domain), or declare the blackout and cover it in coverage fields.

### M6′. `BulkMatchLabelsCrossNet` does not implement "the resolver's network set", and the resolver injection is dead weight under the pinned query

plan.md:154-156, 164-166: query "filtered `w.network_id <> $1` over
the resolver's network set" + `tracing.New(q, tx, sanctions,
resolver, cfg)`. Two contradictions:

- `w.network_id <> $1` matches every other network_id present in
  `wallets` — including networks absent from the resolver
  (deactivated rows, never-registered ids). "Over the resolver's
  network set" requires `w.network_id = ANY(resolver IDs \ {$1})` or
  a post-filter on `n.code`; as speced, labels from non-resolver
  networks emit `network:<their code>` while the plan claims resolver
  scoping.
- Under the single `<>` query the resolver has no runtime use —
  `n.code` already returns every foreign net's code, and there is no
  per-network iteration for the resolver to drive. Under
  resolver-driven per-network iteration the `<>` query is pointless.
  One of the two pinned artifacts is dead weight; pin which.

### M7′. `max_depth_reached` is pinned with a self-contradictory gloss

plan.md:106-109: "`max_depth_reached` = deepest depth with ≥1
attempted position (0 when max_hops=1; **1 at hop-1-only runs** — pin:
it is the deepest *attempted expansion* depth)." A "hop-1-only run" is
most naturally `max_hops=1`, which the first clause values at 0 and
the second at 1 — mutually exclusive. Under the frontier-index reading
("attempted expansion depth"), attempting frontier_2 yields 2, not 1.
Under the graph-depth reading (frontier_2 members are depth-1 nodes),
clause 2 coheres but the field's ceiling at max_hops=5 is 4 — a deeper
quirk the text never states. Pick the indexing (frontier level vs node
depth) and fix the example; as written the only normative sentences
disagree.

## Part 3 — Minors

- **m1′ Rounding placement unpinned.** "computes in hundredths, rounds
  half-up to int" (plan.md:195-197) doesn't say per-term or post-sum.
  Divergent case: two 59.5-valued terms → post-sum 119 vs per-term
  60+60=120. `weighted()` rounds per finding (engine.go:163-172) —
  mirroring is likely intended but never stated.
- **m2′ `MaxPagesFor(depth int)` parameter semantics unpinned.**
  "depths 3–5" (plan.md:88-92) is coherent only if `depth` = frontier
  index / expansion hop (frontier_3 → HOP3 tier). Passing graph depth
  (frontier_3 members are depth-2 nodes) shifts every tier by one.
  Root's mapping (Hop0 via `opts.MaxPages<=0` fallback,
  transaction/service.go:234-236) unstated.
- **m3′ Env name ambiguity.** "TXINDEX_MAX_PAGES_HOP2 (existing
  MaxPagesHopN, renamed concept — env keeps working)" — the existing
  env var is `TXINDEX_MAX_PAGES_HOPN` (config.go:154). Is the env
  renamed with fallback? Both set → precedence? Unstated.
- **m4′ Config shape still free.** `tracing.Config.MaxNodes` is a
  single int (service.go:25); the per-depth representation (map /
  slice / D2..D5 fields) and `defaults()` semantics remain unstated;
  `Concurrency` sharing across depth loops unmentioned.
- **m5′** "every path descends strictly by depth" (plan.md:67) — paths
  *increase* in depth (root→via→…→cp); "strictly increasing" or
  "descends toward the item".
- **m6′ `listed_address` = the normalized address is degenerate** —
  it always equals `counterparty`. If the intent is the foreign
  listing's stored representation, `wallets.address` (original
  representation preserved per AGENTS §6) is the informative value;
  if redundancy is intended, say so.
- **m7′ Foreign labels score at full local weight — undeclared.**
  `kind:"label"` foreign signals flow through `base()` identically
  (SCAM → 70×conf×decay). Cross-chain observations must be "clearly
  distinguished" (AGENTS §5) — `network` distinguishes shape, but
  whether foreign evidence should score at parity with local is a
  semantic choice the plan makes silently.
- **m8′ Guidebook goes stale, unlisted.** `docs/guidebook-transaction-
  tracing.md:31` documents "max_hops=1|2 (0 or >2 → 400)"; plan §4
  defers the Σ-additive deviation to "documented in guidebook" — yet
  no phase file touches `docs/`.
- **m9′ `exposure_score` JSON tag unpinned** — `*int` with
  `json:"exposure_score"` emits `null`; `omitempty` drops the key —
  different wire contract for the null case the plan pins.
- **m10′ Vacuous screening bullet.** §2.7 "`POST /screenings` coverage
  usage of `counterparties_total`: unchanged" — screenings never
  consume tracing coverage (`coverage.tracing` is
  `provider_reported_only`, screening/model.go:169-171); the claim is
  true but references a coupling that doesn't exist.
- **m11′ Test 21 literally impossible.** "Phase 6 suite unchanged and
  green" — `TestValidateRequest` asserts 3 is rejected
  (service_test.go:285-294) and the API test asserts `max_hops=3`
  → 400 (transactions_api_test.go:184-188). Both MUST be edited (3
  becomes valid); reword to "suite updated for the new domain, all
  else green". `Request.MaxHops` doc comment "validated {1,2}"
  (model.go:46) likewise.
- **m12′** Per-depth `BulkMatchLabels` call structure (one union call
  vs per-depth) and foreign-label merge into `signals()`'s
  consider()-skip mechanics — implementable but unlisted; note that
  `signals()` must distinguish foreign rows to skip `consider()`.

## Part 4 — Test gaps (pinned behavior with no discriminating test)

1. **No test for the M1′ regression**: `max_hops=1` with
   |frontier_2| > MaxNodes_2 must NOT emit `truncated=max_nodes` —
   the exact assertion that catches the new guard bug.
2. **No test pinning which sequences survive `MaxPathsPair`** — test
   17 (determinism) asserts identical output, which a deterministic-
   but-wrong path choice also satisfies. Needs a ≥4-sequence fixture
   asserting the emitted tuples (and the truncation-vs-tuple-order
   semantics of M3′ once pinned).
3. **No test for hop-1 ordering under interleaved sentinels** — the
   M2′ domain-mix is invisible unless a test asserts item order with
   a sentinel adjacent to positions.
4. **Foreign-label score contribution untested** — test 12 covers
   local label + `kind:"listing"` (20) but not a foreign `kind:"label"`
   contributing 70×conf (m7′).
5. **`max_depth_reached` boundary cases** — empty-prefix deeper
   frontier, frontier built but never attempted; test 9 asserts the
   field exists, not these values (M7′).
6. **Rounding half-boundary fixture** — pin a sum of terms hitting a
   .5 boundary so per-term vs post-sum (m1′) is decided by test, not
   taste.
7. **Beyond-prefix glimpse case (M5′)** — a node reachable only via a
   beyond-prefix scan at depth 2 and via an emitted path at depth 3:
   assert it still emits at hop 3 (or the declared alternative).

## Part 5 — Positive confirmations

Reading A is the correct generalization and is now explicit; sentinel
exclusion at every frontier + retained item emission is the right
shape; `direction` hop-1-only is honestly declared as a residual;
`exposure_policy_version` isolation from `risk_score` is clean;
`listed_address` is at least pinned; min-depth dedup *intent* is
correct; `TRACING_MAX_NODES` legacy fallback is pinned; the
duplicate-`kind:"listing"` risk is gone (foreign labels stay
`kind:"label"` + `network`); `POST /screenings` `options.max_hops`
frozen at 0–2 with a test; per-hop coverage fields + floored
`nodes_remaining` preserve `examined + remaining = total` at every
depth.

## Assessment

The v2 rewrite resolves the round-1 *vocabulary* problems but
introduces a new off-by-one in the visited formula that makes every
deeper frontier vacuous (B1′), silently regresses two pinned coverage
behaviors at max_hops≤2 (M1′, M2′), misstates its own enumeration
bound (M3′), and still cannot evaluate the scoring formula over the
data it names (M4′). The fixes are mostly recoverable from the pinned
tests, which is why these are not all blockers — but the plan's bar
is "implementable without guessing", and three separate spots
(scoring precedence, minPos domain, visited bound) currently require
the implementer to guess against the text.

**CHALLENGED** — 1 blocker, 7 majors, 12 minors, 7 test gaps.
