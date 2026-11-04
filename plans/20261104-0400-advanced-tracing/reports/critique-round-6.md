# Devin Critic — Round 6: Advanced Tracing plan convergence check (v3)

Scope: `plans/20261104-0400-advanced-tracing/` — punch-list verification
of `rebuttal-round-5.md` against `plan.md` (v3), `phase-01`, `phase-02`;
scoped consistency skim of §2.4–§2.7 interactions touched by the new
tests 33–35. Code anchors re-checked: `internal/tracing/service.go`,
`internal/provider/ofac/parser_test.go:57` (checksummed
`listed_address` = original representation), `migrations/000001` /
`000005` columns (`wallets.address` vs `normalized_address`,
`wl.confidence`).

## Punch-list verification — 5 of 7 landed, 2 claimed-but-absent

| Item | Rebuttal-5 claim | Verified state |
|---|---|---|
| m1 | tests 33–35 → phase-01 | **LANDED** — phase-01:27 reads "1–5, 8, 9, 12–19, 21–24, 26–31, **33–35**"; union with phase-02:34 {6,7,10,11,20,25,32} covers 1–35 with no orphans or overlaps. |
| m2 | test 10 asserts `listed_address` = ORIGINAL w/ checksum fixture | **NOT LANDED** — plan.md:308 still reads "`listed_address`=normalized addr"; no "original"/"checksum" qualifier anywhere in plan.md (only 4 hits: :65 normalized_address sort key, :202 normalized match key, :213–214 §3 pin, :308 the stale assertion). |
| m3 | test 26 disambiguated | **LANDED** — plan.md:344–346 now two clauses matching §2.7:147–149 verbatim: "scheduled-but-zero-attempts (budget gone before first attempt) → not counted; attempted-then-clipped (deadline mid-expansion, emitted prefix 0) → counted". The ambiguous "empty-prefix → not counted" phrasing is gone. |
| n1 | §2.4 heading → "Dedup set" | **LANDED** — plan.md:68 "### 2.4 Dedup set (min-depth + cycle exclusion, unified)"; zero `visited` survivors in plan.md. |
| n2 | `conf(sig)` → `vf×cf` | **LANDED** — phase-01:16 "`vf×cf`, integer per-term rounding (sixteenths decay, half-up)". |
| n3 | materialized-frontier gloss expanded | **LANDED** — plan.md:129–131 "frontier_2 is materialized unconditionally (Phase-6 derives `positions` for ordering/dedup/coverage regardless of expansion)". |
| n4 | test 5 routed through `frontier_by_hop` + call counts + item absence | **NOT LANDED** — plan.md:296 unchanged: "(assert frontier_3 contents, not just items)"; `frontier_by_hop` appears nowhere near test 5. |

## Findings

### m2-carry — the unlanded edit leaves plan.md self-contradictory as written

This is the same defect as round-5 m2, but its persistence changes the
verification verdict, not the severity:

- §3 pins the wire value: `listed_address:<foreign wallets.address
  original representation>` (plan.md:213–214); phase-02:7 projects
  `w.address AS listed_address` — the original-representation column.
- §6 test 10 asserts the same field equals `normalized addr`
  (plan.md:308).
- The fixture is on record as checksum-seeded (rebuttal-3:28–29,
  "m8″ test 10 seeds checksum-case original"), under which
  `wallets.address` (`0xAbC…`-form) ≠ `normalized_address` (`0xabc…`).
  The codebase confirms the distinction is load-bearing: OFAC
  `listed_address` preserves checksum case
  (parser_test.go:57), while `normalized_address` is the lowercase
  index form (AGENTS §6: "lowercase for indexing, original
  representation preserved").

Two mutually exclusive pins for one observable value = the document is
self-contradictory as written: an implementer conforming to §3/phase-02
produces `0xAbC…` and fails test 10; one conforming to test 10 violates
§3's provenance pin. This is a value-level contradiction, not a
wording-level nit — and it sits precisely on the axis m2 flagged. The
escape reading (seed `wallets.address` already normalized, making the
assertion pass) collapses the test to non-discriminating on the exact
axis m8″/m2 exist to verify — and contradicts the rebuttal-pinned
fixture either way.

Repair remains one clause: "`listed_address` = foreign
`wallets.address` original (checksum-seeded fixture)".

### n4-carry — nit, noted, non-blocking on its own

Test 5's "assert frontier_3 contents" is loose (frontier membership is
not a wire surface); the claimed routing through `frontier_by_hop` +
provider-call counts + item absence is absent. Cosmetic — the assertion
is implementable as intended. Would not independently justify a
challenge; recorded because the rebuttal claims it applied.

### Process finding — rebuttal overstates its diff

rebuttal-round-5.md claims all 7 items applied; 2 are verifiably absent
from the files. Both absent items are named edits in the rebuttal's own
bullet list (m2, n4). Consensus cannot be granted against claimed-but-
unapplied edits — the convergence gate is verification of the document,
not of the rebuttal's intent.

## Scoped fresh-consistency skim (no new hunt)

Re-verified only the interactions the round-5 edits touched:

- **Sentinel lifecycle (test 33) consistent.** At depth 2 the level-1
  sentinel is excluded via Phase-6's `{root} ∪ cps` domain (post-filter
  set ≡ `cps`); at depth ≥3 via §2.4's "post-filter cp sets at scan
  levels ≤ d−2" (d=3 → scan level 1 included). "Dedup covers sentinels"
  holds end-to-end; §2.4's explicit scoping "d ≥ 3" leaves level 2 to
  Phase-6's own exclusion — coherent, not a gap.
- **minPos domain (test 34) = §2.5** verbatim ("min over contributing
  sequences of the FIRST via's position index in frontier_2").
- **max_hops=1 coverage (test 35) = §2.7** (materialized {2}, examined
  0, remaining = total, `max_nodes` gated on scheduled).
- **direction interplay (test 29) = §2.4** post-filter domain pin
  (filtered-out cp legitimately re-emittable at hop ≥2 via unfiltered
  deep scans).
- **`exposure_score` arithmetic** re-derived: min non-zero term ≥ 1
  (sixteenths decay, half-up) ⇒ Σ=0 ⟺ all-zero-weight ⟺ `null`;
  `0` unreachable on the wire (§4 invariant intact).
- **Field-name parity** §2.7 ↔ phase-01:12–14 (`frontier_by_hop`,
  `nodes_examined_by_hop`, `frontier_emitted_by_hop`,
  `nodes_remaining_by_hop`, `max_depth_reached`) — identical.

No fresh mechanism-level contradiction found. Round-5's assessment
stands: the plan is mechanism-complete and bit-identical at
max_hops≤2 under every pinned reading.

## Assessment

The mandated verification failed: 2 of 7 punch-list fixes are absent
from the document despite explicit rebuttal claims. One (m2) leaves
plan.md literally self-contradictory — §3/phase-02 pin `listed_address`
to the original representation while §6 test 10 asserts the normalized
form, under a fixture that forces those to differ. That clears the
round-6 bar on its narrowest reading ("self-contradictory as written"):
the plan cannot be implemented with all its own assertions green. The
other (n4) is cosmetic and would not block alone.

This is a verification-failure challenge, not a fresh mechanism hunt —
no new design objections exist and none are asserted. The repair is two
one-line edits: s/normalized/original/ in test 10 (plus the checksum-
fixture qualifier), and reword test 5's assertion through
`frontier_by_hop` + call counts + item absence. Once both land —
verified in the file, not just the rebuttal — the consensus gate
passes; everything else is already verified sound.

**CHALLENGED** — 0 blockers, 0 majors, 1 carried minor (m2 unlanded =
§3-vs-§6 self-contradiction as written), 1 carried nit (n4 unlanded);
5/7 punch-list items verified applied; rebuttal overstates its diff.
