Devin Review — Round 2 re-audit. Cook claims all 19 round-1 findings remediated (test-first), plus ~15 new tests covering the normative acceptance values and the previously-absent service-level v1.1 tests. `reports/final-verification.md` now has a finding→resolution map + A/B divergence evidence.

Re-verify each finding against the NEW diff (files moved since your audit — re-read, don't trust line numbers). Known remediation claims to check:

- #1: provider-source gate now `MaxHops>=1 && (Hops==0 || Hops<=MaxHops)`; `Hops<=2` bound removed; `TestV11_ProviderUnknownHopsAdjacency` added.
- #2: service-classified source/victim skip removed when also direct-tainted or sanctioned; `TestAnalyze_ServiceClassifiedStillTainted` covers HACK+MIXER and sanctioned-exchange.
- #3: index-state query error → `partial`, never `absent`; `TestAnalyze_IndexStatesErrorIsPartial`.
- #4: acceptance suite added — engine 6tx→+65/2×1tx→+60/3src→70/50tx→cap/dedupe≠51/MaxHops=0; analyzer coverage partials; mapper OUTBOUND→possible_victim_contact; service-level persisted v1.1.0 + absent→PARTIAL + v1 envelope parity + A/B divergence.
- Minors #5–#12 and nits #13–#19: strict micro_usd>0, resolver-aware network validation at wiring, OUTBOUND can't join groups, per-source codes preserved, normalized direction vocabulary + openapi fixes, SANCTIONED competes on weight, deterministic ordering, onchain leg counts toward Usable, dead field removed, shared exported constants, NULL indexed_at unusable, assetless token unpriced, `unassessable_amount` reason, overlay no longer mutates global table.
- One documented deviation: `freshTTL==0` = "no staleness bound" (timestamped fresh row usable) — assess whether acceptable.

Gates (orchestrator independently re-ran on remediated tree): `sqlc generate`=0, `gofmt -l .`=empty, `go build ./...`=0, `go vet ./...`=0, `go test -count=1` scoring+screening+config all ok.

Verdict protocol unchanged:
```
Status: APPROVED | CHANGES_REQUESTED
Summary: <per-finding verification result>
Findings: <remaining items with severity + citations — or "none">
```
