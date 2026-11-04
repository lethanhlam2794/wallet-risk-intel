# Devin Critic — Round 8: Transaction Tracing plan — final check

scope: `plans/20261104-0056-transaction-tracing/` (plan.md + 4 phase
files) verified against `rebuttal-round-7.md` (2 minors + 4 nits),
`docs/tailieu.md` touch-points, and the accumulated decided-choices
record (R1–R7) so nothing already adjudicated is relitigated.

## Part 1 — R7 amendment verification

All 6 R7 fixes **landed** and are internally consistent:

| # | Verdict |
|---|---|
| R7-m1 total item-order key `(hops, min contributing position index, counterparty)` | **Landed** — plan.md:301–304 ("a total key"), phase-03:9 identical. |
| R7-m2 per-endpoint `direction` filter enums | **Landed** — plan.md:413–417 (`/transactions` {in,out,self}, `self`⇒self-rows only; `/exposures` {in,out}); phase-02:9, phase-03:9, phase-04:8 all aligned. Item-side `"self"` third value stays emitted-only (plan.md:411–413). |
| R7-n3 `coverage.head_block` = root's observed head | **Landed** — plan.md:282–284 explicit; per-node heads stay internal to `node_indexed_until`; phase-03:9 synced. |
| R7-n4 stale `nodes_*=0` parenthetical | **Landed** — plan.md:268–270 now enumerates examined/emitted/failed=0 + remaining=total; :293–295 repeats. |
| R7-n5 sentinel items pin | **Landed** — plan.md:197–199 "sentinels DO emit items when matched … `counterparties_total` excludes them regardless"; phase-03:9 synced. → but see R8-m1. |
| R7-n6 header | **Landed** — plan.md:6 round count updated. |

## Part 2 — Remaining defects

## [Minor] R8-m1 — Matched hop-1 *sentinel* items have no defined `min contributing position index`: the "total key" is not total over the emitted-item domain

The R7-n5 fix made sentinels emit items ("they are counterparties,
just not expansion positions", plan.md:197–199), while R7-m1 pinned
item order as `(hops asc, min contributing position index,
counterparty asc — a total key)` (plan.md:301–304; phase-03:9). A
hop-1 sentinel item is emitted with `hops=1` but its counterparty is
**not a position** — positions are the post-sentinel-exclusion
expansion slots (plan.md:194–199, 268; "positions derive from
filtered edges" :290–291). Its "min contributing position index" is
therefore undefined.

- Hop-1 non-sentinel items are fine (the counterparty *is* a
  position; indices are unique).
- Hop-2 sentinel items are fine (discovered through a contributing
  position A → index = idx(A)).
- Hop-1 sentinel items are the gap: three defensible readings —
  would-be rank in the pre-exclusion counterparty order, sort-after
  all positioned items, counterparty-asc alone — produce **different
  wire orders**, and the `limit`/`response_cap` "cut at item
  boundary" inherits the nondeterminism. Test 22's "identical given
  identical resolution sets" is unsatisfiable whenever a matched
  sentinel shares a response with other items.

This is the same defect class R7-m1 just fixed — reintroduced at the
domain edge by R7-n5.

**Amendment (one clause):** pin sentinel-item ordering — e.g. "a
hop-1 item whose counterparty holds no position sorts by its rank in
the pre-exclusion expansion sequence" (or "sentinel items sort after
all positioned hop-1 items" — any explicit rule restores totality).

## [Nit] R8-n2 — phase-01:6 "`indexed_until_block`=last terminal write" mislabels the watermark

phase-01:6 writes "indexed_until_block=last terminal write, indexed_at"
— the "last terminal write" property belongs to `indexed_at`
(plan.md:135–137); `indexed_until_block` is the min-contiguous-
coverage watermark. A charitable reading ("the watermark as of the
last terminal write") is true but the terse label can be misread as a
timestamp. plan.md is authoritative and correct; a one-word reword in
the phase file kills the only remaining place an implementer could
conflate the two columns.

## Checked and rejected (decided or forced readings — not defects)

- **chainscan 10k-record cap**: adjudicated dead code at stated
  bounds — page_size 1000 × max_pages 5 = 5000 < 10000 (R2:105–106,
  rebuttal-1 B3). Within one EnsureIndexed a stream is always bound-
  stopped before the wall; `history_truncated` + watermark carry the
  honesty.
- **`head < fetch_from` ⇒ "no upstream call"**: the head fetch is
  what evaluates the predicate; `head_block` on the wire evidences
  it. "No upstream call" = no transfer-window calls — decided R5-m3/
  R6-m5.
- **`matched_signals[].category` for `kind=listing`**: not spelled
  out, but the pinned semantics force it — `category` filtering
  "matches all signals" and item-level category for listing-only
  matches is `cross_chain_listing`; any other value would make a
  `category=cross_chain_listing` filter unmatchable. Derivable.
- **`coverage.provider_status` duplicated inside `root{}`**: same
  value both places (request outcome ≡ persisted terminal state);
  redundant, not contradictory.
- **`max_edges` vs "hop-1 items unaffected by max_nodes/budget"**:
  the immunity clause names only those two bounds; a max_edges cut
  mid-hop-1-scan is a different, declared bound — no contradiction.
- **`plan.md:222` loose "then expansion order" restatement**: the
  pinned form at :301–304 governs; R7 already accepted this
  coexistence.
- **`cross-chain-only path domain for lex-3 path pick`**: "beyond-
  prefix results excluded from items" forces path candidates to
  emitted-prefix positions — coherent reading is compelled.

## Verified-as-true (final state)

- **Arithmetic**: BSC window advance 100,001−100=99,901/call ⇒ ~1300
  calls cold convergence vs ~65M head; nodereal `toBlock−fromBlock ≤
  100,000` = `window_cap` span bound; coverage `examined+remaining=
  counterparties_total` holds under `max_hops=1` explicit rule;
  max-per-stream `.{k}` counting consistent across adapters
  (nodereal W→W `max(1,1)=1`; chainscan kind-stream degeneracy).
- **State machine total**: gate {fresh,partial}∧≤TTL; `indexing`
  joins-or-retries at `IndexingStaleAfter`; `not_indexed` fetches;
  `disabled` re-evaluates `Enabled()`; `failed` cools 60 s;
  `indexed_at`=terminal-write upsert doubles as stale+cooldown clock;
  `provider_status` totality holds (served envelopes only ever carry
  terminal states).
- **Wire fields all servable**: every coverage subfield has a named
  source (persisted columns, per-request counters, once-per-request
  snapshot versions, root-head, attempted-positions map); per-kind
  `matched_signals` applicability is complete; item order key is
  total *except* the R8-m1 sentinel corner.
- **Contract surface aligned**: direction enums identical across
  plan.md, phase-02, phase-03, phase-04; truncated enum + precedence
  identical plan↔phase-03; `limit≤100` default 50 both endpoints.

## Assessment

Seven rounds of amendments landed cleanly; providers, window
arithmetic, content-derived identity, contiguous-prefix BFS, coverage
honesty, and the index-state machine are implementable as written.
One genuine residual remains: the R7-n5 sentinel-emission rule
reopened the R7-m1 non-total-order-key defect at the hop-1 boundary —
a matched sentinel counterparty emits an item with no position index
to sort by. One-clause fix, plus one optional nit. Neither touches
the design.

CHALLENGED
