# Final Report — TRON Enablement Prep (R3)

Date: 2026-11-06. Plan: `plans/20261106-1946-tron-enablement-prep/plan.md`.
End state achieved: **TRON is code-complete but still INACTIVE** — a
verified free provider is chosen, semantics are documented for the §15
reviewer, `address_type` stamping is format-derived, and a dormant,
fully-tested adapter exists that cannot emit traffic.

## Phase evidence

### Phase 1 — Audit drift check → `reports/audit-drift-check.md`
All 27 claims in `docs/non-evm-audit.md` VERIFIED against HEAD (zero
drift), each with file:line evidence. Traced the contract
`EnsureIndexed → ensure → attemptFetch → LookupTransfers → buildRows →
UpsertTransactions → watermark` and recorded the 12 adapter assumptions
(ascending per-stream order, `Streams` declaration, `Finished`,
`UpstreamCalls`, head semantics, nullable block handling). Full
`GetOrCreate` inventory: 4 production callers (incl. `httpserver/
server.go:270`, missed by the original audit), 9+ test sites,
`screening.WalletStore` interface.

### Phase 2 — Provider evaluation → `docs/tron-provider-decision.md`
TronGrid **chosen** — passes all six requirements on a free no-card tier:
native + TRC-20 per-address history (keyless-probed), deterministic asc
ordering + `meta.fingerprint`/`links.next` cursor, solidified head
(`walletsolidity/getnowblock`), opaque-token pagination (`limit≤200`,
201→400; both timestamp bounds probed inclusive), free key ≈15 RPS /
100K-day, terms compatible. Rejected: Tatum (100K *lifetime* credits ≈
76 worst-case ensures), Ankr (Advanced API has no TRON), Covalent/
GoldRush (no TRON chain). Keyless access is dev-only → adapter requires
an API key. Worst-case calls-per-ensure: 1 head + 2 bridge + 5×(2 pages
+ ≤50 txinfo) = **263** ⇒ ~380 worst-case ensures/day on free quota.

### Phase 3 — Indexer semantics → `docs/tron-indexer-semantics.md`
- **(a)** `{n|t}:{contentHash16}.{k}` reused verbatim; hash inputs are
  base58 `from`/`to`, TRC-20 contract `asset`, raw integer `amount`.
- **(b1)** `FromBlock`/`ToBlock` → ms window via `getblockbynum`, carried
  inside the page token (0 re-bridge on continuation); inclusive bounds
  probed; `FromBlock≤0` skips its call (genesis elides fields); missing
  `block_header` → `ErrDegraded`. Monotonicity assumption stated.
- **(b2)** TRC-20 block resolution: **anchor map** (native stream indexes
  TRC-20 recipient txs as `TriggerSmartContract` with `blockNumber` —
  live-probed) + **bounded txinfo fan-out** (50-row page clamp = the
  bound, dedup, ≤3 retries) — never block-less. All non-options named.
- **(b3)** INV-1 (cursor never advances past unresolved rows) + INV-2
  (every emitted stream declared) binding; INV-3 (normalize-before-
  coverage ordering) documented as enablement blocker.
- **(c)** Solidified-only finality; `ReorgTail≈10` justified by index
  lag, not reorgs.
- **(d)** Base58-canonical emission (deviation from verbatim justified:
  provider quirks live in the adapter).
- **(e)** Kinds mapping, `contract[]` iteration, Approval/internal-tx
  exclusions, zero-amount/zero-address policy, `contractRet`/
  `receipt.result` `IsError` rule.
- **(f)** Complete enablement-blocker table incl. `list.go:91` and the
  `streamMaxBlock`-before-`buildRows` ordering note.

### Phase 4 — `address_type` generalization
`GetOrCreate(ctx, net network.Network, address, normalized string)`
stamps `string(net.AddressFormat())`; `""` → typed
`ErrUnknownAddressFormat` before any write. All call sites updated:
`screening/service.go:146` (+`WalletStore` iface), `enrichment:117`,
`transaction/service.go:182`, `httpserver/server.go:270`, 10 test sites.
Four `'evm'` SQL literals got leading `--` comment blocks (no sqlc
regen). EVM paths unchanged — idempotent-refetch test proves
byte-identical `"evm"`.

### Phase 5 — Dormant adapter `internal/provider/trongrid/`
`provider.TxProvider` implementation (compile assertion present), shared
`resilience.Client`, `TRON-PRO-API-KEY` header, `init()` allowlist —
imported by nothing outside the package (verified). Key-required +
chain-gated `Enabled()`; disabled ⇒ `ErrDisabled` pre-HTTP. Token-
carried ms window; per-step ≤1 page/stream; `trx`+`trc20` streams;
anchor map + txinfo fan-out (≤50/step, deduped, ≤3 retries →
`ErrDegraded`); `checkAscending` upstream-contract guard; exact
`UpstreamCalls` accounting incl. bridge + fan-out.

## Acceptance checklist (verified against real test output)

| Criterion | Evidence |
|---|---|
| tron rows `is_active=FALSE`; `/networks` = bsc+eth only | `checkwallet/tests` PASS 66.4s (`non_evm_networks_test.go`) |
| Zero-traffic adapter test | `TestDisabled_ZeroTraffic` — 0 requests counted |
| Refetch determinism | `TestLookup_ReplayDeterminism` — identical sets |
| Ascending + per-stream Finished | `TestLookup_Ascending`, `…OrderViolationIsDegraded`, `TwoStreams_AnchorResolved` |
| GetOrCreate tron/evm stamping | `TestGetOrCreate_AddressTypeByFormat`, `…UnknownFormatFailsClosed` (real `check_wallet_wallet_test` DB) |
| Zero UNCONFIRMED TRON rows in discovery doc | verified by grep — all TRON rows VERIFIED/REJECTED |
| No new external dep; no secrets | `go.mod`/`go.sum` untouched; new code uses stdlib + pre-existing `golang.org/x/time` (already a dep via resilience) |
| Gates | `gofmt -l .` empty; `vet`/`build`/`test ./...` clean |
| INV-1/INV-2 | `TestLookup_INV1_UnresolvedTrc20` (ErrDegraded, no page/token), `TestLookup_INV2_StreamsDeclared` |
| UpstreamCalls exact | asserted per-test (4, 1, 5, 3) incl. bridge+fan-out |

## Drift findings

Zero audit drift. One audit **gap** found and corrected:
`internal/httpserver/server.go:270` was an unlisted `GetOrCreate`
caller — now updated and recorded in `docs/non-evm-audit.md`.

## Remaining §15 items (unchanged boundary)

`is_active` flip; `data_sources` row + providerconfig descriptor +
selector wiring + `TX_PROVIDER_TRON` env; `identity.go`/`list.go`/
`transactions.sql` normalize+literal switches; `wattrFiles`/`tron.csv`;
OpenAPI `Address` pattern; OFAC TRX mapping; warmindex check. Solana:
everything (untouched, out of scope).
