Devin Cook — final task: build the BLIND/property test suite for Stage 4 saturation. These are randomized, seed-driven tests the reviewer never saw — they must probe invariants, not re-assert the named acceptance values.

Create `blind_test.go` files (scoring + screening packages, or one consolidated file per package following existing conventions) where each `TestBlind_*` reads `BLIND_SEED` env var (default e.g. 1), seeds `math/rand`, and generates N≥200 randomized cases per run asserting INVARIANTS:

Scoring engine (`internal/scoring/blind_test.go`):
- Random `FindingSet`s (random kinds/bases/categories/statuses/directions/hops/counts/shuffled orderings) → `EvaluateFor(fs, PolicyV1_1)`: score ∈ [0,100] or nil; exposure subtotal ∈ [0,70]; verdict deterministic across identical re-evaluation; `PolicyVersion` stamped correctly; unknown policy → nil score + UNKNOWN.
- Dedupe invariant: same address N random findings across bases → exposure contribution of that address ≤70 and never sums winner+loser weights.
- Monotonicity: adding a meaningful onchain transfer to a tainted source never decreases that source's weight (until cap).
- v1 parity: same random sets through `EvaluateFor(·, PolicyV1)` match the frozen `Evaluate` output exactly.
- Direction: no random set containing only OUTBOUND KindExposure findings ever produces exposure>0.
- big.Int boundary fuzz: amounts straddling the $100 floor × random decimals/prices → meaningfulness flips exactly at the boundary.

Screening analyzer (`internal/screening/blind_test.go`, fake `exposureStore`):
- Random transaction graphs (random counts of inbound/outbound/self/pending/error rows, amounts, asset keys, wallet IDs) + random label/sanction/index-state fixtures → invariants: coverage=="complete" only when no bound/state/error triggered; every scored source is direct-or-chain tainted AND non-service-chain (service via direct only); `MeaningfulCount` equals exact count of confirmed+inbound+≥floor transfers; `sources_receiver_unchecked` equals sources lacking usable state; no finding ever carries `coverage` other than complete|partial|absent; outbound-only counterparties never become sources; tx hashes in findings ⊆ actual confirmed meaningful tx hashes.

Keep tests fast (<2s per seed), table-printed failures with seed + case index for reproducibility. Do NOT change production code. Run `BLIND_SEED=7 go test ./internal/scoring/ ./internal/screening/ -run TestBlind -count=1` once to prove green, plus full `gofmt -l .`+build+vet. Report Status/Summary/Gates/Concerns.
