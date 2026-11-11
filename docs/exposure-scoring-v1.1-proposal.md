# Proposal: Diversity-Weighted Exposure — Scoring Policy v1.1

> Status: PROPOSED — pending §15 review (scoring policy change gate)
> Author: Devin | Date: 2026-11-10
> Supersedes: amount-weighted share-tier draft (same file, git history)
> Related: `internal/scoring/policy.go`, `internal/scoring/engine.go`, `internal/scoring/model.go`

## 1. Problem

Policy v1 scores indirect exposure **flat per hop** (hop-1 → 7, hop-2 → 4,
cap 20). Two defects:

1. A wallet receiving **95% of a HACK wallet's proceeds** scores the same
   **7** as one that received **$5 dust**.
2. The earlier amount-share fix is **gameable by splitting**: a risky
   source dividing proceeds across N wallets dilutes every individual
   share below the scoring tiers ($50M → 1,000 wallets ⇒ ~0.1% share ⇒
   0 points each). Coordinated fan-out defeats detection.

Operator requirement: model exposure the way investigators actually
convict laundering hubs — **diversity of independent dirty sources**.
One risky contact can be coincidence; N independent labeled sources
cannot be.

## 2. Constraints (invariants)

- **Indirect exposure never equals direct attribution.** Exposure stays
  in its own capped block. Operator-approved changes in this proposal:
  exposure cap 20 → **70** — two independent sources nearly saturate it;
  the remaining 30-point block (`other`) is reserved for external
  platform risk signals (other providers' bans/flags and their stated
  reasons). The exposure-only ceiling (70) touches the bottom of the
  direct band; combined with external corroboration the total can reach
  100 — acceptable because it requires BOTH deep multi-source onchain
  evidence AND independent platform flags, while finding kinds keep the
  direct/indirect distinction legible.
- Scores remain **reproducible by policy version** — v1 rows unchanged;
  v1.1 is a NEW version selected per request/config.
- `reported` stays `reported`; `unknown` never reads as clean.
- No new paid providers; no cross-chain; hops still ≤2.
- §15 gate: this document IS the review artifact — no implementation
  before approval.

## 3. Design — distinct-risky-source weighting

### 3.1 Risky source definition

Wallet **R** is a *risky source* for candidate **X** when **both**:

1. **Meaningful inbound**: at least one confirmed transfer `R → X` of
   value ≥ `MEANINGFUL_TX_FLOOR_USD` (default **$100**, policy
   constant), evaluated via the policy-pinned static price table
   (§3.3). Below-floor transfers produce evidence-only findings (0
   weight) — the dust filter.
2. **R is tainted**, either:
   - **direct-tainted**: R carries a scored direct label
     (SANCTIONED, HACK, RANSOMWARE, PHISHING, TERRORISM, SCAM, DRAINER,
     MONEY_LAUNDERING, DARKNET, EXTREMISM, FROZEN) or an active
     sanctions listing; or
   - **chain-tainted**: R is a `proceeds_receiver` (§3.5) — its taint
     derives from meaningful inbound from a *direct-tainted* source.
     Depth exactly 1: chain-taint never derives from another receiver.

Service classifications (EXCHANGE, GAMBLING, MIXER) are **not** tainted
sources — receiving from Binance is not exposure.

### 3.2 Scoring per distinct source + frequency bonus

```
per_source = 30 + 7 × (n − 1)
             n = count of confirmed meaningful inbound transfers from
                 that source (only ≥ floor transfers increment n)

exposure = Σ per_source over DISTINCT risky source addresses
             onchain-computed evidence → formula above (verified)
             provider-reported only    → flat +21 (no tx-level data to
               count frequency; reported stays reported)
           dedupe by source address across bases (highest basis wins)
           generic provider exposure signals without an identified
             counterparty keep flat v1 weights (7/4), folded into same block

score = min(100, direct_max + min(70, exposure) + min(30, other))
```

- **Diversity is the primary signal**: each distinct source starts at
  +30 — two independent sources reach 60; a third source, repeat
  activity, or a deep single-source channel completes the cap (70).
- **Frequency is the secondary signal**: repeat meaningful transfers
  from the same source add +7 each — an ongoing payment channel to a
  labeled wallet is stronger evidence than a single touch. One
  hyper-active source can also reach the cap alone (7 txs ⇒ 72 ⇒ 70),
  so "multiple independent sources" OR "one deep relationship" both
  max out.
- Multiple assets from the same R still one source; each meaningful
  tx counts once regardless of asset.
- Outbound `X → risky` flows do NOT score — emitted as an informational
  `possible_victim_contact` finding (weight 0): paying a scammer is
  evidence a partner may want, but victims are not flagged as risky.

### 3.3 Materiality floor — static policy price table

The floor is denominated in USD but requires no live oracle:

- Policy v1.1 pins a **static per-asset price table** (majors only:
  ETH, BNB, USDT, USDC, DAI + config-extensible allowlist), stored as
  versioned policy constants — deterministic, auditable, reproducible.
- **Unpriced assets**: materiality cannot be proven → evidence-only
  finding with `reason: "unpriced_asset"` (0 weight). We never
  fabricate value; the evidence is still recorded for the partner.
- Stale-price drift is bounded because the table is pinned **per policy
  version** — revisiting prices means a new version bump, not silent
  rescoring.

### 3.4 Service-classified candidates

If the candidate itself carries a service classification (EXCHANGE,
GAMBLING, MIXER), exposure findings are emitted **informational-only
(0 weight)**. Contact diversity is those entities' business model —
their risk must come from direct evidence, not adjacency. (The
classification list is a policy constant; reviewers may narrow it.)

### 3.5 `proceeds_receiver` flag + 1-depth propagation

- A wallet is flagged `proceeds_receiver` when it has ≥1 confirmed
  meaningful inbound transfer from a **direct-tainted** source.
- Emitted as an informational finding on that wallet's own screenings
  (asset, amount evidence, tx hashes, source address).
- When R appears as a source for candidate X, R's receiver status is
  **computed at screening time** from indexed transactions + R's own
  labels — deterministic, no dependency on R having been screened
  before. A denormalized flag may be persisted for performance but is
  never required for correctness.
- Depth bound: chain-taint derives only from direct-tainted sources —
  `A(labeled) → B(receiver) → C` scores C via B; it does not propagate
  further to D. Total reach stays within the 2-hop bound.

### 3.6 Versioning & selection

- `PolicyV1_1 = "v1.1.0"` registered beside `PolicyV1`; v1 code paths
  inert when v1.1 selected and vice versa.
- `SCORING_POLICY` env (default `v1.0.0`); optional per-request
  `policy_version` for A/B comparison. Unknown version → startup config
  error (fail-closed).

## 4. Edge cases (must be handled + tested)

- **Dust**: inbound < floor → evidence finding, 0 weight, `reason:
  "below_materiality_floor"` — and does NOT increment frequency count
  (dust cannot manufacture a fake recurring relationship).
- **Self-transfers** excluded from source detection and frequency count.
- **Unconfirmed / reorg'd** transfers excluded from both.
- **Partial index coverage** on the source or candidate → finding
  carries `coverage: "partial"` (honesty invariant — partial data never
  presented as complete).
- **Both directions** with same R: only inbound contributes.
- **Same source across bases** (onchain + provider-reported): counted
  once at the higher basis.
- **Multiple assets** from same source: still one source (+30).
- **Service-classified candidate**: all exposure informational.
- **Zero meaningful inbound**: no sources → no exposure score; explicit
  informational reason, not silent absence.
- **Chain depth**: receiver-of-receiver does not propagate.

## 5. Explicit non-goals

- No scoring of hop ≥3; no recursive propagation beyond depth 1.
- No cross-chain exposure.
- No change to direct label/sanctions/provider-score blocks.
- No retroactive rescoring — new scores on new screenings only.
- No live price oracle (static policy-pinned table only).
- No cluster/graph-pattern scoring (fan-out-signature detection is
  future work, v1.2+).

## 6. Risks

- **Multi-address source gaming**: an attacker could try to inflate a
  victim's score by sending from several labeled wallets — but labels
  are scarce (attacker cannot mint labels) and the $ floor makes each
  contributing transfer costly. Net effect: attacker burns labeled
  assets to flag strangers; acceptable.
- **Frequency griefing**: 7×$100 ≈ $700 fabricates a recurring-payment
  channel and pushes a victim to the 70 cap. Mitigation: findings carry
  per-source tx counts, so "one deep channel" vs "diverse sources" are
  distinguishable evidence even at equal scores; the floor keeps each
  manufactured signal costly.
- **Exposure + provider-score stacking**: exposure-only ceiling is 70;
  with max provider-score block (+30) a wallet could reach 100 = the
  sanctioned ceiling. Operator-accepted: reaching it requires BOTH
  multi-source onchain evidence AND independent external platform
  flags — convergent evidence, not adjacency alone. Finding kinds keep
  the direct/indirect distinction legible in every response.
- **Stale static prices**: ETH at $3K vs $10K shifts the floor's real
  value ~3× — bounded by policy-versioned table; noted limitation.
- **Compute cost**: per-candidate source-detection query over
  `transactions`, bounded by maxHops=2 and existing indexes; the
  1-depth receiver check adds one bounded lookup per candidate source.
  Validate with EXPLAIN on the ~1M-row table.

## 7. Rollout

1. §15 review of this document.
2. Implement behind `SCORING_POLICY` default `v1.0.0` — zero behavior
   change until flipped.
3. Shadow-run v1.1 vs v1 on a risky-label sample; diff findings; tune
   floor/tiers if misfiring.
4. Flip default; document; partners see `v1.1.0` on new screenings only.
