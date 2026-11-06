# Phase 1 — Audit Drift Check

Date: 2026-11-06 · Executor: Devin Cook · Scope: verify every file:line
claim in `docs/non-evm-audit.md` against current HEAD; produce the
`GetOrCreate` call-site inventory (Phase-4 contract) and the adapter
contract-assumption list (Phase-3/5 input).

**Result: all audit claims VERIFIED — zero drift.** New findings and
contract assumptions the audit did not enumerate are listed separately.

## Claim-by-claim verification

| # | Audit claim | Verdict | Evidence |
|---|---|---|---|
| 1 | TxProvider contract is block-window shaped + ascending order | VERIFIED | `internal/provider/provider.go` — `TransferLookupOpts.FromBlock/ToBlock` `:247-249`, ordering contract comment `:269-271` ("within one stream, transfers must be emitted in ascending block order"), `TxProvider` interface `:282-288` (audit said `:242-288` — the contract block spans `:242-288` incl. `Transfer` `:228-240` and `StreamStatus` `:258-261`; interface proper starts `:282`) |
| 2 | `GetOrCreate` stamps `"evm"` via `$4` | VERIFIED | `internal/wallet/service.go:20-27` — `AddressType: "evm"` at `:25`; `queries/wallets.sql:1-6` `$4` param |
| 3 | `'evm'` literals ×4 | VERIFIED | `queries/transactions.sql:30` (BulkResolveWallets), `queries/ofac_publish.sql:57` (PublishEnsureWallets), `queries/labels_publish.sql:12` (PublishEnsureLabelWallets), `queries/stablecoin_freeze.sql:61` (PublishFreezeEnsureWallets) |
| 4 | `wallets.sql` param-driven (not a literal site) | VERIFIED | `queries/wallets.sql:1-6` — `VALUES ($1,$2,$3,$4)` |
| 5 | buildRows `NormalizeEVM` (EVM-only upstream) | VERIFIED | `internal/transaction/identity.go:88,93,104` — `From`, `To`, `Asset` all `wallet.NormalizeEVM` |
| 6 | watermark = min per-stream contiguous coverage | VERIFIED | `internal/transaction/service.go:490-524` — `streamSeen` from `page.Streams` only `:444-449`; `streamMaxBlock` on raw `Transfer.BlockNumber` `:450-457`; per-stream `cov = mb-1` / `fetchFrom-1` min-fold `:511-523` |
| 7 | warmindex stored-address EVM check | VERIFIED | `internal/warmindex/queue.go:46-47` — `NormalizeEVM(addr); n != addr ⇒ reject` |
| 8 | `wattrFiles` `code+".csv"` maps | VERIFIED | `cmd/api/main.go:651-658`, `cmd/worker/main.go:285-292` — identical bodies over `resolver.List()` |
| 9 | OFAC TRX listing UNRESOLVED | VERIFIED | `internal/provider/ofac/mapper.go:32-44` — non-EVM format → `("", ScopeChainSpecific)` `:33-34`; EVM-format non-ETH/BSC/BNB currency → `("", ScopeUnresolved)` `:41-42`; `normalizeListed` EVM-only `:123-128` (`:124` NormalizeEVM) |
| 10 | Remaining `NormalizeEVM` sites (audit :108-118) | VERIFIED | `internal/transaction/list.go:91` (asset filter), `internal/provider/walletattribution/mapper.go:30`, `internal/provider/stablefreeze/scanner.go:284`, `internal/ingestion/pipeline_freeze.go:176`, `internal/provider/ofac/mapper.go:124` — full grep confirms these plus `identity.go:88,93,104` and `warmindex/queue.go:47` are the ONLY non-test `NormalizeEVM` callers |
| 11 | `Normalize`/`ResolveAndNormalize` dispatch | VERIFIED | `internal/wallet/address.go:54-65` — `AddressFormatEVM/Solana/Tron` dispatch, default → `ErrInvalidAddress` `:62-64`; `ResolveAndNormalize` `:71-81` |
| 12 | migration `000008` columns + UNIQUE | VERIFIED | `migrations/000008_transactions.up.sql` — `tx_hash VARCHAR(255)` `:12`, `transfer_index VARCHAR(100)` `:13`, `block_number BIGINT` (nullable) `:22`, `UNIQUE(network_id, tx_hash, transfer_index)` `:33` |
| 13 | `000019` source_key rekey | VERIFIED | `migrations/000019_wallet_index_state_source_key.up.sql` — deletes NULL-source rows, drops old PK, `source_id SET NOT NULL`, new `PRIMARY KEY (network_id, wallet_id, source_id)` |
| 14 | `000026` inactive rows | VERIFIED | `migrations/000026_non_evm_networks.up.sql` — solana (`5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp`) + tron (`0x2b6653dc`) both `is_active=FALSE` |
| 15 | 4th `GetOrCreate` caller missed in audit | VERIFIED (new) | `internal/httpserver/server.go:270` — `svc.GetOrCreate(ctx, net.ID, c.Param("address"), normalized)` in `walletHandler`; `net` in scope from `ResolveAndNormalize` `:260` |
| 16 | `wallet_index_state.indexed_until_block` semantics | VERIFIED | `migrations/000008:55` + comment `:42-46` — contiguous-coverage watermark |
| 17 | `wallets.address_type VARCHAR(30)` no CHECK | VERIFIED | `migrations/000001_init.up.sql:20` — `NOT NULL DEFAULT 'unknown'`; `'tron'` is schema-legal today |
| 18 | `txRowJSON.BlockNumber` non-pointer `int64` | VERIFIED | `internal/transaction/identity.go:32` — absent block marshals `0`; `transactions.block_number` IS nullable (`000008:22`) — the b2 evidence-fabrication gap is real |
| 19 | `network.Network` has no `IsActive` field | VERIFIED (new) | `internal/network/resolver.go:12-18` — fields: `ID, Namespace, ChainReference, Code, Name` only. Activeness gating is the resolver's (`LoadResolver` reads `ListActiveNetworks` `:61-70`); adapters cannot check it |
| 20 | `NormalizeTron` = base58check-only, verbatim | VERIFIED | `internal/wallet/address_tron.go:22-40` — `0x41`+20B+dbl-SHA256 checksum, returns input verbatim; hex `41…` upstream payloads would 100% fail |
| 21 | `len(chain)==0` → `writeDisabled` dormant shape | VERIFIED | `internal/transaction/service.go:296-300` — all-disabled terminal writes `status='disabled'` + `error='disabled'` under chain[0]'s source (`:608-639`); srcID==0 → verdict without row `:611-620` |
| 22 | Post-hoc `settle` budget | VERIFIED | `internal/transaction/service.go:333-344` — `prepay` takes 1/step; `settle(calls)` charges `calls−1` AFTER the step → a large fan-out step can overshoot `Budget`; the trc20 page-size clamp is the bound |
| 23 | Page-tier knobs | VERIFIED | `service.go` Config `:28-30` `MaxPagesHop0/HopN/Hop3`; defaults `:49-57` = 5/2/1; `MaxPagesFor` `:147-156` (frontier 2→HopN, ≥3→Hop3) |
| 24 | Indexer always sends both Kinds | VERIFIED | `service.go:432` — `Kinds: {NATIVE, TOKEN}` on every `LookupTransfers` call |
| 25 | `head < fetchFrom` lag path | VERIFIED | `service.go:398-408` — no transfer call, claim released, `head_block` on wire evidences lag |
| 26 | Failover-eligible kinds | VERIFIED | `service.go:64-70` — rate_limited/upstream_5xx/degraded/transport/circuit_open |
| 27 | Keyless-vs-keyed `Enabled` precedents | VERIFIED | chainscan `adapter.go:150-152` `apiKey != "" && chains[code]`; nodereal `adapter.go:144-145` same shape; blockscout `adapter.go:182-184` keyless `chains[code] && enabled[code]` |

## `GetOrCreate` call-site inventory (Phase-4 contract)

Current signature: `GetOrCreate(ctx, networkID int64, address, normalized string) (db.Wallet, error)` — `wallet/service.go:20`.

| # | Site | Shape | `net` in scope? |
|---|---|---|---|
| P1 | `internal/screening/service.go:146` | `s.d.Wallets.GetOrCreate(ctx, net.ID, raw, req.Address)` via `WalletStore` iface `:54-57` | yes (`net network.Network` param) |
| P2 | `internal/enrichment/service.go:117` | `s.wallets.GetOrCreate(ctx, net.ID, rawAddress, normalized)` | yes |
| P3 | `internal/transaction/service.go:182` | `s.wallets.GetOrCreate(ctx, net.ID, normalized, normalized)` | yes |
| P4 | `internal/httpserver/server.go:270` | `svc.GetOrCreate(c.Request.Context(), net.ID, c.Param("address"), normalized)` | yes |
| I1 | `internal/screening/service.go:54-57` | `WalletStore` interface signature | — |
| T1 | `internal/transaction/failover_test.go:283` | `GetOrCreate(ctx, net.ID, addrRoot, addrRoot)` — `net` from `newSvcChain` (`network.Network`) | yes |
| T2 | `failover_test.go:353` | same shape | yes |
| T3 | `failover_test.go:394` | same shape | yes |
| T4 | `failover_test.go:421` | same shape (`w2`) | yes |
| T5 | `internal/transaction/service_test.go:254` | `net.ID` — `net` from `newSvc`/`testNet` (`network.Network`) | yes |
| T6 | `service_test.go:356` | same | yes |
| T7 | `service_test.go:469` | same | yes |
| T8 | `internal/transaction/killswitch_test.go:185` | `net.ID` — `net` from `newSvcChain` | yes |
| T9 | `internal/screening/service_test.go:505` | `testNet().ID` — `testNet()` returns `network.Network` `:33-37` | yes |

Direct `q.GetOrCreateWallet` callers pass `AddressType` explicitly —
unchanged (none found outside `wallet.Service`).

## Adapter contract assumptions (indexer-verified, for Phases 3/5)

Traced `EnsureIndexed → ensure → attemptFetch → LookupTransfers →
buildRows → watermark` (`service.go:179-566`). The TRON adapter MUST:

1. **Gate before traffic** — `Enabled(net)=false` ⇒ `HeadBlock`/
   `LookupTransfers` return `ErrDisabled` pre-HTTP (chainscan
   `adapter.go:156-157,225-227`). Activeness is NOT checkable — `Network`
   has no `IsActive` (`resolver.go:12-18`); resolver gates it upstream.
2. **Head = finalized only** — `HeadBlock` feeds `fetchTo` and
   `Confirmations`; unconfirmed head would index non-final rows.
3. **Emit ascending block order per stream** — watermark is
   max-block−1 per stream (`service.go:450-457,516-519`).
4. **Declare every emitted stream in `page.Streams` each step** —
   `streamSeen` comes only from `page.Streams` (`:444-449`); an
   undeclared stream never gates coverage → silent data loss (INV-2).
5. **Never emit block-less rows** — `streamMaxBlock` derives only from
   `Transfer.BlockNumber`; a block-less row contributes nothing yet its
   stream could still read as covered; `txRowJSON.BlockNumber` is
   non-pointer → `0`+`'pending'` fabrication if it slips through (INV-1
   complement; b2/b3).
6. **Report `Finished` honestly** — finished ⇒ coverage claims
   `fetchTo`; unfinished ⇒ `ownMax−1`. A permanently-unfinished stream
   ⇒ perpetual PARTIAL + history_truncated (honest, not a bug).
7. **Count every outbound request in `UpstreamCalls`** — bridge
   (`getblockbynum`) and fan-out (`gettransactioninfobyid`) calls
   included; `settle` is post-hoc (`:339-344`) so per-step call count is
   the only budget bound — clamp the trc20 page size.
8. **`NextPageToken` "" = all streams done**; opaque, must carry enough
   state to resume identical filters (resolved ms window — RA-4).
9. **Transfer field contract** — `TxHash` lowercase; `Stream` non-empty
   per stream name; `Type` ∈ {NATIVE,TOKEN}; `Asset` "" for NATIVE;
   `AmountRaw` raw integer decimal (NUMERIC fidelity, never float);
   `Confirmations` = `opts.HeadBlock − blockNumber + 1` (0 ⇒ pending).
10. **Respect `Kinds`** — empty = both streams (chainscan
    `adapter.go:236-247`); kind-filtered stream reports `Finished=true`.
11. **Typed errors** — `provider.LookupError` kinds drive failover
    (`service.go:64-70`) and persisted `provider_status`; malformed ⇒
    `ErrMalformed`, upstream error ⇒ `ErrDegraded`/`ErrClient4xx`.
12. **`buildRows` is EVM-only** (`identity.go:88,93,104`) — tron rows
    reaching it skip as `InvalidAddress` while coverage still advances
    (INV-3 ordering: `streamMaxBlock` computed pre-`buildRows`,
    `:450-457` vs `:479`). Enablement blocker, not this adapter's scope.

## Enablement-blocker deltas confirmed

- `internal/transaction/list.go:91` asset filter `NormalizeEVM(f.Asset)`
  — critic RA-7; was missing from the audit's blocker prose (present in
  the :108-118 list). Recorded for the semantics doc blocker section.
- `identity.go` `NormalizeEVM` → `wallet.Normalize(net,…)` switch is
  load-bearing per INV-3 — enablement blocker.

No source files modified in this phase.
