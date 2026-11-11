# Final Verification — scoring policy v1.1 "diversity-weighted exposure"

Date: 2026-11-11. Implementer: Devin Cook.

## Acceptance gates

```bash
cd apps/api && sqlc generate && gofmt -l . && go build ./... && go vet ./... && go test ./...
```

| Gate | Result |
|---|---|
| sqlc generate | PASS — 4 new queries generated into `internal/db` |
| gofmt -l . | PASS — prints nothing |
| go build ./... | PASS |
| go vet ./... | PASS |
| go test ./... | PASS — all packages green incl. integration (check_wallet_*_test DBs) |

## v1 frozen-contract proof

`internal/scoring/testdata/v1_golden.json` — 16-entry corpus generated from
the unmodified v1 engine. `TestGoldenV1` asserts
`EvaluateFor(fs, PolicyV1)` marshals byte-identical to the stored verdict for
every corpus entry (PASS).

## Phase evidence

- Phase 1: `PolicyV1_1` + `KnownPolicies`/`KnownPolicy` registry; `SCORING_POLICY`
  fail-closed in `config.Load`; `options.policy_version` → resolved policy →
  persisted `scoring_policy_version`; unknown request policy → 400 before
  request hash/claim (`TestHandler_UnknownPolicyVersion_400`); canonical-hash
  separation proven (`TestRequestHash_PolicyVersionSeparatesClaims`).
- Phase 2: sqlc `ListConfirmedCounterpartyTransfers` (direction-bounded,
  confirmed/non-error/non-self only), `ListConfirmedInboundToWallets`
  (receiver check), `ListWalletIndexStates`, `BulkMatchSanctionedAddresses`;
  pinned price table (ETH $3,000 / BNB $600 / stables $1), `IsMeaningfulV1_1`
  big.Int floor math (exact $100 boundary tested); `SCORING_ASSET_PRICES`
  fail-closed overlay parser. Integration tests seed
  confirmed/pending/error/self/NULL cases — all green.
- Phase 3: `internal/screening/exposure.go` analyzer (≤6 round trips:
  cand-in, cand-out, index-states, receiver-check, bulk labels, bulk
  sanctions); direct+one-level chain taint; receiver-check honesty
  (`sources_receiver_unchecked`); coverage `complete|partial|absent`;
  service-classified gate via `FindingSet.CandidateServiceClassified`
  (independent of include_labels); `proceeds_receiver` /
  `possible_victim_contact`; provider OUTBOUND gate (A1); Enqueuer seam
  heals absent coverage via warmindex.
- Phase 4: `EvaluateFor` dispatch (v1→frozen Evaluate, v1.1→evaluateV11,
  unknown→NOT_EVALUATED); winner rules incl. winner-zero suppression;
  30+7(n−1) onchain / flat-21 provider-source / v1 weights for generic;
  direction gate; 70 cap; service-gate zeroes exposure with explicit
  reason; informational kinds visible at weight 0; shuffle-determinism test.
- Phase 5: `api/openapi.yaml` — `options.policy_version` enum, scoring
  policy enum, v1.1 exposure item + coverage + reason-code fields;
  `.env.example` documents `SCORING_POLICY` and `SCORING_ASSET_PRICES`.

## New tests (all passing)

- scoring: policy registry/constants, price table + floor boundaries,
  overlay validation, engine v1.1 (dispatch, scoring, cap, dedupe
  winner-zero, direction gate, service gate, informational, v1-weight
  parity, shuffle determinism), golden v1 corpus.
- screening: analyzer fake-store suite (direct/chain/below-floor/unpriced/
  disputed/service/victim/coverage/error-degrade), query integration
  (3 queries), handler policy-version 400/accepted, hash separation.
- config: SCORING_POLICY default/accept/fail-closed cases.

## Remediation round 1 — finding → resolution

Review: `reports/review-round-1-findings.md` (CHANGES_REQUESTED). Every
behavioral fix landed test-first; test names cited.

| # | Sev | Resolution |
|---|-----|-----------|
| 1 | major | `engine_v11.go` — provider-source gate now `MaxHops>=1 && (f.Hops==0 || f.Hops<=fs.MaxHops)`; the unspec'd `Hops<=2` bound removed (B3: identification changes the weight, never the bound). Test: `TestV11_ProviderUnknownHopsAdjacency` (Hops==0 → +21; MaxHops=0 → 0). |
| 2 | major | `exposure.go` — `t.service → continue` removed from source and victim-contact emission; service labels never erase direct taint/sanctions. Tests: `TestAnalyze_ServiceClassifiedStillTainted` (HACK+MIXER source, sanctioned exchange source). |
| 3 | major | `exposure.go` — `ListWalletIndexStates` error tracked via `statesErr`; `onchain_coverage="absent"` now requires a successful probe with zero candidate rows. Test: `TestAnalyze_IndexStatesErrorIsPartial`. |
| 4 | major | Acceptance/service tests added — engine: `TestV11_SixTransfersOneSource` (+65), `TestV11_TwoOneTransferSources` (+60), `TestV11_ThreeSourcesCap70`, `TestV11_FiftyTransfersCap`, `TestV11_DedupeNot51`, `TestV11_MaxHopsZeroInformational`; analyzer: `TestAnalyze_CoveragePartialCases` (stale FreshTTL, NULL indexed_at, recv-leg error, 5000-row scan-limit, 256-source cap, 512-sender depth-2 cap); mapper: `TestMapper_ProviderOutboundIsVictimContact`; service: `TestService_V11_PersistedPolicyAbsentCoverage` (persisted v1.1.0, absent→PARTIAL, enqueue), `TestService_V1_EnvelopeParity` (A5). A/B divergence: `TestAB_Divergence` — identical findings → v1 exposure 7 vs v1.1 exposure 70 (see below). |
| 5 | minor | `pricing_v11.go` — overlay validation now rejects `micro_usd <= 0` (fail closed). Test: `micro_usd:0` case in `TestParseScoringAssetPrices`. |
| 6 | minor | `pricing_v11.go` + `cmd/api/main.go` — key-shape regex accepts any `<net>:` syntax; membership validated by new `ValidateScoringAssetPriceNetworks` against the resolver's active codes at wiring (A14). Test: `TestValidateScoringAssetPriceNetworks`. |
| 7 | minor | `engine_v11.go` — OUTBOUND findings route to the ungrouped informational path; they can never join a `(address, INBOUND)` group or suppress a sibling. Test: `TestV11_OutboundNeverJoinsGroup`. |
| 8 | minor | `engine_v11.go` — under `CandidateServiceClassified` each source reason keeps its own code at weight 0 and exactly one `SERVICE_CLASSIFIED_CANDIDATE` informational reason is emitted. Test: `TestV11_ServiceGateKeepsSourceCodes`. |
| 9 | minor | `mapper.go` — wire `direction` emits normalized `INBOUND`/`OUTBOUND` under v1.1; `openapi.yaml` documents the vocabulary and the stale "basis is always provider_reported" description is corrected. Test: `TestMapper_ProviderOutboundIsVictimContact`. |
| 10 | minor | `exposure.go` — sanctions hit now competes on base weight (`SANCTIONED`=85) instead of only filling an empty category. Test: `TestAnalyze_ServiceClassifiedStillTainted` fs3 case (SANCTIONED outranks FROZEN). |
| 11 | minor | `exposure.go` — deterministic ordering: labels/sanctions rows sorted before taint resolution, victim-contact counterparties sorted, depth-2 sender iteration sorted for equal-weight ties. Covered by `TestV11_ShuffleDeterminism` + analyzer suite. |
| 12 | minor | `mapper.go` — exposure dimension `Usable`/`DataStatus` now `providerUsable || (v1.1 && onchain.evaluated)`; a failed inbound scan sets `evaluated=false` so a broken leg can't masquerade as usable. Test: `TestMapper_OnchainLegMakesExposureUsable`. |
| 13 | nit | `mapper.go` — dead `gathered.candidate` field + `network` import removed. |
| 14 | nit | `policy_v11.go` — the four analyzer bounds exported (`ExposureScanLimitV1_1`, `ExposureMaxSourcesV1_1`, `ExposureMaxDepth2SendersV1_1`, `ExposureEvidenceTxsV1_1`); `exposure.go` uses them. |
| 15 | nit | `exposure.go` — `usableIndex` requires `IndexedAt.Valid` (NULL can never prove freshness); `freshTTL==0` keeps its "no staleness bound" semantics — documented: a fresh, non-truncated, timestamped row is usable; the NULL-timestamp hole is what was closed. Test: NULL-indexed_at case in `TestAnalyze_CoveragePartialCases`. |
| 16 | nit | `exposure.go` — `assetKey` returns `ok=false` for non-NATIVE rows with NULL `asset_identifier` → counted `unpriced`, never priced as the chain coin. Test: `TestAnalyze_NullAssetIDNotNative`. |
| 17 | nit | `exposure.go` — malformed-only sources report `reason="unassessable_amount"` (added to openapi enum), distinct from `unpriced_asset`. Test: `TestAnalyze_MalformedAmountReason`. |
| 18 | nit | `pricing_v11.go` — `ApplyScoringAssetPrices` no longer mutates the pinned policy table; overlays live in a separate consulted map, reset by nil. |
| 19 | nit | `docs/exposure-scoring-v1.1-proposal-vi.md` — Vietnamese translation of the plan's proposal kept intentionally as reviewer-accessible documentation of the scoring rationale; scope now accounted for here. |

### A/B divergence evidence (recorded for review)

Same input `FindingSet` — one on-chain source (`0xaaa`, 6 meaningful
transfers, direct SCAM taint) plus one named provider source (`0xbbb`,
hop 1, HACK) — evaluated under both policies
(`TestAB_Divergence`, `internal/scoring/engine_v11_test.go`):

| Policy | Exposure subtotal | Reason |
|---|---|---|
| v1.0.0 | **7** | provider hop-1 flat weight (10 × reported 0.7); on-chain basis opaque → 0 |
| v1.1.0 | **70** | 30+7·(6−1)=65 on-chain + 21 named provider source = 86 → capped at 70 |

Divergence is the point of the policy: repeated meaningful transfers
from one source and multiple independent sources are both represented,
where v1 saw only flat hop counts.

### Remediation gates (final run below)

Re-run of the full acceptance command after remediation: see the gate
table at top — all commands re-verified PASS on the remediated tree.

## Remediation round 2 — re-audit residuals

| # | Sev | Resolution |
|---|-----|-----------|
| R2-1 | major | `exposure.go` — service-classified receivers are excluded when building `chainTainted`: a service source may score via DIRECT taint/sanctions only; chain taint through a high-degree service (e.g. an exchange) can no longer mass-flag its recipients. Test: `TestAnalyze_ServiceSourceChainTaintExcluded` (chain-only service → no finding); `TestAnalyze_ServiceClassifiedStillTainted` retained (direct/sanctioned service still sources). |
| R2-2 | nit | `exposure.go` — non-source outbound counterparties with wallet IDs now join the same receiver-check; `possible_victim_contact` emits for direct OR chain taint (chain for non-service cps only, per §3.1 intent). A cp whose index is unusable is simply not chain-verified — the contact is never asserted. Test: `TestAnalyze_ChainTaintedVictimContact` (incl. service-only-cp exclusion case). |
| R2-3 | nit | `service_test.go` — `TestService_RequestPolicyOverrideBothDirections`: deployment v1.1 + request v1.0.0 → v1 verdict, persisted `scoring_policy_version="v1.0.0"`, no onchain leg; deployment v1.0.0 + request v1.1.0 → v1.1 verdict, persisted `"v1.1.0"`, absent→PARTIAL. |

## Stage 4 — BLIND/property saturation suite

Seed-driven invariant fuzzing (`BLIND_SEED`, default 1; ≥200 cases/run;
failures print seed + case index). Verified on seeds 1, 2, 7, 42, 99.

`internal/scoring/blind_test.go`:
- `TestBlind_V11_Invariants` — random FindingSets: score ∈ [0,100] or nil,
  exposure ∈ [0,70], deterministic re-evaluation, policy stamping,
  unknown policy fail-closed.
- `TestBlind_V1DispatchParity` — `EvaluateFor(·, v1)` ≡ frozen `Evaluate`
  on every random input.
- `TestBlind_V11_DedupeGroupWeight` — same-address groups contribute
  exactly the dedupe-order rank-winner's weight (winner+loser never
  summed). NOTE: caught that the representative is the first max-rank
  member in `dedupe()` order — the model replicates it.
- `TestBlind_V11_MonotonicMeaningfulCount` — n+1 transfers never
  decreases a tainted source's weight pre-cap.
- `TestBlind_V11_OutboundOnlyNeverScores` — all-OUTBOUND sets → exposure 0.
- `TestBlind_IsMeaningful_BoundaryFuzz` — random micro/decimals overlays;
  meaningfulness flips exactly at the computed big.Int threshold.

`internal/screening/blind_test.go`:
- `TestBlind_Analyzer_Invariants` — random tx graphs + labels/sanctions/
  index-state fixtures: coverage vocabulary + honesty (absent only on
  proven no-index; complete only on fully-usable inputs); sourcesEvaluated
  & sources_receiver_unchecked re-derived; exposure findings only for
  direct-or-chain tainted non-service-chain sources; MeaningfulCount
  exact; tx_hashes ⊆ confirmed meaningful; outbound-only cps never become
  sources; victim contacts need direct-or-(non-service) chain taint;
  deterministic double evaluation.
- Caught and fixed one fake-contract gap (not a production bug): the
  fake store now honors `WalletIds`/`Column2`/`Addresses` params on
  `ListConfirmedInboundToWallets` and `ListWalletIndexStates`, matching
  the generated query contract.
