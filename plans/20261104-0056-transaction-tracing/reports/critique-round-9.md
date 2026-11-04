# Devin Critic — Round 9: Transaction Tracing plan — consensus check

scope: `plans/20261104-0056-transaction-tracing/` (plan.md + 4 phase
files), verified against `rebuttal-round-8.md` (R8-m1 sentinel index,
R8-n2 phase-01 watermark label), the live codebase touch-points named
in phase-04, and the decided-choices record (R1–R8). Bar per prompt:
**unimplementable or self-contradictory as written**.

## Part 1 — R8 residue verification

| # | Verdict |
|---|---|
| R8-m1 sentinel-item ordering = own rank in hop-1 expansion ordering | **Landed** — plan.md:302–304 ("a sentinel item, which never becomes a position, uses its own rank in the hop-1 expansion ordering as its index — then `counterparty` normalized_address asc — a total key"); phase-03:9 identical. Key is now total over the emitted-item domain: positioned hop-1 items → own position index; sentinel items → own rank; hop-2 items → min contributing position; `counterparty` asc final tiebreak unique per item (dedup by counterparty). Test 22's determinism property is now satisfiable. |
| R8-n2 phase-01 watermark label | **Landed** — phase-01:6 reads "`indexed_until_block`=watermark, `indexed_at`=last terminal write"; conflation eliminated. |

## Part 2 — Live-codebase touch-point verification (phase-04)

Every file/line/symbol the plan names exists:

- `api/openapi.yaml:14` — stale "Transaction tracing remains
  intentionally absent" description present as described; `:1316` —
  `tracing:` field with `enum: [NOT_IMPLEMENTED]` and "lands in
  phase 6" description.
- Screening consumers — `internal/screening/service.go:297`,
  `mapper.go:323` (emission sites), `model.go:171` (comment),
  `service_test.go:354` (assertion). All four carry
  `"NOT_IMPLEMENTED"` exactly as phase-04:8 enumerates.
- Feature-map captures — **exactly 5** `docs/feature-maps/assets/*.json`
  files contain `"tracing": "NOT_IMPLEMENTED"` (screening-* captures);
  the "regenerate 5 captures" task is precise, not approximate.
- `internal/provider/resilience/` — `NewClient`, `WithClock`, `OnResult`
  all exist; `OnResult` indeed carries (source, result) with no
  latency — phase-04:9's "call-site observe or OnDuration" note is
  accurate.
- `sanctions.Service.LookupWallet` (cache-backed signature per plan)
  and `LatestSnapshotPublished` (db/sanctions.sql.go:178) exist for
  the coverage fields.
- Migration sequence: current max `000007` → plan's `000008` is next.
- `internal/wallet`, `internal/screening`, `internal/provider/` module
  layout matches AGENTS §3 structure.

## Part 3 — Fresh skim: checked and rejected (forced/decided readings)

- **`.{k}` singleton representation** — the text conditions suffixes
  on duplication, but the only reading compatible with the plan's own
  claims (page-boundary-mid-group "self-healing", the `t:{h}.1`/`.2`
  example, "zero loss, zero collisions", "identity must not depend on
  fetch context") is every occurrence carrying `.k`, k=1..N — a
  singleton `t:{h}.1`. Any other numbering orphans the first-seen row
  on tail refetch. Unique consistent reading is compelled; not a
  defect.
- **`LEGAL` intra-tier order** — the first enumeration in §3.3.5
  omits LEGAL; the same paragraph's authoritative restatement pins
  `LEGAL > MALICIOUS_ACTIVITY > SERVICE_TYPE > ENTITY_TYPE > OTHER`
  and phase-03:7 carries only the full order. Decided R5-m2/R6-m6;
  `risk_categories` seeds make LEGAL-first load-bearing.
- **Cross-process `indexing` rows** — singleflight is in-process;
  the plan defers + documents the cross-process advisory lock.
  Worst case is a duplicate idempotent upsert sequence. Declared
  residual, not a contradiction.
- **Hop-1 matching vs bounds immunity** — "hop-1 items unaffected by
  max_nodes/budget" is consistent because the bulk label query covers
  all counterparties in one batch and per-node `LookupWallet` is
  cache-backed (zero provider calls); hop-2 per-node work is what
  bounds govern.
- **`provider_status` totality** — served envelopes only ever reflect
  terminal states (`indexing` joins-or-retries, `not_indexed` always
  fetches); ErrKind-or-"ok" vocabulary holds on both endpoints.
- **`examined + remaining = counterparties_total`** — holds with
  attempted-but-beyond-prefix counted in `nodes_examined`, and the
  explicit `max_hops=1` rule (examined/emitted/failed=0,
  remaining=total).
- **`direction` enums** — `/transactions` {in,out,self} with self
  matching in/out; `/exposures` {in,out} with self excluded by
  construction. Identical across plan.md:415–419, phase-02:9,
  phase-03:9, phase-04:8.
- **`head < fetch_from` "no upstream call"** — decided R5-m3/R6-m5:
  the head fetch evaluates the predicate; `head_block` on the wire
  evidences it.
- **Stale header** plan.md:6 says "7 rounds → ~115 findings" vs actual
  8/~122 — cosmetic status metadata, updated per rebuttal convention
  (R7-n6 precedent). Not an implementability defect.

## Assessment

Both R8 findings landed cleanly; the total order key is now truly
total. A fresh pass over plan.md and all four phase files found no
contradiction and nothing unimplementable: provider arithmetic,
content-derived `transfer_index`, the index-state machine,
contiguous-prefix BFS, coverage-honesty fields, and the dedup/order
pipeline are all specified to a unique consistent reading, and every
named codebase touch-point exists as described. Nine rounds, ~122
findings applied; the design stands.

PLAN_CONSENSUS_REACHED
