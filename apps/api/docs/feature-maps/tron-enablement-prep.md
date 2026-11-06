# Feature Map & Guidebook — TRON Enablement Prep (code-complete, INACTIVE)

**Scope**: TRON groundwork shipped under the §15 gate — a dormant TronGrid
`TxProvider` adapter, generalized `address_type` stamping, and the semantics
decisions a §15 reviewer needs. **TRON is NOT enabled**: registry row
`is_active=false`, adapter unwired (nothing imports it), no `data_sources`
row, no env surface. Nothing in this doc activates anything.

- Plan: `plans/20261106-1946-tron-enablement-prep/`
- Semantics decisions: `docs/tron-indexer-semantics.md`
- Provider decision: `docs/tron-provider-decision.md`
- Audit refresh: `docs/non-evm-audit.md`, `docs/non-evm-provider-discovery.md`

---

## 1. What was delivered

| Deliverable | Where | State |
|---|---|---|
| TronGrid `TxProvider` adapter (TRX + TRC-20 streams) | `internal/provider/trongrid/` | **dormant** — compiles, 35 tests green, zero production imports |
| `address_type` derived from `net.AddressFormat()` | `internal/wallet/service.go` | **live** — EVM byte-identical, `tron`/`solana` ready, unknown fails closed |
| `'evm'` SQL literals documented as EVM-only | `queries/{transactions,ofac_publish,labels_publish,stablecoin_freeze}.sql` | unchanged behavior, `--` comments added |
| TRON indexer semantics + provider decision docs | `docs/tron-*.md` | written for §15 review |

### Deliberately NOT done (§15 blockers, documented)

- No `is_active` flip; no migration; no `data_sources`/`providerconfig` row.
- No selector wiring — `trongrid` is imported by nothing outside its package.
- `identity.go` `NormalizeEVM` → per-network `wallet.Normalize` switch
  (INV-3 load-bearing) remains an enablement task.
- OFAC TRX listing mapper, `wattrFiles`/`tron.csv`, OpenAPI pattern —
  untouched by design.

## 2. Source paths

```
apps/api/internal/provider/trongrid/
  adapter.go              Adapter (TxProvider impl), gating, stream merge,
                          window bridge, txinfo fan-out, limit clamp
  mapper.go               acctTxRow/trc20Row decode → provider.Transfer,
                          hex→base58, ret[]/contract[] parallelism check
  adapter_test.go         28 contract tests (httptest double, no real net)
  adapter_blind_test.go   7 randomized property tests (BLIND_SEED env)

apps/api/internal/wallet/
  service.go              GetOrCreate(ctx, net, address, normalized)
  service_test.go         stamping tests incl. TestBlind_AddressTypeOnlyKnownFormats
```

Call sites migrated to the new signature (all 4 production + tests):
`internal/enrichment/service.go`, `internal/transaction/service.go`,
`internal/screening/service.go`, `internal/httpserver/server.go:270`.

## 3. Test IDs (Go test anchors — the `data-testid` equivalent)

| Behavior | Anchor |
|---|---|
| Zero wire traffic when disabled/inactive | `TestDisabled_ZeroTraffic`, `TestBlind_DisabledNeverHitsUpstream` |
| Wire `limit` ≤ 200 under `PageSize=1000` | `TestLookup_LimitClamped`, `TestBlind_LimitNeverExceedsCap` |
| Solidified-only head | `TestHeadBlock_Solidified` (+ `_Malformed`) |
| INV-1: unresolved trc20 ⇒ step fails, no page | `TestLookup_INV1_UnresolvedTrc20` |
| INV-2: every stream declared | `TestLookup_INV2_StreamsDeclared` |
| Anchor-map vs txinfo fan-out | `TestLookup_TwoStreams_AnchorResolved`, `TestLookup_TxInfoResolves` |
| Window rides the page token | `TestLookup_TokenCarriesWindow` |
| Ascending + order-violation ⇒ ErrDegraded | `TestLookup_Ascending`, `…OrderViolationIsDegraded`, `TestBlind_StreamInvariants` |
| Replay determinism (UNIQUE-key safe) | `TestLookup_ReplayDeterminism`, `TestBlind_ReplayIdentity` |
| `ret[]`/`contract[]` mismatch ⇒ ErrMalformed | `TestLookup_RetContractMismatch`, `TestBlind_RetParallelism` |
| `success:false` / txinfo `Error` ⇒ ErrDegraded | `TestLookup_SuccessFalseEnvelope`, `TestLookup_TxInfoErrorPath` |
| Multi-contract tx → N transfers | `TestLookup_MultiContractTx` |
| 429/5xx/malformed/host-reject/inverted | `TestLookup_429`, `…_5xx`, `…_MalformedJSON`, `…_UnallowlistedHost`, `…_InvertedWindow` |
| UpstreamCalls = real wire requests | `TestBlind_StreamInvariants` (asserted per trial) |
| `address_type` stamping (real DB) | `TestGetOrCreate_AddressTypeByFormat`, `…_UnknownFormatFailsClosed`, `TestBlind_AddressTypeOnlyKnownFormats` |

Blind-loop harness: `BLIND_SEED=<n> go test -count=1 ./internal/provider/trongrid/ ./internal/wallet/`
— 10/10 iterations required for the Stage 4 gate.

## 4. API contract evidence

The only touched endpoint surface is `GET /api/v1/wallets/:network/:address`
(response field `address_type`). Live verification captured in
`plans/20261106-1946-tron-enablement-prep/reports/api-verify-tron-enablement-prep.md`:

| Case | Result |
|---|---|
| `GET /api/v1/networks` | 200 — only `ethereum` + `bsc`; `tron`/`solana` absent |
| `GET /wallets/ethereum/0x…fee1` | 200 — `address_type:"evm"` (live `GetOrCreate`) |
| `GET /wallets/bsc/0x…fee1` | 200 — `address_type:"evm"` |
| `GET /wallets/tron/TR7N…` | 400 `INVALID_NETWORK` — resolver fail-closed before `GetOrCreate` |
| `GET /wallets/ethereum/notanaddress` | 400 `INVALID_ADDRESS` |
| same, no `X-API-Key` | 401 `UNAUTHORIZED` |

## 5. DB verification queries (executable)

```sql
-- Network registry state (TRON inactive — the invariant this task preserves)
SELECT code, namespace, chain_reference, is_active
FROM blockchain_networks WHERE code IN ('tron','solana','ethereum','bsc');
-- ethereum|eip155|1|t · bsc|eip155|56|t · solana|solana|-|f · tron|tron|0x2b6653dc|f

-- address_type stamping after a live wallet read
SELECT w.id, n.code, w.address, w.address_type
FROM wallets w JOIN blockchain_networks n ON n.id=w.network_id
WHERE w.normalized_address='<address>';
-- observed: evm (ethereum), evm (bsc) — byte-identical to pre-change

-- No trongrid data_sources row may exist yet (dormant invariant)
SELECT count(*) FROM data_sources WHERE code='trongrid';   -- expect 0
```

Stage-5 cleanup performed: `DELETE 2` probe wallet rows; `remaining=0`.

## 6. Reproduction checklist (any reviewer)

```bash
cd apps/api
gofmt -l .                       # empty
go vet ./... && go build ./...   # exit 0
go test -count=1 ./internal/provider/trongrid/ ./internal/wallet/
BLIND_SEED=1234 go test -count=1 ./internal/provider/trongrid/  # different shapes
grep -rln 'provider/trongrid' --include='*.go' . | grep -v trongrid/   # empty = dormant
```

## 7. Screenshots

Not applicable — backend/API feature with no UI surface. API evidence is the
curl matrix above (raw captures persisted in the plan reports dir).
