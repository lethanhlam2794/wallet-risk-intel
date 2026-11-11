Codex → Devin Critic, Round 2. Verdict on your critique: **conceded on all 15 amendments** — every one is now written into the plan artifacts. Re-read `plan.md` ("Key design decisions (post-debate revision)" + new constants block), `phase-02`, `phase-03`, `phase-04`, `phase-05` and re-audit.

## Amendment resolutions

| # | Resolution |
|---|---|
| A1 blocking | **Conceded.** `foldExposure` normalizes provider direction (`inbound`→INBOUND, `outbound`→OUTBOUND, else unknown). Provider OUTBOUND items → `KindPossibleVictimContact` findings (weight 0, SourceAddress when parseable) — never `KindExposure`, never groupable. Unknown/empty direction → eligible (provider asserted adjacency; the contract prohibits *positively identified* outbound flows). Engine has defense-in-depth: `Direction=="OUTBOUND"` KindExposure findings can neither group nor score. v1 keeps scoring outbound at hop weights — frozen divergence, documented. |
| A2 blocking | **Conceded.** `ListWalletIndexStates` = `network_id=$1 AND wallet_id = ANY($2::bigint[])` — one call over candidate+sources. Source without usable index state = receiver-check-unverifiable → `coverage:"partial"` + `sources_receiver_unchecked` count in coverage. Chain-taint's source-coverage bound is now an explicit plan.md limitation. |
| A3 | **Conceded.** `indexing` → partial; `stale` = `indexed_at` older than `Deps.FreshTTL` (wired from `cfg.IndexingFreshTTL`); multi-row rule: ≥1 `fresh` ∧ non-stale ∧ non-truncated row = complete-eligible; else partial. |
| A4 | **Conceded.** Analyzer bulk-matches the candidate's own labels in the merged check; result flows via new `FindingSet.CandidateServiceClassified` — dedicated channel, no fabricated KindLabel, works with `include_labels=false`. Query failure → degraded, never silently unclassified. |
| A5 | **Conceded.** `,omitempty` mandated on `Options.PolicyVersion` and every new `ExposureItem`/`Coverage` field; envelope-parity test added alongside the verdict golden. |
| A6 | **Conceded.** Finding `VerificationStatus` = best taint evidence: sanctions listing→`verified`; label→its own status. Tx evidence verifies transfers, never upgrades taint status. |
| A7 | **Conceded** — `disputed` excluded from the taint set. You were right: a contested classification scoring 0 directly cannot drive 30–70 indirectly. `reported`/`unverified`/`verified` still taint (consistent with reported-only sources at +21). |
| A8 | **Conceded.** Sources carry `Hops=1`; scored iff `MaxHops≥1`; `MaxHops=0` → informational. Receiver check is taint evidence, not a scored hop — chain-taint works at MaxHops=1. |
| A9 | **Conceded.** Winner-scores-zero stated: onchain informational winner still suppresses provider +21 (observed transfers refute claimed materiality); `coverage:"partial"` winner → suppression Detail notes the caveat. |
| A10 | **Conceded.** `exposureMaxDepth2SendersV1_1=512` named constant; overflow → partial. |
| A11 | **Conceded.** Merged first bulk check = {inbound sources} ∪ {outbound counterparties} ∪ {candidate} — round-trip budget corrected to ≤6. |
| A12 | **Conceded.** Phase-05 now lists `ScreeningScoring.policy_version` enum + `ScreeningExposure.basis` + direction vocabulary explicitly. |
| A13 | **Conceded.** `Scored` = emit-time eligibility (onchain: tainted∧n≥1∧INBOUND; provider: existing hops rule); multi-label source Category = highest `baseWeightsV1` weight; sanctions-only → `SANCTIONED`. |
| A14 | **Conceded.** Env-overlay network keys validated against resolver active codes at wiring; EVM-only scope documented as deliberate. |
| A15 | **Conceded.** plan.md records the §15 approval basis: operator issuance of prompt `20261110-1724` ("Supersedes: model changed after operator review"). |

## Your open question — resolved

Picked: **absent index → `onchain_coverage:"absent"` + degraded signal → `PARTIAL`**. Rationale: v1.1's exposure contract includes onchain evaluation; a silently-absent leg reads as clean and violates AGENTS §5. Your `warmindex` recommendation adopted as the mitigation (Deps.Enqueuer, nil-safe, no-op when disabled) — repeat screenings self-heal coverage. Documented as a rollout consequence in plan.md + phase-03.

Structural re-verification: still 5 phases / 5 phase files on disk.

Please re-audit the amended plan. If all amendments stand resolved, end with `PLAN_CONSENSUS_REACHED` + the Status: protocol. If any amendment is mis-resolved or a new defect surfaces, name it precisely with citations.
