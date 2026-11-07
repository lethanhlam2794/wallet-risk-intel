# Phase 1 — Audit Drift Check (Solana enablement-prep)

Date: 2026-11-07 · Executor: Devin Cook · Scope: verify every Solana claim
in `docs/non-evm-audit.md`, `docs/non-evm-provider-discovery.md`, and the
phase-01 claim list against current HEAD post-TRON-prep; adjudicate
VERIFIED/DRIFTED with file:line evidence.

**Result: all 13 claims VERIFIED — zero drift.** Landmines and
enablement-blocker surface re-confirmed below; nothing in Phases 2–4
inputs needs correction.

## Claim-by-claim verification

| # | Claim | Verdict | Evidence |
|---|-------|---------|----------|
| 1 | `GetOrCreate` stamps `net.AddressFormat()`, fails closed on unknown namespace | VERIFIED | `internal/wallet/service.go:29-39` — `format := net.AddressFormat()` `:30`, `format == ""` → `ErrUnknownAddressFormat` `:31-33` (var at `:15`), `AddressType: string(format)` `:38`. Callers pass `net network.Network` (screening `service.go:146`, enrichment `service.go:117`, transaction `service.go:182`, httpserver `server.go:270`) |
| 2 | OFAC `SOL` + solana-format → `CHAIN_SPECIFIC`, unmapped network | VERIFIED | `internal/provider/ofac/mapper.go:32-44` — `resolveListing` returns `("", ScopeChainSpecific)` for any non-EVM-format listed address `:33-35`; test row `{"SOL", "9WzD…AWWM", "", "CHAIN_SPECIFIC"}` `mapper_test.go:29`. Note nuance: the branch keys on address FORMAT, not currency code — a `SOL` currency code on an EVM-format address still hits `("", UNRESOLVED)` `:42`. `normalizeListed` is EVM-only `:123-128` |
| 3 | `identity.go` chain-agnosticism: `contentHash` content-derived; `buildRows` EVM-only | VERIFIED | `contentHash` `identity.go:41-45` = SHA-256 over `kind\|asset\|from\|to\|amount` → 16-hex prefix — pure string math, reusable verbatim on base58 input. `wallet.NormalizeEVM` call sites: `:88` (From), `:93` (To), `:104` (Asset) — all inside `buildRows`, all unconditional |
| 4 | warmindex EVM boundary | VERIFIED | `queue.go:47` — `NormalizeEVM(addr); err \|\| n != addr ⇒ reject` (canonical gate). `seed.go:195-203` — net gate `:195-199` incl. `AddressFormatForNamespace(r.ns.String) != network.AddressFormatEVM` `:197` + `IsEVMAddress` `:201`; `NormalizeEVM` reachable only post-gate `:209` |
| 5 | `wallet.Normalize` dispatch has a solana branch | VERIFIED | `internal/wallet/address.go:54-65` — `case AddressFormatSolana → NormalizeSolana` `:58-59`; unknown → `ErrInvalidAddress` `:62-64`. `NormalizeSolana` itself `address_solana.go:10-22` — base58, 32–44 chars → exactly 32 bytes, returns input verbatim (no case folding, comment `:7-9`) |
| 6 | `000026` `is_active=FALSE`; DOWN FK-safe; `is_active` predicates intact | VERIFIED | `migrations/000026_non_evm_networks.up.sql:6-9` — solana (`5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp`) + tron both `FALSE`. `down.sql:7-8` deletes only `is_active = FALSE` rows — an activated row survives rollback. Predicates: `labels_staging.sql:23,48`, `labels_publish.sql:18,63,104` — 5 `bn.is_active` joins total, all present |
| 7 | TxProvider contract shape + ascending-order + `UpstreamCalls` | VERIFIED | `provider.go` — `TransferLookupOpts{FromBlock,ToBlock,Kinds,PageToken,PageSize,HeadBlock}` `:247-254`; `StreamStatus` `:258-261`; ordering contract `:269-271` ("within one stream, transfers must be emitted in ascending block order — the indexer's contiguous-coverage watermark (max-block-minus-one per stream) depends on it"); `UpstreamCalls` doc `:265-267` ("the real number of outbound requests"); `TxProvider` iface `:282-287` (contract block `:242-288` incl. `Transfer` `:228-240`) |
| 8 | Indexer mechanics the adapter must satisfy | VERIFIED | `MaxPagesHop0=5` `service.go:49-51` (HopN=2 `:52-54`, Hop3=1 `:55-57`; `MaxPagesFor` `:147-156`). `prepay`/`settle` post-hoc `:333-344` — `settle(calls)` charges `calls−1` AFTER the step → fan-out can overshoot Budget; per-step bounds are the adapter's own clamp. `head < fetchFrom` lag `:398-408` (no transfer call, claim released). `streamSeen`/`streamMaxBlock`/`finishedStreams` `:416-418`, populated `:444-457`. Watermark `:490-523` — `newWatermark = fetchTo` then per-unfinished-stream `cov = streamMaxBlock−1` (or `fetchFrom−1`) min-fold `:516-519`. `history_truncated` write: `truncated := status == StatusPartial` `:657` + `:669`; FAILED preserves prior `:658-659`. `Kinds: {NATIVE, TOKEN}` always sent `:432` |
| 9 | Discovery-doc Solana rows still UNCONFIRMED; `solana.csv` never requested | VERIFIED | `docs/non-evm-provider-discovery.md:6-8` — "Solana rows remain UNCONFIRMED"; Helius row `:37` quota col "UNCONFIRMED". `wattrFiles` iterates `resolver.List()` `cmd/api/main.go:653-659`, `cmd/worker/main.go:327-335`; resolver loads `ListActiveNetworks` only `resolver.go:63-64` ← `queries/networks.sql:1-2` `WHERE is_active = TRUE` ⇒ `solana.csv` can never be requested |
| 10 | `x/time/rate` + `resilience` toolkit available | VERIFIED | `internal/provider/resilience/{breaker,client,limiter,retry}.go` + tests exist; `golang.org/x/time/rate` imported `client.go:19`, `limiter.go:7` (module dep already in `go.mod` via trongrid) |
| 11 | **`identity.go:116` `txHash := strings.ToLower(tr.TxHash)` landmine** | VERIFIED | Line `:116` confirmed verbatim, inside the `buildRows` per-transfer loop — runs for EVERY provider row after amount validation `:111-115`, before `contentHash`/grouping `:125-126`. Case-folding a case-sensitive base58 signature changes its decoded bytes → stored `tx_hash` unresolvable upstream AND divergent identity vs the pre-lowered input. Worse than a skip: silent corruption. **Enablement blocker — documented, NOT fixed in this scope** |
| 12 | NULL-`block_timestamp` read-path unreachability | VERIFIED | `transactions.sql:64-67` keyset `(t.block_timestamp, t.id) < (cursor_ts, cursor_id)` + `ORDER BY t.block_timestamp DESC, t.id DESC` `:67`. Postgres `DESC` sorts NULLs first; `NULL < x` is never TRUE → a NULL-ts row may appear on page 1 but can never satisfy a non-NULL cursor → unreachable past page 1. Compounder: `list.go:116` `encodeCursor(last.BlockTimestamp.Time, …)` marshals a zero `time.Time` for a NULL row → next cursor is year-1. Read-path consequence of no-fabricated-`blockTime`: real, documented for the semantics doc |
| 13 | `Transfer.Confirmations` slot semantics | VERIFIED | `identity.go:188-191` — `conf = "confirmed"` iff `g.t.Confirmations > 0` else `"pending"`. Provider doc `provider.go:244-246` defines `Confirmations = head − blockNum + 1` (0/unknown → pending). On Solana `head − slot + 1` is slot distance, NOT confirmation depth (skipped slots inflate it); finalized-only reads mean every emitted row is already final. What the adapter may honestly emit is a Phase-3 (c) decision |

## Confirmed enablement-blocker surface (Solana §f-equivalent, NOT this scope)

Re-verified for the Phase-3 blockers table:

- `identity.go:88,93,104` `NormalizeEVM` → must switch to
  `wallet.Normalize(net, …)` at enablement (INV-3 analogue: solana rows
  skip as `InvalidAddress` while coverage advances).
- **`identity.go:116` `ToLower` on `TxHash`** — corrupts base58 sigs
  (claim 11).
- `queries/transactions.sql:30` `'evm'` literal (`BulkResolveWallets`
  stamps `address_type='evm'` for solana endpoint wallets) + comment
  `:25-27`.
- `internal/transaction/list.go:91` asset filter `NormalizeEVM`.
- `data_sources` row, providerconfig descriptor, selector wiring,
  `TX_PROVIDER_SOLANA` env surface — none exist yet (dormant by absence).
- `solana.csv` / `wattrFiles` — resolver-gated, never requested.
- OpenAPI `parameters.Address` `pattern: "^(0x)?[0-9a-fA-F]{40}$"` —
  EVM-only (`api/openapi.yaml`).
- OFAC `SOL` → solana network mapping — currently `("", CHAIN_SPECIFIC)`
  per spec §5.6; correct until a solana `network_id` is evidenced.
- warmindex gates `queue.go:47`, `seed.go:195-203` — EVM-only, base58
  must never enter.
- `is_active` flip + `transactions`/`wallet_index_state` DDL — §15-gated.
- `block_number`/`indexed_until_block` slot semantics — documented, not
  DDL-changed.
- `'solana'` `address_type` is schema-legal today (`wallets.address_type
  VARCHAR(30)`, no CHECK — `000001_init.up.sql`).

## Drift found

None. Every cited line number matched current code within ±2 lines; the
post-TRON-prep tree is exactly as the audit doc describes.

## New findings (not in the 13 claims — recorded for Phase 3)

- `encodeCursor` on a NULL-`block_timestamp` row emits a year-1 cursor
  (`list.go:116`) — the unreachability is a cursor-composition issue, not
  just a sort-order issue (claim 12 nuance).
- `Transfer.TxHash` doc says "TxHash is lowercase" (`provider.go:223`) —
  written for hex providers; on Solana the verbatim base58 signature is
  the honest value and the doc + `identity.go:116` both need an
  enablement-time decision (Phase-3 (d) input).
- `head < fetchFrom` lag path (`service.go:398-408`) releases the claim
  and serves persisted facts — the analogous Solana condition is
  `head < floor` or `fetchTo < floor` (node ledger below the window);
  maps to the Phase-2 ledger-floor probe.

No source files modified in this phase.
