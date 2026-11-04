# Devin Critic — Round 7: Advanced Tracing plan convergence check (v3, verification)

Scope: `plans/20261104-0400-advanced-tracing/` — verify the two
claimed-but-absent round-6 fixes (m2 test 10, n4 test 5) are now
present in `plan.md` as written, then a scoped consistency check on
the touched lines only. Code anchors re-checked: `migrations/
000001_init.up.sql:18-19` (`wallets.address` vs `normalized_address`),
`queries/labels_publish.sql:5` ("wallets.address keeps the CSV
spelling verbatim"), `queries/transactions.sql:138` (`BulkMatchLabels`
selects `w.normalized_address`), `internal/provider/ofac/
parser_test.go:56-57` (checksummed `ListedAddress` vs lowercase
`NormalizedAddress`), `phase-01`, `phase-02`.

## Punch-list verification — both landed

| Item | Prior state | Verified state |
|---|---|---|
| m2 | test 10 asserts `listed_address` = ORIGINAL `wallets.address` + checksum fixture | **LANDED** — plan.md:309-311 reads "`listed_address`=the foreign `wallets.address` ORIGINAL representation (fixture seeds a checksum-case `address` so original ≠ normalized — discriminating)". The stale "`listed_address`=normalized addr" assertion is gone; grep over plan.md finds no surviving normalized-pin for `listed_address` (remaining `normalized` hits are unrelated: :65 sort key, :202 match key, :311 the new qualifier itself). |
| n4 | test 5 routed through `frontier_by_hop` + call counts + item absence | **LANDED** — plan.md:295-297 reads "edge to dedup-set member excluded — asserted via `frontier_by_hop`/`nodes_examined_by_hop` values, provider-call counts, and item absence." The loose "(assert frontier_3 contents, not just items)" phrasing is gone; `frontier_by_hop` now appears at test 5 (plan.md:296). |

## Scoped consistency check on the touched lines

### Test 10 (m2) — consistent end-to-end

- §3 pin (plan.md:213-214) `listed_address:<foreign wallets.address
  original representation>` — test 10 now asserts exactly this. The
  round-6 §3-vs-§6 value-level contradiction is resolved.
- phase-02:7 projects `w.address AS listed_address` — the
  original-representation column; consistent.
- Schema confirms the distinction is load-bearing:
  `wallets.address` (verbatim source spelling, `labels_publish.sql:5`)
  vs `wallets.normalized_address` (lowercase index form,
  `000001_init.up.sql:18-19`, `UNIQUE(network_id, normalized_address)`).
  A checksum-case seed therefore forces original ≠ normalized —
  the test is genuinely discriminating, matching the codebase
  convention (OFAC `ListedAddress` preserves checksum case,
  `parser_test.go:57`).
- "no duplicate signals vs `kind:"listing"`" matches §3's "sanctions
  observations already flow through the existing `kind:"listing"`
  path — untouched, no duplication" (plan.md:203-204).
- "category never wins; solely-foreign item →
  `category="cross_chain_listing"`" matches §3 precedence verbatim
  (plan.md:216-219: foreign signals skip `consider()`).
- Ownership: test 10 sits in phase-02's list (phase-02:34), which owns
  the `BulkMatchLabelsCrossNet` projection — coherent.

### Test 5 (n4) — consistent end-to-end

- `frontier_by_hop`/`nodes_examined_by_hop` are pinned wire fields
  (§2.7 plan.md:144-145, keyed by materialized frontier index
  {2..max_hops}) and phase-01 `Result` fields (phase-01:12-14) —
  asserting via them fixes the round-6 nit (frontier membership was
  not a wire surface).
- Semantics check on the A→B→C→A fixture: A ∈ dedup as root
  (§2.4:71-72); §2.4:83-85 — a dedup'd candidate "emits no item and
  no position at the deeper level"; §2.2:52-53 — dedup-set far
  endpoints' edges never re-emitted. Therefore: no frontier_3
  position for A (`frontier_by_hop[3]` excludes it), no attempt
  (`nodes_examined_by_hop[3]` never counts it), no provider call,
  no item — all three assertion channels are implementable and
  discriminating. Test 5 is the verification of §2.4's "ancestor
  cycles are impossible" claim — coherent, not contradictory.
- Ownership: test 5 sits in phase-01's list (phase-01:27).

### No fresh contradictions introduced

- The two edits touch only assertion text; no mechanism pins moved.
- No stale phrasing survives ("frontier_3 contents", "normalized
  addr" for `listed_address`) anywhere in plan.md.
- §4's "MatchedSignal stays unchanged on the wire" (plan.md:233-234)
  remains scoped to the scoring computation (score is a separate
  field); §3's added `network`/`listed_address` fields on
  foreign-label signals is a pre-existing v3 pin reviewed in prior
  rounds — wording-level at most, not a mechanism-level
  contradiction.

## Assessment

Both mandated repairs are verified in the file — not merely claimed
in a rebuttal. m2 eliminates the §3-vs-§6 self-contradiction (test 10
now asserts the same observable value §3 pins, under a fixture that
makes the assertion discriminate). n4 routes test 5 through pinned
wire surfaces plus call counts and item absence, exactly as
specified. The scoped consistency check over the touched lines found
no new mechanism-level contradiction; every interacted pin (§2.2,
§2.4, §2.7, §3, phase-01, phase-02, schema, code convention) agrees.

Per the round-6 gate: "Once both land — verified in the file, not
just the rebuttal — the consensus gate passes." Both have landed and
verified. Round-6's underlying assessment stands: the plan is
mechanism-complete and bit-identical at max_hops≤2 under every
pinned reading.

**PLAN_CONSENSUS_REACHED** — 0 blockers, 0 majors, 0 minors, 0 nits
outstanding; 7/7 cumulative punch-list items verified applied across
rounds 6–7.
