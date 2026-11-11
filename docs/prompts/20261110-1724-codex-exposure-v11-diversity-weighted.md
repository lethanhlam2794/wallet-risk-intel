# Prompt: codex — exposure-v11-diversity-weighted

> Created: 20261110-1724 | Target: codex | Template: M (agentic)
> Supersedes: `20261110-1607-codex-exposure-v11-amount-weighted.md` (model changed after operator review)

## Prompt

```
You are working in /home/lethanhlam/Desktop/Profile/check-wallet (Go + Postgres monolith, rules in AGENTS.md, spec docs/tailieu.md). All Go commands run from apps/api/.

## Context (carry forward)
- Scoring lives in internal/scoring/: policy.go (weights/constants), engine.go (score computation), model.go (Finding/verdict types). PolicyV1 = "v1.0.0" is the only version; every screening_results row stores scoring_policy_version and scores MUST stay reproducible per version.
- Exposure today: Finding Kind="exposure" scores ONLY when Basis == BasisProviderReported ("provider_reported") — hop1 → 10×0.7=7, hop2 → 5×0.7=4, exposure block capped at 20 via exposureCapV1. Formula: score = min(100, direct_max + min(20, exposure) + min(30, other)).
- transactions table holds indexed transfers: network_id, tx_hash, transfer_index, from_address, to_address, amount_raw (NUMERIC), asset identifier, block data, confirmation_status — written by queries/transactions.sql UpsertTransactions; counterparties fan out via BulkResolveWallets into wallets.
- Approved design (docs/exposure-scoring-v1.1-proposal.md — READ IT FIRST, it is the binding contract): v1.1 replaces flat hop weights with DISTINCT-RISKY-SOURCE weighting plus a frequency bonus — per_source = 30 + 7×(n−1) where n = number of SEPARATE confirmed meaningful inbound transfers on the same edge (R→X), NOT hop distance. "1 source, 6 tx" means six transfers from one address — a source is always a direct 1-hop sender; hop distance is retired as a scoring axis under v1.1 and only bounds the tracing enumeration (≤2) and chain-taint depth (1). Provider-reported-only sources flat +21; exposure block cap raised 20 → 70. The remaining 30-point "other" block is reserved for external platform risk signals (other providers' bans/flags with reasons).

## Task
Implement scoring policy v1.1 "diversity-weighted exposure" per the proposal — a NEW policy version selected via config, with v1 behavior byte-identical when selected.

## Design contract (v1.1)
1. internal/scoring/policy.go: add PolicyV1_1 = "v1.1.0" alongside PolicyV1 (untouched). New constants in one place: exposureCapV1_1 = 70, per-source base weight onchain=30, per-extra-meaningful-tx bonus=7, reported-only source flat=21, MEANINGFUL_TX_FLOOR_USD (default $100), static per-asset USD price table (majors: ETH, BNB, USDT, USDC, DAI — config-extensible allowlist), service classification list (EXCHANGE, GAMBLING, MIXER). v1 constants unchanged.
2. internal/scoring/model.go: add BasisOnchainComputed = "onchain_computed". Exposure findings carry optional fields: source_address, direction ("INBOUND"|"OUTBOUND"), amount/asset evidence, tx hashes, coverage ("complete"|"partial"), reason (e.g. "below_materiality_floor", "unpriced_asset"). New informational finding kinds: proceeds_receiver, possible_victim_contact — both weight 0.
3. Risky source rule: R is a source for candidate X iff (a) ≥1 confirmed R→X transfer priced ≥ floor AND (b) R is tainted — either direct-tainted (scored label set or active sanctions listing) or chain-tainted (R itself has ≥1 confirmed meaningful inbound from a direct-tainted source — depth exactly 1, computed at screening time from indexed txs + R's labels, never from another receiver). Service classifications are NOT tainted sources. Every source is by definition a 1-hop direct sender to X — Finding.Hops stays populated for evidence/traceability but is NOT a scoring input under v1.1.
4. Queries: add sqlc queries — (a) per (network_id, candidate): per from_address, count confirmed transfers to candidate priced ≥ floor and gather evidence (join price table via asset allowlist; amounts are NUMERIC — compare in integer/decimal-safe terms, no float money math); (b) receiver check: does address R have confirmed inbound ≥ floor from any direct-tainted address. Both bounded by the existing 2-hop tracing enumeration; exclude self-transfers, unconfirmed/reorg'd rows.
5. Screening: under v1.1, evaluate each enumerated counterparty as a potential source; per_source = 30 + 7×(n−1) with n = meaningful inbound count (below-floor transfers never increment n); dedupe by address across bases (highest basis wins); sum weights, cap 70; provider-only sources score flat 21; generic provider exposure signals without identified counterparty keep flat v1 weights inside the same block. Candidate carrying a service classification → all exposure findings informational (0 weight). OUTBOUND-only contact → possible_victim_contact informational finding, weight 0.
6. Scoring engine: exposure block = min(70, Σ per_source + generic provider signals); v1 paths inert. No amount/share tiers — this model deliberately has no share computation.
7. Config: SCORING_POLICY env (default "v1.0.0"); optional per-request policy_version for A/B. Unknown version → startup config error (fail-closed).

## MUST NOT
- Do not modify PolicyV1 weights, formula, or findings — v1 output identical before/after (golden regression test).
- Do not count the same source address twice (same source via multiple assets/bases counts once — frequency bonus counts separate transfers on that one edge, never hop distance).
- Do not use hop distance as a scoring input under v1.1 — hop-based weights exist only inside the v1 code path.
- Do not score OUTBOUND (candidate→risky) flows; do not auto-verify community/provider reports.
- Do not treat unpriced assets as meaningful — evidence-only finding, reason="unpriced_asset"; never fabricate value.
- Do not let partial/truncated index data read as complete — emit coverage="partial".
- Do not propagate chain-taint beyond depth 1 (receiver-of-receiver does not taint).
- No live price oracle / new providers / cross-chain / hops beyond bound / retroactive rescoring.
- No comments/renames/refactors outside this feature's scope.

## Failure/scenario requirements (acceptance criteria)
- 1 onchain-verified risky source, 1 meaningful tx → +30; 2 txs → +37; 6 txs → 65; 7+ txs → 70 (cap); 2 distinct sources 1 tx each → 60; 3 distinct sources → 70 (cap).
- 50 meaningful txs from one source count toward frequency but block caps at 70; below-floor txs never increment the count.
- Provider-reported-only source → +21 flat regardless of reported frequency; same source also onchain-verified → onchain formula not 51.
- Inbound < floor → evidence-only finding, weight 0, reason="below_materiality_floor" (dust attack produces no score and no frequency).
- Unpriced-asset-only inbound → evidence-only, reason="unpriced_asset".
- B (proceeds_receiver via direct-tainted A) → C receives meaningful inbound from B → C scores +30 via chain-taint; D receiving from C does NOT score via C.
- X → risky outbound only → possible_victim_contact informational, weight 0.
- Service-classified candidate (EXCHANGE/GAMBLING/MIXER) → all exposure informational, 0.
- Risky source with zero meaningful inbound to candidate → no source counted, explicit informational reason.
- SCORING_POLICY=v1.0.0 → output identical to baseline (golden test).
- Unknown SCORING_POLICY → startup config error.

## Tests
Table-driven unit tests beside source for: source dedupe (multi-asset/multi-base), frequency counting (only ≥floor tx increment; below-floor excluded), per-source formula boundaries (1/2/6/7 tx), floor boundaries (exactly at floor, below, unpriced), direction filter, direct vs chain-taint source classification, depth-1 propagation boundary, service-classified candidate, cap 70 saturation via both diversity (3 sources) and frequency (7 tx) paths, reported-only 21 path, v1/v1.1 parity and divergence. Reuse helpers from internal/scoring/*_test.go and transactions query test patterns.

## Verification gates (run all, report output)
cd apps/api && sqlc generate && gofmt -l . && go build ./... && go vet ./... && go test ./...

## Stop conditions — halt and report instead of guessing
- Any conflict with AGENTS.md domain invariants (indirect ≠ direct, unknown ≠ clean, reported ≠ verified).
- Proposal ambiguity that would change scoring semantics or the policy-version contract.
- A migration/schema decision not covered by the contract above.

After each major step output a one-line checkpoint. When done, report: files changed, tests run + results, v1 parity proof, and any deviation with reason.
```

## Notes for the operator

- Proposal file `docs/exposure-scoring-v1.1-proposal.md` must be reviewed/approved BEFORE running this prompt (§15 scoring-policy gate).
- Static price table values (ETH/BNB/etc.) need operator-confirmed numbers at implementation time — the prompt treats them as policy constants, not fetched data.
- After merge: flip `SCORING_POLICY=v1.1.0` only after the shadow-diff check in proposal §7.
