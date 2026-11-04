# Rebuttal — Critic round 1 (all findings accepted)

## Blockers

**B1** — accepted; pinned Reading A (all scanned edges of
emitted-prefix nodes seed the next frontier — clean intermediaries
reach deeper hops). Added: `position{addr, parents}` structure §2.2;
multi-parent ordering = `firstEdge` over the union of ALL incoming
edges §2.3; `minPos` = position index in its own frontier §2.5.

**B2** — accepted; weight table re-keyed on `(kind, code/tier)`:
`sanctions_direct` 100; label-kind `SANCTIONED` 85 × conf (distinct
from OFAC by kind, per Phase-5); tier MALICIOUS_ACTIVITY 70, all other
tiers 0 (resolves SERVICE_TYPE self-contradiction — MIXER/GAMBLING/
EXCHANGE are 0, matching AGENTS §5); `listing` 20. `conf`: verified
1.0 / reported 0.7 / unverified 0.5 / disputed **0.0** (Phase-5
semantics); n/a kinds → 1.0. Rounding = integer-tenths reuse.

**B3** — accepted; pinned: `exposure_score` is **null when all matched
signals are zero-weight** — 0 never appears; the no-signal case is
unreachable by construction (items need sigs>0) and test 9 was
rewritten to the all-classification fixture.

## Majors

**M1** — coverage pinned §2.7: `counterparties_total` = Σ frontiers
(max_hops≤2 ⇒ single frontier = today's value); `positions_emitted` =
Σ prefixes; `nodes_remaining` redefined `total − examined`; per-hop
counter fields + `max_depth_reached` = deepest *attempted* depth;
`node_indexed_until` spans depths; truncation guards generalized.

**M2** — pinned status quo: `direction` filters hop-1 only; deeper
edges unfiltered; declared residual §2.8.

**M3** — pinned: sentinel exclusion at EVERY frontier + visited set
covers discovered counterparties (sentinel or not) §2.1/§2.4.

**M4** — rewritten §3: only foreign-network *labels* are new
(sanctions observations unchanged, no duplication); new sqlc
`BulkMatchLabelsCrossNet` projects network code; signals stay
`kind:"label"` with `network`/`listed_address`; never in `consider()`;
solely-foreign item → `cross_chain_listing`; resolver injected into
`tracing.New` + main.go — all listed in phase files.

**M5** — pinned §2.9: adjacent-pair temporal semantics; tuple-ordered
sequences; `MaxPathsPair` cut; parent-map truncation bound
O(MaxPathsPair² × MaxNodes); ancestor exclusion unified into visited
set.

**M6** — tests rewritten 1–21 covering every flagged gap (B1 guard,
sentinel@depth≥3, direction scope, temporal mid-edge, coverage
invariants, signal shape, dedup vs listing kind, screenings max_hops
frozen, per-depth node_until, max_depth_reached semantics).

## Minors/Nits

m1–m8 pinned in §2.6/§2.7/§4/§5 (config map+legacy fallback,
MaxPagesFor(depth), ValidateRequest+error strings, OpenAPI touch list,
rounding, pipeline placement, post-bound residual, version always
emitted, formula aligned to signals only, literal Σ + min(100));
n1 — phase files rewritten granularly; n3/n4 confirmed (request-global
nodeUntil; global metric).
