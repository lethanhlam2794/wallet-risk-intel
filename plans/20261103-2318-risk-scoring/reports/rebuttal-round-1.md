Codex counter-analysis — Critic round 1, plan risk-scoring.

Verdict received: CHALLENGED with 11 required amendments. Assessment: all 11 are VALID — no false premises, no defenses needed. All have been applied to `plans/20261103-2318-risk-scoring/plan.md` and the four phase files (rewrite, not patch). Amendment-by-amendment disposition:

1. **Idempotency claim protocol — adopted, your recommended design.** §3.3 now specifies: claim-first INSERT (result_id NULL), pipeline under `context.WithoutCancel` + own deadline, result INSERT + claim UPDATE in ONE transaction; conflict loser: `result_id` set → 200 + stored body + `Idempotent-Replay: true`; NULL + claimed_at fresh (<5min) → bounded poll (250ms × ≤30s) → terminal replay or `409 IDEMPOTENCY_IN_FLIGHT`; NULL + stale (≥5min) → takeover DELETE+re-claim. `request_hash` = sha256 of canonical request (resolved network, normalized address, defaulted options) — never raw body bytes.

2. **Phase 1 constraints.** `idempotency_key` CHECK `char_length BETWEEN 1 AND 255` + handler charset `^[A-Za-z0-9._~-]+$`. `screening_results.result` column is type **`json`** (not jsonb) — byte-faithful replay preserved; GET contract = byte-identical stored body. `request` stays `jsonb` (hash computed pre-storage). **PENDING removed from persisted vocabulary** — `screening_requests.screening_result_id IS NULL` expresses in-flight; `screening_status` CHECK keeps spec's four values only.

3. **Availability fold** now explicit (§3.5): AVAILABLE→usable, STALE→usable-degraded, UNAVAILABLE→not-usable, NOT_REQUESTED→excluded — keyed on data_status never error. No-snapshots → INSUFFICIENT_DATA + NULL score is success criterion + required test #3.

4. **Label collection**: `ListLabels` cursor loop to exhaustion, hard cap 1000 + `truncated` marker in data_completeness — no silent 200-truncation.

5. **All 14 §9.7 fields** enumerated in phase-03 model incl. `evidence` — defined as flattened `{kind, ref}` list (wallet_labels.id, sanctions_entities.id/sanctions_wallets.id, provider observation {source, fetched_at}; cross-chain listings as informational entries).

6. **include_*/max_hops matrix** (§3.6): `evaluated:false` markers never empty-as-clean; provider fan-out always runs (feeds provider_status + other block); all-include-false legal → providers-only screening → possibly INSUFFICIENT_DATA; defaults include_*=true, max_hops=2, refresh=false; max_hops reject-not-clamp.

7. **Detached context** adopted: `context.WithoutCancel` + bounded deadline (§3.2).

8. **Phase-02 spec gaps closed** (§3.4): dimension→mechanism table for all seven §10.1 dims; **MIXER=0 deliberate** (spec §7.11 SERVICE_TYPE; sanctioned mixers still caught by OFAC dimension; documented for future revision); **confidence folded**: high 1.0/medium 0.9/low 0.8/unknown 0.7 applied to labels AND provider observations (OFAC direct flat 100); `weight = round(base × verif × conf)`; dedup key `{kind}:{category}:{source}:{ref}` with cross-source mirrors distinct-by-design + caps bounding residual stacking; provider observation category→category_type resolved via `risk_categories` DB truth (unknown → OTHER 0); provider SANCTIONED observations score via same category table (no special-casing); multiple provider_scores → **max** band contribution only; precedence fixed `min(100, direct_max + min(20,exposure) + min(30,other))`; reason codes are structured objects with a closed `code` enum (no suffix parsing).

9. **Exposure conditions** all adopted: `basis: "provider_reported"` on every item, `hops > 2` informational-only never scored, filtered by `max_hops`, never under `transaction_evidence`, coverage block reports `tracing: NOT_IMPLEMENTED` + `max_hops_requested` (+`disabled_by_request`).

10. **§17 metrics added** (§3.9 + phase-03/04): `screening_requests_total{status}`, `screening_duration_seconds`, `screening_partial_total`, `screening_id` log field.

11. **Test list**: your §4.4 list #1–#10 is now verbatim in phase-04 success criteria; invariant-critical cases also in phase-03.

Additional resolutions from your §4.3 edge-case list: screened_at explicit at insert; FAILED-row boundary (persistable only post-wallet-resolution; pre-resolution DB failure → 500 no row) documented; GET results non-private noted; retention deferred noted; sync-latency + >30s client-timeout guidance into openapi phase.

Your conditional acceptances are honored: openapi documents the sync-201 deviation; `Location` header on 201; `wallet:screening` key builder kept for §14 naming parity but unused (your "safer design" agreement); provider exposures keep all three conditions.

Re-evaluate the amended plan at `plans/20261103-2318-risk-scoring/plan.md` + `phase-0{1..4}-*.md`. If every amendment lands and no new fatal flaw is visible, emit `PLAN_CONSENSUS_REACHED`; otherwise list only remaining blockers. Write your re-review to `reports/critique-round-2.md` (only file you may write; read-only elsewhere — tree hash will be verified).

End your final reply with:
Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
Summary: one or two sentences
Concerns/Blockers: optional
