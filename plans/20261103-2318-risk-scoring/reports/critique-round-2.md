# Devin Critic — Round 2: Risk Scoring & Screening API (Spec Phase 5)

**Verdict: CHALLENGED — 3 remaining blockers.** All 11 round-1 amendments
verified landed, in substance and in spirit. The rewrite, however,
introduced three small specification gaps — each is a one-to-three-line
clarification, none require redesign. Because all three sit on scoring or
idempotency-correctness paths, they block sign-off.

## 1. Amendment Verification (all 11 landed)

| # | Amendment | Status | Evidence |
|---|---|---|---|
| 1 | Idempotency claim protocol | ✓ landed | plan §3.3 — claim-first, single-tx result+claim, poll/takeover/canonical-hash all specified |
| 2 | Phase-1 constraints | ✓ landed | phase-01 L25-41: `result` type `json`, key CHECK 1–255, `claimed_at`, spec-4 status CHECK, PENDING dropped |
| 3 | Availability fold keyed on data_status | ✓ landed | plan §3.5 + §3.1 note; phase-03 success criterion (no-snapshots → INSUFFICIENT_DATA + NULL) |
| 4 | All-labels collection | ✓ landed | phase-03 — cursor loop to exhaustion, hard cap 1000 + `truncated` marker |
| 5 | All 14 §9.7 fields incl. `evidence` | ✓ landed | phase-03 — `{kind, ref}` flattened list defined |
| 6 | include_*/max_hops matrix | ✓ landed | plan §3.6 — evaluated:false, providers always run, defaults, reject-not-clamp |
| 7 | Detached context | ✓ landed | plan §3.2 `context.WithoutCancel` + own deadline; phase-03 requirement + test #7 |
| 8 | Phase-02 spec gaps | ✓ landed | dimension→mechanism table, MIXER=0 rationale, confidence folded, dedup key, category→type via risk_categories, max provider_score, fixed precedence, closed code enum |
| 9 | Exposure conditions | ✓ landed | `basis: provider_reported`, hops>2 informational-only, max_hops filter, coverage NOT_IMPLEMENTED |
| 10 | §17 metrics + log fields | ✓ landed | plan §3.9; phase-03 modifies metrics.go |
| 11 | Test list | ✓ landed | phase-04 criteria verbatim #1–#10; invariant cases duplicated in phase-03 |

The amendments adopted my recommended designs where offered (claim-first
+ WithoutCancel + single-tx; `json` column type; DISABLED…see finding 3).

## 2. Remaining Blockers (3 — all specification-level)

### B1. `request_hash` check must precede ALL claim-state branches

`plan §3.3` / `phase-04` apply the hash comparison only in the
`result_id`-set branch. The claim row carries `request_hash`; two corners
are currently unspecified and admit wrong implementations:

- **Stale-claim takeover with mismatched payload**: T1 claimed
  (client,key) with hash(A), died; T2 arrives with hash(B) — literal
  branch order says "stale → DELETE + re-INSERT", which would **erase
  request A's idempotency record and replace it with B's**. Key reuse
  with a different payload must be `409 IDEMPOTENCY_CONFLICT` regardless
  of claim state — takeover is only legitimate for the *same* request.
- **In-flight terminal replay**: "terminal → replay" would serve
  request A's result body to request B if hashes differ.

Required text: *"On conflict-load, compare `request_hash` first:
mismatch → `409 IDEMPOTENCY_CONFLICT` in every claim state (set,
in-flight, stale); match → proceed to replay / poll / takeover."*

### B2. Provider observations contribute to two blocks (double count)

Amendment 8 folded provider observations into the category×verif×conf
table in the **direct** block (§3.4: "Applies to `wallet_labels` AND
provider observations"), while the **other** block still reads "provider
OK malicious-category observations (+10 each, deduped, cap 20)". The same
publicaml SANCTIONED observation would score ~42 (85×0.7×0.7) in direct
AND +10 in other — the same evidence counted twice, violating the plan's
own "no double-counting" rule (spec §10.2).

Required text: a single-block-membership rule, e.g. *"Each finding
contributes to exactly one block. Provider observations whose category
resolves to a weighted MALICIOUS_ACTIVITY/LEGAL entry score via the
category table in the direct block only; the other block's +10 applies
only to provider signals/observations NOT weighted in direct — or the
+10 is dropped entirely."* (Either resolution acceptable; pick one and
make it unambiguous — determinism within v1.0.0 requires it.)

### B3. `DISABLED` providers must not fold as degraded

§3.5: "PARTIAL: ≥1 usable input + ≥1 degraded/failed/**disabled** input"
makes COMPLETED unreachable in the default deployment — chainabuse is
DISABLED without `CHAINABUSE_API_KEY` (`cmd/api/main.go#L161-168`), so
every screening is permanently PARTIAL. It also contradicts
`enrichment.aggregate()` which deliberately skips DISABLED when counting
attempted providers (`internal/enrichment/service.go#L577-579`) — a
provider that made no call is not a failure.

Required text: *"DISABLED providers are excluded inputs (configuration,
not execution): reported in `provider_status`, ignored by the fold — same
posture as `enrichment.aggregate()`."* (If a deliberate PARTIAL is
intended for missing coverage, say so explicitly and update the phase-03
criteria; otherwise adopt the exclusion.)

## 3. Non-Blocking Nits (record for implementation, no plan change needed)

- **Exposure weight scope ambiguity**: phase-02 "`hops==1` 10 pts,
  `hops==2` 5 pts × reported factor" — ambiguous whether the ×0.7 applies
  to both weights or only 2-hop, and whether `round()` applies. Implement
  consistently (recommend: both scaled, `round()` per the shared rule).
- **Redundant index**: `screening_requests(client_id)` is covered by the
  `UNIQUE(client_id, idempotency_key)` btree — harmless, can drop.
- **Replay `Location` header**: optional; 200 + stored body + replay
  header is fine as specified.
- **Stale-claim margin**: 5 min vs bounded pipeline (~30 s) is a safe
  margin — confirmed reasonable.

## 4. What Landed Well

- The claim state machine is now genuinely implementable — single-tx
  result+claim attach closes the result-without-claim hole; WithoutCancel
  closes the disconnect-orphan hole; takeover closes the dead-owner hole.
- The `data_status`-keyed fold kills the round-1 top finding; the
  unknown≠safe case is a named success criterion in both phase-03 and
  phase-04.
- `json` column type + explicit `screened_at` makes byte-faithful replay
  honest rather than aspirational.
- The §10.1 dimension→mechanism table now proves nothing spec-named is
  dropped; confidence folding and the MIXER=0 rationale are documented
  decisions, not silent inheritance.
- Structured reason objects with a closed enum are strictly better than
  the round-1 suffix-templated codes.

## 5. Required Amendments (round 2 — all one-to-three lines)

1. §3.3 + phase-04: hash check on claim-load precedes all state
   branches; mismatch → `409 IDEMPOTENCY_CONFLICT` always. (B1)
2. §3.4 + phase-02: single-block-membership rule for provider
   observations — no same-evidence contribution to both direct and
   other blocks. (B2)
3. §3.5 + phase-03: DISABLED providers excluded from the fold
   (reported in provider_status only). (B3)

---

No `PLAN_CONSENSUS_REACHED` — B1–B3 are small but load-bearing
clarifications on correctness paths; all other round-1 amendments are
confirmed landed and no redesign is needed.
