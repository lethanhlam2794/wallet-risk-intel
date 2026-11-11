# Feature Map & Guidebook — Scoring Policy v1.1 "Diversity-Weighted Exposure" (LIVE)

**Scope**: second registered scoring policy `v1.1.0` — exposure scoring now
weights *distinct risky sources* (30 base + 7 per additional meaningful
transfer, cap 70) instead of flat hop weights, backed by bounded on-chain
evidence computed from indexed `transactions`. `v1.0.0` is **frozen** —
byte-identical verdicts when selected (16-entry golden corpus proves it).

- Plan + debate trail: `plans/20261111-1131-scoring-v11-diversity-exposure/`
- Contract: `docs/prompts/20261110-1724-codex-exposure-v11-diversity-weighted.md`
- Live evidence: `plans/20261111-1131-scoring-v11-diversity-exposure/reports/stage5/`
- Pipeline evidence: `docs/pipeline-log.md` (all 6 stages)

## 1. What was delivered

| Deliverable | Where | State |
|---|---|---|
| `PolicyV1_1="v1.1.0"` + registry (`KnownPolicy`, `EvaluateFor`) | `internal/scoring/policy_v11.go`, `policy.go` | live |
| v1.1 engine (`evaluateV11`): direction gate, source-group dedupe (onchain>provider, winner-zero suppression), 30+7(n−1)/cap70, flat-21 provider-source, service-candidate suppression | `internal/scoring/engine_v11.go` | live |
| Static price table (`(network,asset)`→micro-USD, big.Int materiality @ $100 floor) + `SCORING_ASSET_PRICES` JSON overlay (fail-closed, `micro_usd>0`, active-network keys) | `internal/scoring/pricing_v11.go`, `config.go` | live |
| On-chain exposure analyzer: inbound grouping, direct+chain taint (depth-1 only, services direct-only), coverage honesty (`complete\|partial\|absent` + `sources_receiver_unchecked`), `proceeds_receiver`/`possible_victim_contact` | `internal/screening/exposure.go` | live |
| 4 sqlc queries (counterparty scans ×2 direction, receiver-check bulk, index-state bulk, sanctions bulk) | `queries/{transactions,sanctions}.sql` → `internal/db/` | live |
| Request `options.policy_version` (validated 400, idempotency-hash-bearing), persisted `scoring_policy_version` incl. failure paths | `screening/{handler,service,model}.go` | live |
| OpenAPI: policy enums, `ScreeningExposure`/`ScreeningCoverage` fields, normalized direction vocab, reason codes | `api/openapi.yaml` | live |
| ~50 named tests + 7 `TestBlind_*` (200 cases/seed) + 16-entry v1 golden corpus | `internal/{scoring,screening}/*_test.go`, `scoring/testdata/` | green |

## 2. Source paths

```
apps/api/internal/scoring/
  policy_v11.go     PolicyV1_1, KnownPolicy/EvaluateFor dispatch, constants
                    (cap 70, base 30, freq 7, provider 21, floor $100,
                    taint/service category sets), bounds (scan 5000,
                    sources 256, depth-2 senders 512, evidence txs 10)
  pricing_v11.go    PriceTableV1_1 {ETH,BNB,USDT,USDC,DAI} pinned micro-USD;
                    IsMeaningfulV1_1 big.Int floor compare; overlay parse
  engine_v11.go     evaluateV11 exposure fold — divergent only there;
                    shared v1 helpers untouched
  model.go          +BasisOnchainComputed, Direction, SourceAddress,
                    MeaningfulCount, TxHashes, TaintKind, Coverage, Reason,
                    CandidateServiceClassified (json:"-"), new kinds/reasons
apps/api/internal/screening/
  exposure.go       exposureAnalyzer (6 bounded round-trips): scans,
                    merged bulk label+sanctions match, index-state bulk,
                    coverage verdict, finding+evidence emission
  mapper.go         foldExposure direction normalization (provider
                    OUTBOUND→possible_victim_contact), onchain merge,
                    exposure-dimension usability
  service.go        Deps.{Policy,PriceTable,FreshTTL,Enqueuer}, analyzer
                    as 4th errgroup leg, resolvePolicy, persist stamped
  handler.go        policy_version validation → 400 pre-claim
apps/api/queries/   transactions.sql +scan queries; sanctions.sql +bulk match
apps/api/cmd/api/main.go  wiring (Policy, PriceTable overlay validation,
                          FreshTTL, Enqueuer)
```

## 3. Test anchors

| Behavior | Anchor |
|---|---|
| Acceptance values (30/37/65/70cap/60/3src-cap/21/≠51) | `TestV11_OnchainSourceScoring`, `…_SixTransfersOneSource`, `…_TwoOneTransferSources`, `…_ThreeSourcesCap70`, `…_FiftyTransfersCap`, `…_ProviderSourceFlat21`, `…_DedupeNot51` |
| Direction gate | `TestV11_OutboundNeverScores`, `…_OutboundNeverJoinsGroup`, `TestMapper_ProviderOutboundIsVictimContact` |
| MaxHops/hops bounds | `TestV11_MaxHopsZeroInformational`, `…_ProviderUnknownHopsAdjacency` |
| Service rules (candidate + source + direct-only-via-chain) | `TestV11_ServiceClassifiedGate`, `…_ServiceGateKeepsSourceCodes`, `TestAnalyze_ServiceClassifiedStillTainted`, `…_ServiceSourceChainTaintExcluded` |
| Coverage honesty | `TestAnalyze_CoverageAbsentAndUnchecked`, `…_CoveragePartialCases`, `…_IndexStatesErrorIsPartial`, `…_QueryErrorDegrades` |
| Taint rules (chain depth-1, disputed excluded, victim contacts) | `TestAnalyze_ChainTaintOneLevel`, `…_DisputedAndService`, `…_ChainTaintedVictimContact` |
| v1 freeze | `golden_test.go` (`testdata/v1_golden.json`), `TestBlind_V1DispatchParity`, `TestService_V1_EnvelopeParity` |
| A/B divergence | `TestAB_Divergence` (identical input: v1 exposure 7 vs v1.1 70) |
| Policy plumbing | `TestEvaluateFor_Dispatch`, `TestService_RequestPolicyOverrideBothDirections`, `…_V11_PersistedPolicyAbsentCoverage`, `TestHandler_UnknownPolicyVersion_400`, `TestRequestHash_PolicyVersionSeparatesClaims` |
| Config fail-closed | `internal/config/config_test.go` (unknown `SCORING_POLICY`, malformed overlay) |
| Blind/property | `internal/scoring/blind_test.go` ×6, `internal/screening/blind_test.go` ×1 — `BLIND_SEED`-driven, 200 cases/seed |

## 4. Reproduce

```bash
cd apps/api
make deps-up && make migrate-test          # stack on :5433/:6380
sqlc generate && gofmt -l . && go build ./... && go vet ./... && go test ./...
# blind saturation:
BLIND_SEED=<n> go test ./internal/scoring/ ./internal/screening/ -run TestBlind -count=1
# live API (fixture documented in reports/stage5-surface-verification.md):
SCORING_POLICY=v1.1.0 go run ./cmd/api     # :8010
curl -X POST localhost:8010/api/v1/screenings -H "X-API-Key: <key>" \
  -d '{"network":"ethereum","address":"0x1111…1111","options":{"include_exposure":true,"policy_version":"v1.1.0"}}'
```

## 5. DB verification (observed on dev :5433)

```sql
SELECT scoring_policy_version, risk_score, status FROM screening_results;   -- v1.1.0/60/PARTIAL, v1.0.0/0
SELECT jsonb->'scoring'->>'policy_version' FROM screening_requests;         -- per-request policy persisted
```

## 6. Rollout notes

- `SCORING_POLICY` (default `v1.0.0`, fail-closed unknown→boot error);
  per-request `options.policy_version` overrides, rides idempotency hash.
- `SCORING_ASSET_PRICES` JSON `{"net:asset":micro_usd}` merges over pinned
  table; `micro_usd≤0`/unknown network/malformed → boot error. EVM-only
  (transactions schema is EVM); static prices = reproducibility constraint.
- Coverage semantics: `absent` = proven no index row (→ PARTIAL screening +
  warm-queue enqueue); `partial` = stale/truncated/error/unverifiable
  receiver checks/bounds hit; `complete` only when all checks usable.
- §15 basis: proposal `PROPOSED→approved` via operator issuance of
  implementation prompt `20261110-1724` (recorded in plan.md).
- Deferred nits (review round 3, informational): unusable-index
  victim-contact counterparty under-reports coverage signal; round-trip
  bound test asserts ≤8 vs budget ≤6.
```
