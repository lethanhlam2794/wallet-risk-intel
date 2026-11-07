# Gate Evidence — Solana enablement-prep (Phase 5)

Run from `apps/api/` on 2026-11-07. Commands and verbatim outcomes.

## `gofmt -l .`

```
(empty output)
```

## `go vet ./...`

```
(clean — no diagnostics)
```

## `go build ./...`

```
(success — no output; BUILD_DONE marker reached)
```

## `go test ./...`

All packages pass. Solana-relevant excerpt:

```
ok  	checkwallet/internal/provider/solanarpc	(cached)
```

Full suite: every `ok` — no FAIL (run captured in this session's
transcript; solanarpc suite standalone run: `ok … 51.124s` incl. the
blind saturation suite).

## Dormancy verification

```
$ go list -deps ./cmd/api ./cmd/worker | grep -i solana
(no output — the solanarpc package is in no production binary's
 dependency tree)
```

```
$ grep -rn "solanarpc" apps/api --include='*.go' | grep -v internal/provider/solanarpc/
(no output — referenced nowhere outside its own package)
```

## Inactive-state verification

- `apps/api/migrations/000026_non_evm_networks.up.sql:7` — solana row
  still `is_active=FALSE` (unchanged, never touched).
- `apps/api/migrations/000026_non_evm_networks.down.sql:8` — delete
  guard `WHERE code IN ('solana','tron') AND is_active = FALSE` still
  applies.
- `wattrFiles` (`cmd/api/main.go:651-658`, `cmd/worker/main.go:327-335`)
  — unchanged; no `solana.csv` exists anywhere in the repo.
- `/networks` — resolver is active-only (`queries/networks.sql`
  `WHERE is_active`); solana still never resolves → response remains
  BSC + Ethereum only (unchanged behavior — no code path was modified).
- OpenAPI `Address` pattern — untouched
  (`^(0x)?[0-9a-fA-F]{40}$` still EVM-only).
- No DDL changed: `git status` shows zero modified files under
  `apps/api/migrations/` and `apps/api/queries/`.
- No selector/providerconfig/data_sources/cmd-default wiring: the only
  new Go files are the four under `internal/provider/solanarpc/`.
- warmindex — untouched (`git status`: no modifications).

## Test inventory (solanarpc)

`adapter_test.go` — named-fixture TDD:
`TestEnabled_Gates`, `TestDisabled_ZeroTraffic`,
`TestHeadBlock_Finalized`, `TestHeadBlock_Malformed`,
`TestLookup_DescFeedAscendingEmission`, `TestLookup_SameSlotIndexOrder`,
`TestLookup_MidCollectionZeroEmission`, `TestLookup_CrossStepGlobalAscending`,
`TestLookup_ReplayDeterminism`, `TestLookup_KindsFilter`,
`TestLookup_FloorAboveFromBlocksFinish`, `TestLookup_CollectCapZeroEmission`,
`TestLookup_FailedTxExcludedAndFeeNeverEmitted`, `TestLookup_SPLExtraction`,
`TestLookup_SPLBareTransferMintResolution`, `TestLookup_NullBlockTime`,
`TestLookup_Malformed`, `TestLookup_RPCErrorTyped`,
`TestLookup_NullTxResultNoAdvance`, `TestLookup_BatchUpstreamCalls`,
`TestLookup_BeforeSeedWhenToBelowHead`, `TestLookup_EmptyFeedFinished`,
`TestLookup_EmptyFeedAboveFloorUnfinished`, `TestLookup_InvertedWindow`.

`adapter_blind_test.go` — randomized/property suite:
`TestBlind_StreamInvariants` (6 trials — per-stream ascending, declared
streams, integer amounts, in-window bounds, exact emitted-count),
`TestBlind_PageTokenGarbage`, `TestBlind_MalformedNeverPanic`,
`TestBlind_DisabledNeverHitsUpstream`, `TestBlind_ReplayIdentity`.
