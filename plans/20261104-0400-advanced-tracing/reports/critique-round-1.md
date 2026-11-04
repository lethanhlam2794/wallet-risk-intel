# Devin Critic — Round 1: Advanced Tracing plan challenge

Scope: `plans/20261104-0400-advanced-tracing/` (plan.md v1 + 2 phase
files), verified against `internal/tracing/service.go`,
`internal/tracing/model.go`, `internal/transaction/service.go`,
`internal/transaction/model.go`, `internal/provider/provider.go`,
`internal/sanctions/{service,model}.go`, `internal/scoring/policy.go`,
`internal/httpserver/transactions.go`, `internal/config/config.go`,
`cmd/api/main.go`, `queries/{transactions,sanctions}.sql`,
`migrations/000005_labels.up.sql`, `api/openapi.yaml`,
`docs/tailieu.md` (§9.10, §10, §11, §19, §21), and the Phase-6 plan
`plans/20261104-0056-transaction-tracing/plan.md`.

Bar per prompt: **unimplementable or self-contradictory as written**;
implementation-ready and self-contained required.

## Part 1 — Blockers

### B1. Frontier construction has two contradictory readings; the multi-depth position/parent structure is never specified

plan.md:36-39 — "each depth has its own ordered position list (built
from the previous depth's **emitted-prefix edges**, same (earliest ts,
tx_hash, normalized_address) ordering key)".

"Emitted-prefix edges" admits two implementable readings that produce
different graphs:

- **Reading A (correct):** all scanned edges (`r.edges`,
  `service.go:182`) of emitted-prefix nodes — consistent with the
  Phase-6 pin that *every* counterparty is an expansion position
  regardless of match status ("clean intermediaries are exactly what
  hop-2 exists to reach through", Phase-6 plan §3.3.2;
  `service.go:160-165` makes positions from all non-sentinel `cpOrder`
  entries, not only matched ones).
- **Reading B (catastrophic):** edges emitted inside items' `paths[]` —
  only `pickEdge`-selected edges (`service.go:408-418`, one per hop
  position). Frontier₃ would then contain only counterparties already
  evidenced at depth 2, and hops ≥3 would be reachable almost nowhere
  except via matched intermediaries.

Compounding this, three load-bearing mechanics are absent:

1. **Multi-parent ordering key.** At depth d a node is reachable via
   multiple emitted-prefix parents. The ordering key "(earliest ts,
   tx_hash, normalized_address)" must be defined over *which* edge set —
   hop-1 computes `firstEdge` over edges grouped per counterparty
   (`service.go:135-158, 617-631`). The plan must pin: group all
   incoming edges per candidate across all emitted-prefix parents, take
   `firstEdge` over the group. Otherwise the position order is
   implementation-dependent and determinism test 14 (plan.md:136-137)
   is unverifiable.
2. **Per-position parentage.** Path assembly needs the parent chain:
   today `hop2[cp2][via]→e2s` is a 2-level map (`service.go:298-324`);
   a depth-3 path root→A→B→C needs e1 from `cps[A].edges`, e2 from A's
   scanned edges where `counterpartyOf=B`, e3 from B's scanned edges
   where `counterpartyOf=C`. No structure for parent→edges per frontier
   position is described anywhere in the plan or phase files.
3. **`minPos` for ordering at depth ≥3.** `Item.minPos` is "min
   contributing position index" (`service.go:401-411`) — the via's index
   in *its own* frontier? The tuple of position indices along the
   chain? Unstated; the item total order (`service.go:451-460`) needs a
   defined key to satisfy plan.md test 14.

### B2. `signal_weight` table is unevaluable on emitted signals and contradicts itself + Phase-5 vocabulary

plan.md:77-79 keys weights on `category`: `SANCTIONED 100,
MALICIOUS_ACTIVITY 70, LEGAL 0, SERVICE_TYPE 10, ENTITY_TYPE 0,
cross_chain_listing 20`.

- **Vocabulary mismatch.** `MatchedSignal.Category` carries the
  category *code* (`rc.code` — SCAM, MIXER, GAMBLING, EXCHANGE…;
  `service.go:801,807`; `migrations/000005_labels.up.sql:17-36`).
  `MALICIOUS_ACTIVITY`/`SERVICE_TYPE`/`ENTITY_TYPE`/`LEGAL` are
  `category_type` *tier* values (`000005_labels.up.sql:10`,
  `service.go:744-757`). As written the weight function cannot be
  evaluated against any emitted signal.
- **Internal contradiction.** `SERVICE_TYPE: 10` vs plan.md:86-87
  "Sentinel/classification-only (MIXER, GAMBLING, EXCHANGE) categories
  score 0" — MIXER and GAMBLING *are* SERVICE_TYPE
  (`000005_labels.up.sql:28-29`). Both statements cannot hold; one must
  be resolved (and per AGENTS §5 + Phase-5 `policy.go:30-32` the
  classification tiers are deliberately zero-weight — 10 needs
  justification, not just existence).
- **Kind/category conflation on SANCTIONED.** `risk_categories` seeds a
  `SANCTIONED` label category (`000005_labels.up.sql:17`): a
  `kind=label` signal with `category=SANCTIONED` (a community claim at
  `verification_status=reported`) would score the same 100 as
  `kind=sanctions_direct` (an OFAC listing). Phase-5 deliberately
  separates these — `sanctions_direct` flat 100 vs label-kind
  `SANCTIONED` base 85 × verification × confidence
  (`internal/scoring/policy.go:33-45`); Phase-6 pins the distinction
  ("a `category=SANCTIONED` item is distinguished by `kind`",
  Phase-6 plan §3.3.5). The weight table must key on `(kind, category)`,
  not `category`.
- **`confidence_factor` invents values.** plan.md:80-82 claims "same
  vocabulary as Phase 5" but `disputed 0.2` contradicts
  `verificationFactorsV1[VerificationDisputed] = 0`
  (`internal/scoring/policy.go:50-55`, "disputed, which IS zero").
  Disputed evidence contributing 20% is a semantics change, not
  vocabulary reuse.
- **Factor undefined on the two highest-weight kinds.**
  `sanctions_direct` and `listing` signals never carry
  `verification_status` (`service.go:778-788` sets only
  Kind/Category/Source/Entity/Programs; `service.go:813-819` sets
  Kind/Category/Network/ListedAddress/Source — `omitempty` field left
  empty, `model.go:72`). `weight(SANCTIONED) × decay × confidence(?)`
  is undefined for both.

### B3. `exposure_score` null-vs-0 semantics are unreachable as specified — test 9 cannot exist

plan.md:92-93: "`exposure_score` (int, null when no signals — never
0-as-safe confusion: absent signals ⇒ null score)". But items are only
appended when `len(sigs) > 0` (`service.go:363-366`, `388-390`) — a
no-signal item never reaches the wire, so the null case is unreachable
and test 9 (plan.md:128, "No-signal item → `exposure_score: null`")
has no possible fixture.

The ambiguity that actually matters is unspecified instead: an item
whose matched signals are *all zero-weight* (LEGAL-only,
ENTITY_TYPE-only, or only the classification-only codes of plan.md:86)
emits `exposure_score: 0` — the exact "0 reads as safe/informational"
confusion the invariant guards against. Pin one of: null when all
matched signals are zero-weight; always-integer with documented
informational-zero; or per-item `scoreable` flag. As written two
implementers produce different wire contracts.

## Part 2 — Majors

### M1. Pinned coverage fields are undefined at depth ≥3; the precedence guards are written against a single depth

- `counterparties_total` is pinned "expansion positions after sentinel
  exclusion" — the hop-1 set (`service.go:166`; Phase-6 plan §3.3.8).
  At max_hops=5: stays hop-1-only, or sums all frontiers? If hop-1-only,
  the reaffirmed invariant `examined + remaining =
  counterparties_total` (critique-round-9:69-72) breaks once
  `nodes_examined` counts deeper attempts. If summed, the pinned meaning
  changes silently and test 20's "max_hops≤2 bit-identical" survives
  only by coincidence.
- `positions_emitted` is pinned "contiguous resolved-prefix length" —
  a per-depth concept. plan.md:105 says "Per-depth prefix facts stay
  inside `positions_emitted` (global) plus new
  `frontier_emitted_by_hop`" — is the global value the depth-2 prefix,
  or the sum? Unstated.
- `nodes_remaining` is pinned "never attempted" — deeper frontiers have
  their own unattempted positions; the new fields list
  `nodes_examined_by_hop`, `frontier_by_hop`, `frontier_emitted_by_hop`
  (plan.md:103-106) but no `nodes_remaining_by_hop` and no global
  redefinition.
- `node_indexed_until` is documented "attempted positions only"
  (`api/openapi.yaml:1713-1716`) — does it now span every depth's
  attempted positions? (Uniqueness holds via dedup, but scope must be
  stated.)
- `max_depth_reached` (plan.md:104): deepest depth *attempted* or
  deepest with ≥1 emitted position? Differs when a depth is reached
  but emits an empty prefix.
- Truncation guards are literally single-depth:
  `len(positions) > s.cfg.MaxNodes && maxHops == 2`
  (`service.go:512`) and `budget.Exhausted() && nodesRemaining > 0`
  (`service.go:510`) — both need the per-depth generalization spelled
  out (e.g., `∃d: |frontier_d| > MaxNodes_d` where frontier_d was
  actually built; "nodesRemaining" needs the M1 definition first).
  plan.md:107-108 "reasons apply to whichever bound hit first at
  whichever depth" gestures at this but the precedence evaluation is
  post-assembly, not chronological — and "which depth hit" is
  unrepresentable in the single reason (acceptable, but say so).

### M2. `direction` filter scope at depth ≥2 is unspecified

Phase-6 pins `direction` to the hop-1 edge set *before* position
derivation (`service.go:129-134`; Phase-6 plan §3.3.8: "hop-1 edge
direction", "`direction` filters the hop-1 edge set **before** position
derivation"). plan.md never mentions `direction`. Two implementable
readings diverge: (a) keep hop-1-only filtering — e2..eN edges stay
unfiltered bidirectional (status-quo semantics, but a `direction=out`
response can then carry inbound e3 edges — an honesty question the plan
must acknowledge); (b) filter every depth's edges — changes frontier
composition and path semantics. Pin one.

### M3. Sentinel handling at depth ≥3 is absent — burn addresses will consume deeper tiers

Sentinels (`0x0`, `0x…dead`) are excluded from positions but still emit
matched items, enforced only at hop-1 (`service.go:55-58, 137,
160-165`). Frontier construction (plan.md:36-39) and the visited set
(plan.md:48-51) never mention them. Consequences: a `0xdead`
counterparty of a depth-2 node enters frontier₃, burns a `MaxNodes_3`
slot and provider budget on a guaranteed-empty address; worse, since an
*unmatched* sentinel at hop-1 is neither "emitted" (no signals → no
item) nor "queued in a shallower frontier" (never a position), the
plan's own dedup wording ("emitted at a shallower depth OR queued in a
shallower frontier", plan.md:48-50) does not cover it — it can be
re-emitted at every subsequent depth. Burn/mint addresses are among the
most common on-chain counterparties; this is a frequent-path defect.
Pin: sentinel exclusion applies at every frontier derivation and to the
visited set (queued *or discovered-as-counterparty* at shallower
depths).

### M4. Cross-chain section misstates what exists, invents `kind` vocabulary, and leaves precedence/shape/wiring undefined

- **Vocabulary drift.** plan.md:62-63 emits
  `MatchedSignal{kind:"cross_chain_listing", …}` — `kind` is a closed
  enum `[sanctions_direct, label, listing]`
  (`api/openapi.yaml:1621-1623`; `model.go:32-35`). Cross-chain
  observations already emit `kind:"listing"` with
  `category:"cross_chain_listing"` plus `network`, `listed_address`,
  `source` (`service.go:813-819`; `model.go:27`). Either a new enum
  value (undocumented contract change) or a field-name mistake.
- **Redundant with the existing path.** `LookupWallet` already returns
  `CrossChainObservations` + `UnresolvedNetworkListings` for the same
  normalized address on other/unresolved networks
  (`internal/sanctions/service.go:97-107, 165-188`;
  `queries/sanctions.sql:21-47`), folded into `matched_signals` already
  (`service.go:809-824`). What is genuinely new in §2.2 —
  foreign-network *labels* — is never separated from what exists, so a
  parallel sanctions path would duplicate signals in both
  `matched_signals` and `exposure_score` (mirror-source stacking,
  AGENTS §8).
- **Query unspecified.** `BulkMatchLabels` is network-scoped
  (`w.network_id = $1`, `queries/transactions.sql:147`) and returns no
  network identity — a foreign match needs either N−1 per-network
  calls or a new query projecting `networks.code`; neither is speced
  and no sqlc task exists in either phase file.
- **Precedence.** plan.md:64 "never competes for `SANCTIONED`" covers
  only one direction. Whether foreign-label signals join `consider()`
  (`service.go:769-774`) at all, with what rank — could a
  foreign-network SCAM label claim `item.category` over a local
  `cross_chain_listing`? — is unpinned (hunt question: "can it ever win
  precedence?" — the plan does not say).
- **Signal shape.** `listed_address`/`verification_status` semantics
  for a foreign *label*: `wallet_labels` has `verification_status` but
  no listing-address representation — what populates
  `listed_address` (the normalized address itself? the raw
  `wallets.address`)? Undeclared.
- **Wiring.** `tracing.Service` holds `{q, tx, sanctions}`
  (`service.go:61-69`) — no resolver. "Resolver-driven" foreign-network
  iteration (plan.md:60-61) requires injecting `network.Resolver` and
  changing `tracing.New` + `cmd/api/main.go:258-263` — absent from both
  phase files (phase-01:14 scopes main.go to env vars only).

### M5. Path-evidence rules at depth ≥3 under-specified

- `temporal_order_valid`: `temporalOK(e1, e2)` is a two-edge predicate
  (`service.go:668-670`). For N edges the semantics (every adjacent
  pair non-decreasing — transitively sufficient — vs all-pairs) are
  unstated; test 1's "temporal flag honest" (plan.md:113) is not a
  definition.
- `MaxPathsPair` at depth d: today keeps the 3 lexicographically-first
  *via* addrs (`service.go:393-399`). A depth-d sequence is a d-tuple;
  the ordering key (lexicographic over the address tuple?) and sequence
  identity are unpinned. plan.md:99 restates only
  `Edges length = HopCount`.
- **Enumeration cost unbounded in analysis.** Sequences per item are a
  product over ancestor-set sizes per depth (parents × grandparents ×
  …) before the `MaxPathsPair` cut. §5's cost argument
  (plan.md:160-162) counts nodes and pages but not path-enumeration
  work — an adversarial graph can make assembly the dominant cost.
  Pin a bound (e.g., per-depth parent maps with truncated enumeration).
- **Ancestor exclusion ambiguous.** "edges returning to root or to any
  frontier ancestor of the current path" (plan.md:50-51): positions are
  shared across parents, so "the current path" is undefined for a
  multi-parent position — is an edge to B excluded when B is an
  ancestor via *any* chain or only the selected one? The rule is also
  largely subsumed by visited+root exclusion (a shallower-depth node
  can never re-enter a deeper frontier), so its incremental bite —
  same-depth or selected-path ancestors — needs definition. Self-edges
  at deeper nodes (`service.go:127-129, 303-305` handle hop-1/root)
  are not mentioned for deeper frontiers.

### M6. Test list lacks discriminating coverage for pinned behaviors

Missing: (a) whichever `counterparties_total`/`nodes_remaining`/
`positions_emitted` semantics is chosen at ≥3 (M1) — the tests assert
new fields exist, not that old invariants hold or are redefined;
(b) sentinel exclusion at depth ≥3 with zero provider spend (M3);
(c) `direction` scope — deeper edges unfiltered (or whichever is
chosen, M2); (d) `temporal_order_valid` on a 3-edge path with a
non-monotone middle edge; (e) the B1 reading — a clean (unmatched)
depth-2 intermediary MUST seed frontier₃ (assert positions, not just
final items); (f) score rounding, all-zero-weight item semantics,
label-kind `SANCTIONED` vs `sanctions_direct` weight distinction, and
`disputed` factor; (g) foreign-label signal shape
(kind/category/network/verification_status/listed_address); (h) no
duplicate signals between the existing `SigListing` path and the new
foreign-label path; (i) `POST /screenings` `options.max_hops` stays
0–2 (`internal/screening/handler.go:95-97`, `openapi.yaml:1333`) —
declare unchanged so nobody "harmonizes" it upward; (j)
`node_indexed_until` populated for attempted positions at every depth;
(k) `max_depth_reached` semantics (attempted vs emitted). The
cross-chain E2E also needs a second seeded network — the test resolver
builds one network only (`service_test.go:101`), feasible but must be
planned.

## Part 3 — Minors

- **m1 Wiring/config details absent.** `Config.MaxNodes` is a single
  int commented "hop-2 expansion position cap" (`service.go:25`) — the
  per-depth type (map/slice) is unstated; legacy `TRACING_MAX_NODES`
  (`config.go:169`) vs new `TRACING_MAX_NODES_{2,3,4,5}` — does the old
  key remain the depth-2 default? `MaxPagesFor(hopN bool)`
  (`transaction/service.go:98-103`) must become depth-aware — signature
  change unlisted; `TXINDEX_MAX_PAGES_HOP3` (phase-01:15) vs plan.md
  `MaxPagesHop{2,3+}` — state explicitly that hops 3–5 share one tier.
  `ValidateRequest` (`service.go:858-872`) and both handler error
  strings "max_hops must be 1 or 2" (`transactions.go:144, 169`) need
  updating — listed nowhere.
- **m2 OpenAPI touch-list incomplete.** "OpenAPI updated"
  (plan.md:96) omits: summary "Bounded 1-hop/2-hop" (`:508`),
  description bounds paragraph (`:510-519`), `max_hops`
  `enum: [1, 2]` + "`0` or `>2` → 400" (`:525-526`), item `hops`
  `enum: [1, 2]` (`:1681`), new item `exposure_score` (nullable int),
  envelope `exposure_policy_version`, per-hop coverage fields.
  AGENTS §9 requires OpenAPI *examples* for contract changes — none
  given.
- **m3 Rounding unpinned.** Float factors
  (0.0625 × 70 × 0.7 = 3.0625) → `int` needs a rounding rule; Phase-5
  uses integer-tenths `(base×vf×cf + 50)/100` (`engine.go:159-172`) —
  reuse or pin.
- **m4 Pipeline placement unpinned.** Where in
  assemble → category-filter → sort → cut is the score computed? (Field
  on the item → compute at item build, before the filter.) Say it.
- **m5 Post-bound work amplification.** Per-counterparty
  `lookupSanctions` plus the new foreign-label queries run after the
  bounded loops under the same ctx; counterparty count is bounded by
  `edges_examined` (≤5000) — deadline there propagates as a 500, not
  `truncated=deadline` (pre-existing, amplified at depth 5). Declare
  the residual.
- **m6** `exposure_policy_version` when `items` is empty — pin (emit
  always).
- **m7 Formula source inconsistency.** plan.md:21-22 "computed from
  emitted paths and signal confidence" vs plan.md:72-74 summing
  `matched_signals` — paths play no role in the formula; align.
- **m8 No dedup/cap rule inside Σ.** A counterparty with
  `sanctions_direct` + label-kind `SANCTIONED` + `cross_chain_listing`
  sums all three; AGENTS §8 "no stacking points from mirror sources"
  and Phase-5's max-not-sum direct block (`policy.go:27`) — at minimum
  pin whether Σ is literal (and cap interplay with `min(100, …)` is
  the only bound).

## Part 4 — Nits

- **n1** Phase files are 15–20 lines vs Phase-6's granular scope lists —
  no file/function change lists (constructor signature, new/changed
  sqlc queries + regeneration step, model field diffs, test-file
  names). Below the repo's implementation-ready bar.
- **n2** Positive confirmations worth keeping: refusal to add
  `max_nodes_d{d}` reasons (vocabulary stays closed, plan.md:52-54);
  edge budget/provider budget request-global (plan.md:55); cross-chain
  explicitly never expands the graph (plan.md:14-19); score isolation
  from `risk_score` (plan.md:83-85); min-depth dedup direction correct.
- **n3** `nodeUntil` (`service.go:196`) per-depth vs shared map — keys
  are addresses so dedup guarantees uniqueness, but say whether the map
  is request-global.
- **n4** `IncTracingNodesExamined` stays a single global counter —
  consistent, but unmentioned in phase files.

## Assessment

The plan names the right invariants but leaves the three mechanics it
exists to specify — frontier construction, per-depth coverage
semantics, and the scoring weight table — ambiguous or internally
contradictory. B1 makes hops ≥3 depend on an unresolved reading; B2's
weight table cannot be evaluated on the signals the code emits and
contradicts both itself and Phase-5's disputed/classification
conventions; B3 specifies a null-case that cannot occur on the wire.
Every major has concrete file/line evidence and at least two
implementable divergent readings. The test list (20 items) is the right
shape but asserts around, not on, the undefined semantics.

CHALLENGED
