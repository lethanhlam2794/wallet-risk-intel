# TRON Indexer Semantics — decisions for the dormant TronGrid adapter

Status: **decided at prep-time, §15-gated for enablement** — every rule
below is implemented by `internal/provider/trongrid/` (dormant) or
recorded as an enablement blocker in §(f). Evidence: file:line in-repo
references plus live keyless probes of `https://api.trongrid.io`,
2026-11-06 (probe outputs transcribed in
`plans/20261106-1946-tron-enablement-prep/reports/` and quoted inline).

Scope: what a `provider.TxProvider` adapter for TRON must do so the
existing indexer (`internal/transaction/service.go:179-566`) computes
honest coverage watermarks and `transactions` rows carry real evidence.
Nothing here activates the network — `blockchain_networks` stays
`is_active=FALSE` (migration `000026`).

## (a) transfer_index identity — reuse the EVM scheme verbatim

Current scheme (`internal/transaction/identity.go:41-53,197-210`):
`{n|t}:{contentHash16}.{k}` — `n`/`t` from `provider.TransferKind*`,
contentHash = 16-hex SHA-256 prefix over `kind|asset|from|to|amount`,
`.k` = max-per-stream occurrence count within the tx, unique under
`UNIQUE(network_id, tx_hash, transfer_index)` (`000008:33`).

**Decision: identical scheme, no deviation.** It is content-derived and
chain-agnostic; determinism across refetches holds because the hash
inputs are transfer semantics, not positions.

TRON field mapping into the hash:

| Hash field | TRON source | Form |
|---|---|---|
| `kind` | `n` (NATIVE) / `t` (TOKEN) | existing constants |
| `asset` | `token_info.address` of the trc20 row | base58, verbatim; `""` for NATIVE |
| `from`/`to` | trc20 `from`/`to` verbatim; native `parameter.value.owner_address`/`to_address` (hex `41…`) converted in-adapter | **base58** (see (d)) |
| `amount` | trc20 `value`; native `parameter.value.amount` (sun) | raw integer decimal string — never float (NUMERIC fidelity, `000008` `amount_raw NUMERIC(78,0)`) |
| `tx_hash` | `txID` / `transaction_id` | 64-hex, lowercased |

- Stream names for per-stream occurrence counting: **`trx`** (native)
  and **`trc20`** (token). `Transfer.Stream` feeds both identity dedup
  and per-stream coverage — see INV-2 below.
- Same-tx duplicate TRC-20 events (identical `kind|asset|from|to|
  amount`, same stream) derive `.k` = occurrence count — order-
  insensitive, replay-safe by construction. Pinned by a Phase-5 fixture.
- Determinism + refetch-stability + uniqueness under the existing
  UNIQUE constraint all hold: the hash inputs are upstream-stable
  fields; the `.k` suffix is a pure function of the page content.

## (b) The tri-problem — decided separately

TRC-20 rows carry **no block number** (`transaction_id`, `from`, `to`,
`token_info{symbol,address,decimals,name}`, `value`, `block_timestamp`,
`type` — probed 2026-11-06). That single gap splits into three
independent problems, decided below.

### (b1) Window bridge — input side, BOTH streams

The indexer hands the adapter `FromBlock`/`ToBlock` block numbers
(`provider.go:247-249`); TronGrid account endpoints filter by
`min_timestamp`/`max_timestamp` in **milliseconds**.

**Decision: bridge via `walletsolidity/getblockbynum`** — one call per
boundary on the first step only; the resolved `[minTs, maxTs]` window
rides inside the page token (fingerprint continuation needs identical
filters anyway, and re-bridging every step would repay 2 calls/step).

- `FromBlock ≤ 0` → `minTs = 0`, **no bridge call** (verified
  `getblockbynum(0)` returns the genesis block with `raw_data`
  **eliding** `number`/`timestamp` — proto3 defaults omitted; a bridge
  parse would fail on absent fields, and ts=0 is equivalent to an
  unbounded low edge).
- `FromBlock > 0` → `minTs = block_header.raw_data.timestamp` of that
  block. `ToBlock` → `maxTs` likewise (always bridged — `fetchTo` is a
  real head or capped window).
- Inclusivity — **probed inclusive on both ends** on both streams:
  `min_timestamp == max_timestamp == <row ts>` returned exactly the
  boundary row (`/transactions` row at ts 1555401072000; `/trc20` row at
  ts 1559473476000). So `[ts(From), ts(To)]` ⊇ txs in blocks
  `[From, To]`; over-inclusion from timestamp collisions is dedup-safe
  (UNIQUE + content-hash identity). No widening needed — exclusivity
  was the failure mode to guard against and it does not occur.
- Guard `FromBlock ≤ ToBlock` → `ErrInternal` if violated (the
  indexer's `head < fetchFrom` lag path `service.go:398-408` already
  prevents this; the guard is defense-in-depth).
- Bridge calls are real upstream calls → `UpstreamCalls` (+2 on the
  first step, +0 after) and they consume the request budget.
- `getblockbynum` past head → HTTP 200 with `{}` body (probed). Absent
  `block_header` ⇒ `ErrDegraded` — never fabricate a timestamp.
- **Monotonicity assumption (stated, not proven):** TRON block
  timestamps are non-decreasing in block number (~3s slot quantized;
  skipped slots mean absent numbers, not out-of-order timestamps).
  Consequence if violated: a block whose timestamp lies outside
  `[minTs, maxTs]` silently drops out of the claimed window → coverage
  lie. Mitigations: inclusive bounds (verified), the watermark never
  claims beyond a stream's own observed max block−1 (unfinished) or
  `fetchTo` (finished — under the assumption). Accepted as an enablement
  assumption; no cheap counter-probe exists keyless.

### (b2) TRC-20 per-row block-number resolution — stored evidence

`txRowJSON.BlockNumber` is non-pointer `int64`
(`internal/transaction/identity.go:32`): an absent block persists
`block_number=0` + `confirmation_status='pending'` — fabricated evidence
in a risk-evidence store — while `transactions.block_number` IS nullable
(`migrations/000008:22`). Emitting block-less rows is a **named
non-option** (see non-options).

**Decision: Option ii — anchor map + bounded fan-out fallback.**

- *Anchor map (free):* the native `/transactions` stream indexes txs
  where the account is a TRC-20 *recipient*, not just the sender —
  probed: tx `db5b080124dc…` (USDT Transfer `THzz…→TLU7…`) appears in
  `TLU7…`'s native stream as `TriggerSmartContract` with
  `blockNumber` present. So the same txid→blockNumber needed by a trc20
  row is often already on the native page fetched in the same step.
  The adapter builds `txid → blockNumber` from that step's native rows
  and resolves trc20 rows against it first.
- *Fan-out fallback (bounded):* each remaining unresolved unique
  `transaction_id` → `walletsolidity/gettransactioninfobyid`
  (`{value: txid}` → `blockNumber` + `receipt.result` — probed; same
  call yields `IsError` evidence, see (e)). Dedup within the step
  (multi-event txs resolve once). The trc20 upstream page is clamped
  to **50 rows/step** (`limit=50`) regardless of `opts.PageSize`, so a
  step's fan-out ≤ 50 calls — the stated bound that keeps the post-hoc
  `settle` budget honest (`service.go:339-344`).
- *txinfo indexing lag:* the txinfo index can lag the event index; a
  bounded in-step retry (≤3 attempts, same resilience client) then
  `ErrDegraded` — the honest outcome. Never emit the row block-less.
- Every fan-out call counts in `UpstreamCalls`; worst case/step =
  2 pages + 50 txinfo (+2 bridge on step 1) = 54 — folded into the
  calls-per-ensure model in `docs/tron-provider-decision.md`.

*Named non-options (recorded, rejected):*

1. **Emitting block-less trc20 rows** — fabricates `block_number=0` +
   `'pending'` (non-pointer `txRowJSON.BlockNumber`, `identity.go:32`).
   Honest-NULL would need a `txRowJSON.BlockNumber → *int64` contract
   change — an enablement decision, not this scope.
2. **Dropping unresolvable rows while advancing the fingerprint** —
   silent coverage lie; violates INV-1.
3. **Timestamp-arithmetic block estimation** — skipped slots make
   ts→block non-invertible; fabricated evidence.
4. **Block-range scanning** (`getblockbynum` per candidate block) —
   unbounded calls per step; violates the budget model.
5. **`StreamStatus.CoveredThrough` contract extension** — would fix
   coverage signaling for a block-less stream but NOT stored evidence;
   recorded as a possible future contract decision — NOT this scope.
6. **Defer-trc20 (Option iii)** — emit only `trx`, declare `trc20`
   permanently `Finished=false` → honest perpetual PARTIAL +
   history_truncated, zero fan-out. *Kept as the documented fallback:*
   if txinfo bridging proves unreliable at enablement, this is the only
   no-resolution honest path — switchable by dropping the trc20 stream
   fetch while still declaring it in `page.Streams` (INV-2 still binds:
   the stream must be declared either way).

### (b3) Binding invariants — test-asserted in Phase 5

- **INV-1 cursor-advance:** a stream's fingerprint may only advance
  past fully-resolved rows. Partial trc20 resolution inside a step ⇒
  typed `ErrDegraded` and the whole step returns no page — the
  continuation token is never emitted, so the cursor is held. Never
  drop-and-advance.
- **INV-2 stream-declaration:** every `Transfer.Stream` value emitted
  in a step MUST appear in that step's `page.Streams`. Verified failure
  mode: `streamSeen` is populated only from `page.Streams`
  (`service.go:444-449`) — an undeclared stream never gates coverage,
  so `fetchTo` would write a watermark over incomplete data =
  permanent silent data loss.
- **INV-3 (documented, enablement-side):** `streamMaxBlock` is computed
  on raw transfers **before** `buildRows` normalization
  (`service.go:450-457` vs `:479`) — if tron rows ever reach a
  still-EVM `buildRows` (`identity.go:88,93,104`), they skip as
  `InvalidAddress` yet coverage advances. Recorded in §(f); the
  identity.go normalize switch is load-bearing, not cosmetic.

## (c) Finality — solidified only

- `HeadBlock` = `walletsolidity/getnowblock` →
  `block_header.raw_data.number` (probed: 87765540 @ ts 1793970309000).
  **Never** `wallet/getnowblock` (unconfirmed head — non-final rows
  would enter evidence).
- All upstream reads stay on the `walletsolidity/` view for consistency
  with `only_confirmed=true` on both account streams: `getnowblock`,
  `getblockbynum`, `gettransactioninfobyid`.
- `Confirmations` = `opts.HeadBlock − blockNumber + 1` (0 when unknown
  → `confirmation_status='pending'` per `identity.go:188-191`).
- **`ReorgTail` rationale — index lag, not reorg.** TRON's DPoS
  solidification means solidified blocks do not reorg; the tail exists
  because the trc20 event index and the txinfo index lag solidification
  (a freshly solidified tx may be invisible to both for seconds).
  Recommended `ReorgTail["tron"]` ≈ **10 blocks** (~30 s at 3 s/block —
  enablement config decision). Cost asymmetry: every tail-refetched
  trc20 row repays the (b2) fan-out — keep the tail small.

## (d) Address form — DECIDED: base58-canonical emission

`NormalizeTron` accepts base58check only (`address_tron.go:22-40`,
returns input verbatim); native-stream hex `41…` payloads would error →
100% `InvalidAddress` skip.

- Adapter converts upstream hex (`41` + 20-byte payload, e.g.
  `parameter.value.owner_address`) → base58check before emitting;
  asserts the `0x41` version byte — a non-`41` payload is `ErrMalformed`
  evidence of an upstream contract break, never silently passed through.
- trc20 `from`/`to`/`token_info.address` arrive base58 → verbatim.
- Deviation-from-verbatim (vs the `Transfer` doc "upstream
  representations verbatim", `provider.go:223`) is deliberate and
  recorded: provider wire quirks live inside the owning adapter
  (AGENTS.md §7); expanding `NormalizeTron` to dual-form would change
  frozen groundwork for one provider's wire format.

## (e) Kinds mapping

| `Transfer.Type` | Upstream source | Filter | Evidence |
|---|---|---|---|
| NATIVE | `/transactions` rows where **any** `raw_data.contract[i].type == "TransferContract"` | iterate the array — multi-contract txs exist; `[0]`-only would miss them | `ret[]` is parallel to `contract[]` |
| TOKEN | `/transactions/trc20` rows where `type == "Transfer"` | `Approval` etc. excluded — `type` values `{'Transfer','Approval'}` observed on 200 rows live | Approval moves no value |

- Native row fields: `txID`→`TxHash` (lowercase);
  `parameter.value.amount`→`AmountRaw` (sun, raw integer decimal —
  e.g. 1 TRX = `1000000`); `parameter.value.owner_address`/`to_address`
  hex→base58; `blockNumber`→`BlockNumber`;
  `block_timestamp`→`BlockTimestamp` (ms→time).
- TRC-20 fields: `transaction_id`→`TxHash`, `token_info.address`→`Asset`
  (base58 verbatim), `value`→`AmountRaw`, `block_timestamp`→
  `BlockTimestamp`, resolved `blockNumber` (b2)→`BlockNumber`.
- **Excluded scope (documented):** non-`TransferContract` native types
  (TriggerSmartContract calls, votes, freezes — not value transfers);
  `internal_transactions`/`internalTransactions` TRX moves inside
  contract execution (out of MVP scope, chainscan `INTERNAL` parity);
  TRC-20 `type != "Transfer"` rows.
- **Zero-amount / zero-address policy (explicit):** native
  `TransferContract` rows with `amount ≤ 0` are scope-excluded (not
  value transfers — chainscan `keepTxlist` parity); TRC-20 `value=0`
  rows are **kept** (a contract-emitted Transfer event is evidence);
  zero-address endpoints (`T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb`,
  `0x41`+20 zero bytes — mint/burn) are **kept** — valid base58check
  that `NormalizeTron` accepts. No unstated exclusion.
- **`IsError`:** native — `ret[i].contractRet != "SUCCESS"` on **any**
  ret element parallel to the contract array ⇒ `true`. TRC-20 — when
  the row was fan-out resolved, `receipt.result != "SUCCESS"` ⇒ `true`
  (same call, probed shape `{receipt:{result:"SUCCESS"}}`); when
  anchor-resolved, `false` — justified: a REVERTED/out-of-gas tx
  persists no logs, so an indexed event is itself success evidence
  (conservative default, documented).
- Empty `Kinds` = both streams — chainscan convention
  (`adapter.go:236-247`); the indexer always sends both
  (`service.go:432`).

## (f) Enablement blockers — out of scope now, required at §15 enablement

| Blocker | Location | Why it blocks |
|---|---|---|
| `buildRows`/`identity.go` `NormalizeEVM` → `wallet.Normalize(net,…)` switch | `internal/transaction/identity.go:88,93,104` | load-bearing per INV-3 — tron rows skip as InvalidAddress while coverage advances |
| `queries/transactions.sql:30` `'evm'` literal → format-aware stamping | `BulkResolveWallets` | stamps `'evm'` for tron wallet ids |
| `internal/transaction/list.go:91` `NormalizeEVM` asset filter | list path | tron asset filter normalization (RA-7) |
| `data_sources` row for `trongrid` + providerconfig descriptor + selector wiring + `TX_PROVIDER_TRON` env surface | `cmd/api|worker`, `internal/providerconfig` | adapter currently importable but registered nowhere (dormant by design) |
| Remaining `NormalizeEVM` upstream sites | `walletattribution/mapper.go:30`, `stablefreeze/scanner.go:284`, `ingestion/pipeline_freeze.go:176`, `ofac/mapper.go:124`, `warmindex/queue.go:46-47` | EVM-only upstreams; per-adapter revisit at enablement |
| `wattrFiles` / `tron.csv` artifact | `cmd/api/main.go:651-658`, `cmd/worker/main.go:285-292` | resolver-listed networks map `code+".csv"` — a tron artifact or different mapping needed before activation |
| OpenAPI `Address` pattern | `api/openapi.yaml` | `^(0x)?[0-9a-fA-F]{40}$` is EVM-only |
| OFAC TRX currency mapping | `ofac/mapper.go:32-44` | stays UNRESOLVED per spec §5.6 — define at enablement |
| `is_active` flip on `tron` row | migration `000026` | the §15 gate itself |
| `wallets.address_type` | `000001:20` `VARCHAR(30)` no CHECK | `'tron'` is schema-legal today — Phase-4 service change only, no DDL |

## Calls-per-ensure reconciliation

Under (b1)+(b2) as decided: worst case = `1 head + 2 bridge + 5×(2 pages
+ ≤50 txinfo)` = **263 calls** per hop-0 ensure (`MaxPagesHop0=5`,
`service.go:49-57`); anchor hits reduce the fan-out in practice.
Reconciled against TronGrid free-key quota (100K/day ⇒ ~380 worst-case
ensures/day) in `docs/tron-provider-decision.md` — fits; keyless 3-RPS
shared quota is dev-only.
