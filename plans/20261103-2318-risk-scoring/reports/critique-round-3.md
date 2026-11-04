# Devin Critic — Round 3: Risk Scoring & Screening API (Spec Phase 5)

**Verdict: ACCEPTED — consensus.** All three round-2 blockers verified
fixed, in the letter and in the mechanism. No remaining specification
gaps, no fatal flaws, no invariant violations detected. The plan is
implementable as written.

## 1. Round-2 Blocker Verification

| # | Finding | Status | Evidence |
|---|---|---|---|
| B1 | `request_hash` ordering | ✓ fixed | plan §3.3 L72-76 + phase-04 L28-30 — hash compared FIRST on claim-load, before all branches; `409 IDEMPOTENCY_CONFLICT` in every claim state (terminal/in-flight/stale). Takeover cannot erase a different payload's record; mismatched requests get an immediate 409 rather than a pointless 30 s poll followed by a wrong-body replay |
| B2 | Provider-observation double-block | ✓ fixed | plan §3.4 L123-126 + phase-02 L36-39 — explicit single-block-membership rule; provider observations score once via the direct-block category×verif×conf table; other block now holds only max `provider_score` band + future non-observation signals. Resolution is better than either alternative I offered — and `direct_max` (MAX not SUM) means even cross-source mirrors cannot stack |
| B3 | DISABLED in fold | ✓ fixed | plan §3.5 L167-169 + phase-03 L47-48 — `DISABLED` excluded from the status fold (configuration, not failure), mirrors `enrichment.aggregate`'s `attempted` skip (`internal/enrichment/service.go#L577`); still reported verbatim in `provider_status`. COMPLETED reachable in a default deployment |

Nits resolved: exposure weights clarified ("1-hop 10, 2-hop 5 — both ×
reported factor 0.7"); redundant `screening_requests(client_id)` index
removed with documented leftmost-prefix rationale (phase-01 L42-44).

## 2. Final Invariant Sweep (spec §1.2/§21 vs amended plan)

- **unknown ≠ safe**: fold keyed on `data_status`, never error — verified;
  INSUFFICIENT_DATA + `risk_score` NULL is a named test.
- **reported ≠ verified**: verif factor ×0.7/×0.5/×0; disputed → 0.
- **direct ≠ indirect**: provider exposures tagged
  `basis: provider_reported`, capped, filtered by `max_hops`, `hops>2`
  informational-only, never `transaction_evidence`.
- **chain-specific ≠ cross-chain**: cross-chain/unresolved listings stay
  informational (`CROSS_CHAIN_LISTING` reason, 0 weight).
- **provider failure ≠ clean**: typed statuses in `provider_status` +
  PARTIAL fold; DISABLED reported but excluded (correct semantics).
- **classification ≠ crime**: GAMBLING/EXCHANGE/MIXER 0-weight with
  documented MIXER rationale.
- **no double counting**: single-block-membership + direct-max + dedup
  keys + block caps.
- **reproducibility**: pure-function policy `v1.0.0`, deterministic
  ordering, version stored per row.
- **idempotency**: claim-first + hash-first + single-tx attach +
  WithoutCancel + takeover — state machine fully ordered; concurrent,
  crashed-owner, disconnect, and payload-mismatch paths all defined.
- **audit fidelity**: `result` column type `json` + explicit `screened_at`
  → byte-identical replay contract is honest.

## 3. Residual Implementation-Latitude Notes (non-blocking)

Recorded for the implementer's awareness — no plan text required:

- Exposure weight rounding (3.5 → 4 under `round()`) — pin in the
  property tests; the shared `round()` rule implies it.
- Claim-INSERT is a separate autocommitted write before the pipeline —
  correct by design (visibility to pollers); the ~30 s bound on the
  pipeline keeps the 5-min stale threshold a safe margin.
- On poll-timeout `409 IDEMPOTENCY_IN_FLIGHT`, a retry replays the same
  poll path — converges once the owner lands.

## 4. Sign-Off

Four phases, all spec files present, every collaborator verified against
source, 14 required amendments across two rounds all landed, contested
decisions ruled and documented. Ready for implementation per the phase
order — the invariant-critical tests in phase-03/04 (no-snapshots →
INSUFFICIENT_DATA; include_sanctions=false never reads clean; disconnect
→ terminal row) are the acceptance gate.

PLAN_CONSENSUS_REACHED
