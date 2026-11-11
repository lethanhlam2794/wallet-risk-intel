---
phase: 3
title: "Onchain exposure analyzer + screening/mapper wiring"
status: done
priority: P1
dependencies: [1, 2]
---

# Phase 3: Onchain exposure analyzer + screening/mapper wiring

## Overview

Build the screening-side analyzer that turns indexed `transactions`
into `BasisOnchainComputed` exposure findings: per-source meaningful-
inbound counting, direct/chain taint resolution, outbound victim-contact
detection, and coverage honesty — then wire it into the service fold so
v1.1 screenings get onchain evidence without touching v1 paths.

## Requirements

- Functional (contract §3/§5, prompt §4–5):
  - Runs **only** when `include_exposure` AND resolved policy = v1.1.
    Indexed data only — never calls `EnsureIndexed` or any provider.
  - Inbound scan for candidate → group by `from_wallet_id`/`from_address`
    → per-source: all confirmed txs, meaningful count `n` (≥floor),
    below-floor list, unpriced-asset list, tx evidence
    (bounded `TxHashes`, first-K meaningful, K=10 policy constant).
  - **Merged taint check (A11)**: ONE `BulkMatchLabels` +
    ONE `BulkMatchSanctionedAddresses` call over the UNION of
    {inbound source addrs} ∪ {outbound counterparty addrs} ∪
    {candidate's own address} — the outbound counterparties feed
    `possible_victim_contact` classification and the candidate's own
    labels feed the service-classification gate (A4), all inside the
    same bounded round-trip budget (≤6, enumerated below).
  - Direct-taint = `BulkMatchLabels` rows whose category ∈
    `directTaintCategoriesV1_1` AND `verification_status != 'disputed'`
    (A7 — a contested classification never drives a 30–70-point block;
    `reported`/`unverified`/`verified` all taint) ∪
    `BulkMatchSanctionedAddresses` hits (sanctions → `verified`).
  - Chain-taint = source R has ≥1 confirmed meaningful inbound from a
    DIRECT-tainted address — one bounded scan
    `direction='in', ids = all source wallet_ids`; depth-2 senders get
    ONE bulk taint check evaluated as DIRECT-ONLY (a chain-tainted
    intermediary's senders never confer taint). Depth exactly 1.
    Depth-2 sender set bounded by `exposureMaxDepth2SendersV1_1=512`
    (A10); overflow → coverage partial.
  - **Receiver-check honesty (A2, blocking)**: `ListWalletIndexStates`
    covers candidate + all source wallet_ids. A source with NO usable
    index state = receiver-check-unverifiable: it still scores via
    direct taint, but its chain-taint verdict is unknown →
    `coverage:"partial"` + surfaced `sources_receiver_unchecked` count.
    Chain-taint detection is bounded by source index coverage —
    documented rollout limitation.
  - **Service-classified candidate gate (A4)**: analyzer bulk-matches
    the CANDIDATE's own labels; candidate carrying a NON-DISPUTED
    `serviceClassificationsV1_1` label → `FindingSet.CandidateServiceClassified
    = true` — a dedicated engine-input channel, independent of
    `include_labels`, never a fabricated `KindLabel`. Disputed service
    labels do NOT suppress (symmetric with A7: a contested
    classification cannot gate 30–70 points of real exposure
    evidence). On analyzer query failure the classification is
    UNKNOWN → degraded signal, never silently "unclassified".
  - Emitted findings per source R for candidate X:
    - tainted ∧ n≥1 → `KindExposure`, `Basis=onchain_computed`,
      `SourceAddress=R`, `Direction="INBOUND"`, `Hops=1`,
      `MeaningfulCount=n`, `TxHashes` (bounded),
      `TaintKind="direct"|"chain"`, `Coverage=…`,
      `VerificationStatus` = BEST taint-evidence status (A6: sanctions
      listing → `verified`; label-only → that label's own status —
      never inflated; tx evidence verifies the transfers, not the
      taint). `Category` = the highest-`baseWeightsV1`-weight taint
      label when the source holds several (deterministic rule, A13);
      sanctions-only → `SANCTIONED`.
    - tainted ∧ n=0 ∧ priced-but-below-floor txs → same shape,
      `MeaningfulCount=0`, `Reason="below_materiality_floor"`.
    - tainted ∧ only unpriced-asset txs → `Reason="unpriced_asset"`.
    - untainted → no exposure finding (not evidence of risk).
  - Candidate `proceeds_receiver`: ≥1 meaningful inbound from a
    DIRECT-tainted source → `KindProceedsReceiver` finding w/ evidence
    (source address, amount, asset, tx hashes). Chain-tainted inbound
    alone does NOT raise it (receiver-of-receiver does not propagate).
  - Outbound scan (`direction='out'`) → counterparties that are
    tainted ∧ NOT already an inbound source →
    `KindPossibleVictimContact` finding (weight 0, `Direction=OUTBOUND`).
  - **Provider-item direction gate (A1, blocking)**: in `foldExposure`,
    provider `e.Direction` normalized: `inbound`→`INBOUND`,
    `outbound`→`OUTBOUND`, else → unknown. Provider items with
    direction `OUTBOUND` → `KindPossibleVictimContact` findings
    (SourceAddress set when `wallet.NormalizeEVM` succeeds) — the
    contract's outbound prohibition verified: PublicAML, the only
    emitting provider, emits `direction:"outbound"` items with
    parseable EVM counterparties; without this gate they'd score +21.
    `INBOUND`/unknown-direction items with a parseable `Counterparty` →
    `KindExposure` finding carrying `SourceAddress` (dedupe with
    onchain at engine); non-address counterparties → generic
    v1-weight signal, unchanged. Deliberate divergence: v1 keeps
    scoring these at hop weights regardless of direction.
  - **`Coverage` per finding (A3)** — `"partial"` when ANY of:
    candidate index-state set has no `fresh` non-stale non-truncated
    row (zero usable rows / all `not_indexed|failed|disabled|indexing`),
    candidate's multi-row set is only `partial`/`history_truncated`/
    stale (`stale` = `indexed_at` older than `cfg.IndexingFreshTTL` —
    wired into `Deps`), any scan hit its LIMIT, sources were skipped by
    `exposureMaxSourcesV1_1`, receiver-check sources lacked index state,
    or depth-2 senders overflowed their bound. Else `"complete"`.
    Multi-row resolution rule (transactions carry no `source_id`):
    usable = union across sources — ≥1 `fresh` ∧ non-stale ∧
    non-truncated row ⇒ complete-eligible; `indexing` alone never
    completes. Exposures block coverage gains `onchain_coverage`
    (`complete|partial|absent`) + `sources_evaluated` +
    `sources_receiver_unchecked` — ALL `,omitempty` (A5).
  - **Rollout consequence (documented)**: candidate with NO usable
    index coverage under v1.1 → exposure dimension keeps provider-leg
    usability but reports `onchain_coverage:"absent"` AND contributes a
    degraded signal → status `PARTIAL`. Chosen deliberately over
    "AVAILABLE+absent, no degraded": v1.1's exposure contract includes
    onchain evaluation and silent absence would read as clean (AGENTS
    §5). Mitigation: when the candidate lacks index coverage, the
    service enqueues it onto `warmindex.Queue` via a `Deps.Enqueuer`
    seam (no-op when `WARM_INDEX_ENABLED=false` — turns the coverage
    footgun into self-healing coverage for repeat screenings).
  - `ExposureItem.Scored` under v1.1 (A13) = emit-time eligibility:
    onchain items `tainted ∧ n≥1 ∧ INBOUND`; provider items keep the
    existing hops rule. Dedupe losers (engine-side) stay listed with
    weight-0 informational reasons.
  - Bounds (policy constants): `exposureScanLimitV1_1 = 5000` rows per
    scan; `exposureMaxSourcesV1_1 = 256` distinct sources evaluated;
    `exposureMaxDepth2SendersV1_1 = 512`; `exposureEvidenceTxsV1_1 = 10`
    tx hashes per finding. Deterministic skip order (ORDER BY of the
    scan); every bound hit → coverage partial.
- Non-functional:
  - All analyzer IO via one seam interface (`exposureStore`) so unit
    tests fake it; `*db.Queries` satisfies it.
  - Round-trip budget per screening: ≤6 (candidate inbound scan,
    outbound scan, receiver-check scan, merged bulk label check, bulk
    sanctions check, `ANY`-array index-state read) — updated from the
    original "≤4" claim after the A2/A11 corrections.
  - Everything under the existing 30 s `Deadline`; ctx respected.
  - Any analyzer query error → that leg degrades honestly
    (`onchain_coverage:"partial"` — "error" is NOT a distinct enum
    value; cannot-determine ≈ partial, while `absent` asserts a proven
    no-index condition), never silently proceeds.

## Architecture

New file `internal/screening/exposure.go`:

```go
type exposureStore interface {
    ListConfirmedCounterpartyTransfers(ctx, ListConfirmedCounterpartyTransfersParams) ([]…, error)
    BulkMatchLabels(ctx, BulkMatchLabelsParams) ([]…, error)
    BulkMatchSanctionedAddresses(ctx, BulkMatchSanctionedAddressesParams) ([]…, error)
    ListWalletIndexStates(ctx, ListWalletIndexStatesParams) ([]db.WalletIndexState, error)
}

type exposureAnalyzer struct {
    store    exposureStore
    prices   scoring.PriceTableV1_1
    freshTTL time.Duration // staleness horizon for index-state rows (A3)
}
```

`analyze(ctx, net, walletID, normalizedAddr) → onchainExposure{findings,
evidenceItems, coverage, serviceClassified, sourcesEvaluated,
receiverUnchecked, evaluated}` — pure DB-in, structs-out; the
mapper merges into `blk.Items` + `m.findings` + `m.evidence` and sets
`fs.CandidateServiceClassified`.

`service.go`: `Deps.PriceTable` (nil → `scoring.DefaultPriceTableV1_1()`),
`Deps.FreshTTL` (index staleness horizon = `cfg.IndexingFreshTTL`),
`Deps.Enqueuer` (warmindex seam, nil-safe); analyzer invoked as a 4th
errgroup leg under v1.1 (taint checks are self-contained DB queries —
independent of other legs, shares the deadline), storing into
`gathered.onchain`, then `mapGathered` folds it.

`model.go`: `ExposureItem` additive fields — ALL `,omitempty` (A5):
`SourceAddress`, `MeaningfulCount`, `TxHashes`, `Coverage`, `Reason`,
`TaintKind`; `Coverage` struct additive `OnchainCoverage string`,
`SourcesEvaluated int`, `SourcesReceiverUnchecked int` — all
`,omitempty`. `FindingSet.CandidateServiceClassified bool` — engine
input only, never serialized into the findings JSONB column.

`persistFailed` untouched semantically (already stamped in phase 1).

## Related Code Files

- Create: `apps/api/internal/screening/exposure.go`,
  `apps/api/internal/screening/exposure_test.go`
- Modify: `apps/api/internal/screening/service.go` (Deps + errgroup leg),
  `apps/api/internal/screening/mapper.go` (foldOnchain + provider-item
  SourceAddress normalization under v1.1),
  `apps/api/internal/screening/model.go` (additive response fields),
  `apps/api/cmd/api/main.go` (PriceTable wiring from cfg)

## Implementation Steps (TDD)

1. Red: `exposure_test.go` with fake store — table-driven:
   - 1 tainted source 1 meaningful tx → onchain finding n=1 direct;
   - 6 meaningful txs → n=6; below-floor excluded from n but present as
     evidence reason; unpriced-only → `unpriced_asset`;
   - chain-taint: S(direct)→R→X produces R chain-tainted source;
     receiver-of-receiver does NOT propagate;
   - source with no index-state row → receiver-check skipped, coverage
     partial, `sources_receiver_unchecked` incremented (A2);
   - `disputed`-only label → NOT tainted (A7); `reported` label →
     tainted, finding VerificationStatus="reported" not "verified" (A6);
   - multi-label source → Category = highest baseWeightsV1 weight (A13);
   - outbound-only to tainted → `possible_victim_contact`;
   - provider OUTBOUND exposure → `possible_victim_contact` finding,
     never `KindExposure` (A1); unknown-direction provider item stays
     eligible;
   - candidate direct-tainted inbound → `proceeds_receiver`;
     chain-tainted inbound alone → NO proceeds_receiver;
   - service-classified source (EXCHANGE label) → never a source;
     service-classified CANDIDATE → CandidateServiceClassified=true
     even with include_labels=false (A4);
   - coverage partial on: no index row / indexing-only / partial state /
     stale (FreshTTL) / scan limit / source cap / depth-2 cap /
     receiver-unchecked; complete ONLY on fresh non-stale
     non-truncated usable row + no bound hit;
   - self-transfer + pending + is_error rows never counted;
   - v1.1 off → analyzer produces nothing;
   - analyzer query error → degraded leg, no fabricated output.
2. Red: `service_test.go` — v1.1 run persists findings incl.
   `onchain_computed` basis; `scoring.policy_version=="v1.1.0"`;
   exposure block item fields populated; v1 response has NO new keys
   (envelope parity, A5); absent index → `onchain_coverage:"absent"` +
   PARTIAL status.
3. Green: implement analyzer + wiring.
4. Refactor, gofmt, build, vet, tests green.

## Success Criteria

- [ ] Every contract §4 edge case has a named test that fails-then-passes
- [ ] ≤6 round trips per screening proven by fake-store call counting
- [ ] `coverage` never reports complete when index state absent/partial
      OR source receiver-checks unverifiable (A2)
- [ ] Provider `OUTBOUND` items never reach `KindExposure` scoring (A1)
- [ ] `disputed` labels never taint; findings carry best-evidence
      verification status, never inflated (A6/A7)
- [ ] `proceeds_receiver` emitted only on the candidate's own screening
      and only for DIRECT-tainted inbound
- [ ] Provider non-address counterparties unchanged (v1 generic path)
- [ ] Candidate service gate works with `include_labels=false` (A4)
- [ ] All new `ExposureItem`/`Coverage` fields `,omitempty`; v1
      response bytes gain zero new keys (A5)

## Risk Assessment

- High-degree wallet → scan LIMIT → honest partial; deterministic
  ordering keeps results reproducible across runs.
- Depth-creep bug (receiver-of-receiver tainting) → single-hop receiver
  check only; test asserts D∉taint(S) cannot taint C via B via C.
- Taint set vs scored-set drift → `directTaintCategoriesV1_1` is a named
  constant tested against the proposal list verbatim.
- Analyzer latency inside screening deadline → ≤6 bounded round trips
  (enumerated above); each inherits the pipeline ctx deadline.
- `to_wallet_id`/`from_wallet_id` NULL semantics → `IS DISTINCT FROM`
  keeps NULL-from rows visible but sourceless; tests cover.
