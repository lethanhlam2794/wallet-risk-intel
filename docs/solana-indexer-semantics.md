# Solana Indexer Semantics — decisions for the dormant public-RPC adapter

Status: **decided at prep-time, §15-gated for enablement** — every rule
below is implemented by `internal/provider/solanarpc/` (dormant) or
recorded as an enablement blocker in §(f)-equivalent. Evidence: file:line
in-repo references plus live keyless probes of
`https://api.mainnet-beta.solana.com`, 2026-11-07 (probe outputs
transcribed in
`plans/20261107-0042-solana-enablement-prep/reports/probe-solana-rpc.md`
and cited below as probe §N).

Scope: what a `provider.TxProvider` adapter for Solana must do so the
existing indexer (`internal/transaction/service.go:179-566`) computes
honest coverage watermarks and `transactions` rows carry real evidence.
Nothing here activates the network — `blockchain_networks` stays
`is_active=FALSE` (migration `000026`).

## (a) slot→`block_number` mapping + intra-slot order

- `transactions.block_number` stores **SLOT** — documented
  reinterpretation, no DDL change. Slot ≠ block ≠ wall-clock: ~5% of
  slots are skipped (never produce a block) and `blockHeight` is a
  secondary, nullable counter (probed `getBlock(200000000)` →
  `blockHeight:182664994`; `getBlock(1000)` → `blockHeight:null` — probe
  §2). `block_number` and `indexed_until_block` are both slot-space.
- **Intra-slot order = `transactionIndex`** — the
  `getSignaturesForAddress` row field is present on every probed row
  (0/1000+ NULL — probe §3) and is the canonical intra-block position:
  the feed is globally desc by `(slot, transactionIndex)` — 1000 rows
  over 5 slots on the token-program feed, every multi-tx slot strictly
  desc, zero violations (probe §3).
- **Emission order = ascending `(slot, transactionIndex, signature)`** —
  the adapter sorts the collected sig set ascending; `transactionIndex`
  orders within a slot, `signature` is the deterministic tiebreak for
  NULL/absent index rows. Ascending block order is the contract's
  invariant (`provider.go:269-271`); intra-slot sequencing only needs to
  be deterministic for replay stability, and canonical order is the
  honest choice when the field exists.

## (b) signature-desc pagination → ascending `[FromBlock, ToBlock]` bridge

`getSignaturesForAddress` paginates **desc by `(slot,transactionIndex)`**
(`before`/`until` cursors, both EXCLUSIVE — probe §4; ≤1000/page,
`limit>1000` → `-32602`) while the contract wants ascending emission —
the core adapter problem.

### Binding invariant (Critic R1 — non-negotiable)

**The emitted set per stream MUST be a contiguous ascending prefix of
the requested window.** `service.go`'s `cov = streamMaxBlock − 1`
watermark (`:511-523`) is only sound under bottom-anchored emission: a
top-anchored slice `(frontier, T]` over-claims `[F, frontier]` forever.
Therefore **no row is emitted until desc collection has passed below
`FromBlock` (or the ledger floor)** — the whole window's signature set
is known before emission; emission is then ascending slices.

### Decided mechanism — collection phase then emission phase, state in token

The page token carries `{window[F,T], phase, frontier sig (desc cursor),
topSeed, collected sig set (bounded), emitPos}`. It lives only in-process
(`attemptFetch` locals) — never persisted — so its size is a memory
concern, not a wire/schema one; it is still hard-bounded.

- **Collection phase (steps 1..k):** each step desc-collects ≤
  `maxSigPagesPerStep` pages from `before=frontier`. Zero emission while
  `phase=collecting` (a mid-collection step emits **zero rows** —
  required by the invariant and asserted in Phase 4). Collection ends
  when the frontier passes below `max(FromBlock, ledgerFloor)` — the
  *observable* window bottom (sigs below the floor cannot exist, so
  `max` is the correct bound; `min` would demand collecting below an
  unreachable floor) — or the feed bottoms out (short/empty page).
- **Top seed when `fetchTo < head`:** `before` accepts **foreign sigs
  as global positional seeds** — probed: a sig at slot 342.77M not on the
  queried address's feed anchored collection just below it (probe §4).
  The adapter seeds from `getBlock(S, transactionDetails:"signatures")`
  taking the FIRST signature of the first produced slot `S > ToBlock`
  (covers all of slot `ToBlock`; `before` is exclusive), walking skipped
  slots ≤ `maxSeedWalk` calls. Skipped slots and soft failures
  (degraded/rate-limited/unavailable) continue the walk and degrade to
  a head-scan; `internal`/`malformed` classes propagate — silently
  retrying a permanent fault would hide an adapter bug. When
  `fetchTo == head` no seed is needed — collection starts at the feed
  top. `getBlockSignatures` is disabled on the public endpoint
  (`-32601`, probe §8) — `getBlock` signatures-only is the seed source.
- **Emission phase:** the collected set is sorted ascending
  `(slot, transactionIndex, signature)`; each step emits ≤ `emitCap`
  rows from `emitPos`, fanning out `getTransaction` per signature
  (JSON-RPC batches ≤ `batchSize=50` — probe §6). Cross-step order is
  globally ascending because `emitPos` only advances (Phase-4 test).
- **Both streams share the one collection** — `getSignaturesForAddress`
  covers every tx touching the owner; `sol`/`spl` differ only in
  extraction (§e/§g), so one desc walk serves both streams.

### Pinned bounds (named constants, Phase 4)

| Constant | Value | Guards |
|---|---|---|
| `sigPageLimit` | 1000 | upstream max page size |
| `maxSigPagesPerStep` | 10 | desc collection work per step |
| `emitCap` | 100 | sigs fetched per step ⇒ ≤100 `getTransaction` logical calls; emitted transfer *rows* can exceed it (a tx emits ≥0 rows) |
| `batchSize` | 50 | logical calls per JSON-RPC batch HTTP request |
| `maxCollectSigs` | 20000 | sigs held in token across steps (~1.7 MB worst) |
| `maxSeedWalk` | 5 | skipped-slot walk for the `before` seed |

- **Window exceeding `maxCollectSigs` before passing below
  `FromBlock`** → collection cannot honestly bottom out → **zero
  emission, stream `Finished=false`, token ends** ⇒ `partial` +
  `history_truncated`. The degenerate >1000-sigs-in-one-slot case is the
  same class — bounded zero-emission steps, never a top-anchored leak.
- **Feed drift:** finalized desc order is stable below newly-prepended
  sigs; `before=frontier` anchors positionally between steps. A
  delayed/backfilled sig inside the emitted prefix is a residual the
  UNIQUE+content-hash identity and the next ensure's tail-refetch absorb
  (finalized commitment makes it rare).
- **`Kinds` filter:** `Kinds={NATIVE}` still fetches SPL sigs via
  `getTransaction` — the sig's kind is unknowable pre-fetch (the feed is
  address-level); extraction honors `Kinds` at mapping time, and a
  kind-filtered stream declares `Finished=true` when not requested
  (chainscan parity — INV-S2 still binds: the stream is declared either
  way).
- **`PageSize`:** accepted for contract compatibility and ignored —
  the emission slice is `emitCap`-bounded instead.
- **Failed-step accounting:** a step that errors returns no page, so
  its real upstream calls aren't settled into `UpstreamCalls`
  (`service.go:441-442` settles non-nil pages only) — the budget
  undercounts on failures; bounded by `MaxPagesHop0` regardless.

## (c) finality — `finalized` commitment only

- `HeadBlock` = `getSlot{commitment:"finalized"}` (probe §1 → 466200957).
  All reads pin `commitment:"finalized"`. `confirmed` CAN fork
  (`processed` is rejected outright on history endpoints — `-32602`,
  probe §8); `confirmed` probed returning all-`finalized` rows but is
  never relied on.
- **`ReorgTail["solana"]` ≈ 32 slots** (~13 s) — justified as **index
  lag, not reorg**: finalized slots do not reorg; the signature index
  may lag the finalized slot head by a few slots, so a small tail keeps
  the window inside the consistent region. Cost is negligible (tail
  refetch = the tail's sigs only).
- `fetchTo` may be a skipped slot (no block) — harmless: collection is
  signature-space, not slot-enumeration.

## (d) `transfer_index` identity — reuse `{n|t}:{hash16}.{k}` verbatim

`identity.go:41-45` `contentHash` is chain-agnostic string math
(`kind|asset|from|to|amount` → 16-hex SHA-256). **Identical scheme, no
deviation** — determinism, refetch-stability and
`UNIQUE(network_id, tx_hash, transfer_index)` hold on base58 input
because the hash inputs are transfer semantics, never the tx-hash
alphabet. `.k` = max-per-stream occurrence count (identical transfers in
one tx → occurrence count — e.g., same-amount spl fan-outs in aggregator
txs, observed in probe §5).

- `tx_hash` = the base58 signature **verbatim** (~88 chars, fits
  `VARCHAR(255)`). **`identity.go:116` `strings.ToLower(tr.TxHash)`
  corrupts case-sensitive base58** — documented enablement blocker,
  NOT this adapter's scope (its rows can't reach `buildRows` until the
  normalize+lowercase switch lands at §15).
- SPL `from`/`to` semantics — **owner-attributed via `tokenBalances`
  where evidenced, else the raw token account**: the parsed SPL
  instruction's `source`/`destination` are TOKEN ACCOUNTS (probe §5);
  `pre/postTokenBalances` carries `accountIndex → {mint, owner,
  programId}` (probe §5). Owner attribution makes the wallet's own
  history listable under `from_wallet_id OR to_wallet_id = W`
  (`transactions.sql:51`); emitting raw ATAs would strand the queried
  wallet's SPL rows under ATA identities. When an endpoint's owner is
  absent from both balance maps (unattributed account) the raw token
  account is emitted — evidence, never invention. Determinism holds:
  the balance maps are part of the tx's stored upstream evidence.
- `Transfer.Stream` ∈ `{sol, spl}` — per-stream occurrence counting and
  per-stream coverage both feed off it (INV-S2).

## (e) SPL extraction — parsed instructions are the authoritative source

**Decision: instruction-level parsed `transfer`/`transferChecked` —
outer `message.instructions` AND `meta.innerInstructions` (both carry
`stackHeight`/`index`), under Token (`TokenkegQf…`) AND Token-2022
(`TokenzQdB…`) programs.** `getTransaction{encoding:"jsonParsed",
maxSupportedTransactionVersion:0, commitment:"finalized"}` returns
parsed ixs `{parsed:{type, info}, program, programId}` — probed §5.

- `transferChecked` → `info.{source, destination, mint, tokenAmount.amount
  | amount}` — mint direct. `transfer` → `info.{source, destination,
  amount}` — NO mint field (probed §5); mint resolves via
  `pre/postTokenBalances[accountIndex(destination|source)].mint` —
  missing both ⇒ the row is skipped honestly, never invented.
- `tokenAmount.amount`/`amount` = u64 decimal string → `AmountRaw`
  verbatim (NUMERIC fidelity).
- **`meta.err != null` ⇒ tx excluded entirely** — failed txs roll back
  inner instructions; their (failed) rows never emit. `IsError` on
  emitted rows is therefore always `false` — documented, not a blind
  spot: only successful transfers are value-moving evidence.
- **Fee is never a transfer** — a failed or successful tx charges
  `meta.fee` lamports as an implicit balance change, not a transfer
  instruction → never emitted (no fee-only rows).
- **Noise policy:** only `system`-program `transfer`/`transferWithSeed`
  count as NATIVE (§g); ComputeBudget, vote, stake, ALT, ATA-create,
  initialize*, getAccountDataSize etc. are ignored — filtered, honestly
  (they are not value transfers between owners). ATA creation ixs
  (`spl-associated-token-account` `create`) move rent, not value —
  excluded.
- Zero-amount SPL transfers are kept (an emitted transfer instruction is
  evidence; `amount_raw=0` is honest data — chainscan `keepTokentx`
  parity).

## (f) archival truncation → `history_truncated` coverage channel

- **Floor signal = `getFirstAvailableBlock`** (1 call per ensure) —
  on mainnet-beta = **0** (archival; `getBlock(1000)` served — probe §2),
  on PublicNode ≈465.6M ≈ ~3 days retention (probe §9).
  `minimumLedgerSlot` is the local RocksDB floor (466.1M on
  mainnet-beta) and does NOT bound the sig feed (sigs at slot 342M
  returned fine) — it is a secondary signal only. **Feed exhaustion is
  the operational floor**: an empty page with `frontier ≥
  max(FromBlock, floor)` means the node bottomed out above the window.
- **Truncated composition:** floor (or feed bottom) above `FromBlock` ⇒
  `[F, floor)` unobservable ⇒ emit everything honestly collected, keep
  the stream **`Finished=false`**, end the token ⇒ `service.go` takes
  the default branch `:554-558` → `StatusPartial` +
  `history_truncated=true` (`:657,:669`); watermark = min-fold over
  `min(fetchTo, streamMaxBlock−1)` (`:511-523`) — the only bound the
  formula produces; `[F, floor)` is never claimed.
- **New-vs-truncated distinction:** `floor ≤ F` (mainnet-beta today) ⇒
  an empty feed is a genuinely inactive address → `Finished=true`
  honestly. `floor > F` with an empty feed is indistinguishable
  (new wallet vs purged history) ⇒ `Finished=false` — conservative per
  "never equate no data with safe".
- Composition verified against `service.go:493-559`: unfinished stream ⇒
  `cov = streamMaxBlock−1` (or `fetchFrom−1` with zero rows) min-folded
  into `newWatermark`; `token==""` + unfinished ⇒ `allFinished=false`
  ⇒ default branch ⇒ partial + truncated. No contract change needed.

## (g) Kinds mapping

| `Transfer.Type` | Upstream source | Fields |
|---|---|---|
| NATIVE | `system`-program parsed `transfer`/`transferWithSeed` ixs (outer+inner) | `TxHash`=sig verbatim; `From`=`info.source`; `To`=`info.destination`; `Asset`=`""`; `AmountRaw`=`info.lamports` (u64 decimal); `BlockNumber`=tx slot; `BlockTimestamp`=tx `blockTime` (absent ⇒ zero/`NULL` — §below); `IsError`=`false` |
| TOKEN | `spl-token`/`spl-token-2022` parsed `transfer`/`transferChecked` ixs (outer+inner) | `Asset`=mint (ix field or tokenBalances attribution, §e); `From`/`To`=owner-or-ATA per §d; `AmountRaw`=u64 decimal |

- **Excluded:** every other program/type; fee; vote/stake/budget noise
  (§e).
- **`BlockTimestamp` absent ⇒ zero `time.Time` ⇒ NULL persisted**
  (`identity.go:192-196`) — genesis-era finalized txs genuinely lack
  `blockTime` (probed §2: `getBlock(1000)` `blockTime:null`;
  `getTransaction` genesis sig `blockTime:null`). Never fabricate a
  timestamp. Read-path consequence: NULL-ts rows are unreachable past
  page 1 of `/transactions` (`transactions.sql:64-67` + `list.go:116`
  year-1 cursor composition) — an accepted, documented enablement
  blocker (§blockers), not an emission-time lie.
- **`Confirmations` = `opts.HeadBlock − slot + 1`** — slot DISTANCE,
  not confirmation depth (skipped slots inflate it); honest under
  finalized-only reads (every emitted row is final). `0` ⇒ `pending`
  (`identity.go:188-191`) when head is unknown.

## (h) SPL discovery coverage — declared blind spot (decision-determining)

**Proven gap (probe §7):** `getSignaturesForAddress(owner)` indexes txs
where `owner ∈ accountKeys`. An incoming SPL `transfer` deposits into
the wallet's ATA — accountKeys = `[sourceATA, destATA, mint?,
authority]` — the dest-ATA's **owner is absent** → the deposit is
unindexable on the owner feed (empirically: USDC deposit
`4UsiwZTYEBq…` slot 359509488 into Binance ATA `3hNzYSQs…`, owner
`9WzD…` not in keys). Per-ATA enumeration measured unbounded
(`getTokenAccountsByOwner` = 2834 accounts on that wallet) and closed
ATAs are unenumerable by construction.

**Decision — scoped coverage, declared honestly:**

- The `spl` stream's coverage contract is **"SPL transfers in txs where
  the queried owner is a tx account"** — a scope the feed serves
  completely, so `spl.Finished` is honest under it.
- The blind spot — SPL transfers landing on owner-controlled ATAs while
  the owner is absent from `accountKeys` — is **out of scope, declared**:
  recorded here, in `docs/solana-provider-decision.md` (req 1 verdict),
  and in the enablement blockers. It is a *coverage limitation*, never a
  silent gap: nothing is emitted for it and nothing is claimed.
- **Helius Enhanced Transactions / Parsed Events** is the upgrade path
  that closes it via provider-side owner indexing — a keyed free tier
  requiring user approval (stop condition); dormant path documented in
  the decision doc.

*Named non-options (rejected):*

1. **Per-ATA auxiliary feeds** — `getTokenAccountsByOwner` enumeration is
   unbounded (2834 accounts on one wallet) and cannot see closed ATAs —
   still-incomplete coverage at unbounded cost.
2. **`spl` perpetual-unfinished** (TRON defer-trc20 analogue) — honest
   but needlessly downgrades every wallet to eternal `partial` while the
   in-scope coverage is complete; rejected in favor of scoped coverage.
3. **Claiming full SPL coverage** — a silent coverage lie; never.
4. **tokenBalances-delta extraction** as the transfer source — deltas
   net same-owner movement, lose per-instruction endpoints and
   counterparties (aggregators); instruction-level parse is the
   authoritative evidence source (§e). Deltas remain a documented
   fallback if parsed ixs degrade.

## Binding invariants for the adapter (Phase-4 asserted)

- **INV-S1 contiguous-ascending-prefix:** no row emitted before desc
  collection passes below `max(FromBlock, ledgerFloor)` (the observable
  window bottom); the emitted sequence across steps is globally
  ascending `(slot,txIndex,sig)`. **Feed-order guard:** the feed is
  contractually non-increasing by slot — a row arriving above its
  predecessor (intra-page or across pages/anchors) is `ErrDegraded`,
  since a misordered page could mask in-window sigs behind a false
  bottom; rows after the bottoming row are still order-checked.
  **`getTransaction` slot cross-check:** a fetched tx whose `slot`
  disagrees with the feed row is index inconsistency → `ErrDegraded`.
- **INV-S2 stream-declaration:** every emitted `Transfer.Stream` value
  appears in that step's `page.Streams` (`streamSeen` derives only from
  it — `service.go:444-449`); an undeclared stream never gates coverage.
- **INV-S3 cursor-advance:** unresolvable mid-step ⇒ typed error, no
  token advance; the continuation token only ever represents committed
  honest state.
- **INV-S4 no fabricated evidence:** no slot/block_time invention
  (NULL timestamp when absent, never year-1-as-data); failed txs emit
  nothing; fee never emits; `IsError` only from real evidence (always
  `false` under §e).
- **INV-S5 bounded work:** per-step `maxSigPagesPerStep` + `emitCap` +
  `maxSeedWalk`; `UpstreamCalls` counts every real outbound HTTP
  request (a JSON-RPC batch = 1); token state bounded by
  `maxCollectSigs`.

## Enablement blockers — out of scope now, required at §15 enablement

| Blocker | Location | Why it blocks |
|---|---|---|
| `buildRows` `NormalizeEVM` → `wallet.Normalize(net,…)` | `internal/transaction/identity.go:88,93,104` | load-bearing INV-3 analogue — solana rows skip as InvalidAddress while coverage advances |
| **`identity.go:116` `strings.ToLower(tr.TxHash)`** | `internal/transaction/identity.go:116` | corrupts case-sensitive base58 sigs — stored tx_hash unresolvable; worse than a skip |
| `queries/transactions.sql:30` `'evm'` literal | `BulkResolveWallets` | stamps `address_type='evm'` for solana endpoint wallets |
| `internal/transaction/list.go:91` asset filter `NormalizeEVM` | list path | solana asset filter normalization |
| NULL-`block_timestamp` keyset unreachability | `queries/transactions.sql:64-67`, `list.go:116` | NULL-ts rows sort first but can't satisfy a non-NULL cursor; year-1 cursor emitted for NULL rows — read-path consequence of §g |
| `data_sources` row + providerconfig descriptor + selector wiring + `TX_PROVIDER_SOLANA` env surface | `cmd/api|worker`, `internal/providerconfig` | adapter importable but registered nowhere (dormant by design) |
| Remaining `NormalizeEVM` upstream sites | `walletattribution/mapper.go:30`, `stablefreeze/scanner.go:284`, `pipeline_freeze.go:176`, `ofac/mapper.go:124`, `warmindex/queue.go:47` | EVM-only upstreams; per-adapter revisit at enablement |
| `wattrFiles` / `solana.csv` artifact | `cmd/api/main.go:653-659`, `cmd/worker/main.go:327-335` | resolver-listed networks map `code+".csv"` |
| OpenAPI `Address` pattern | `api/openapi.yaml` | `^(0x)?[0-9a-fA-F]{40}$` is EVM-only |
| OFAC `SOL` currency mapping | `ofac/mapper.go:32-44` | stays `("", CHAIN_SPECIFIC)` per spec §5.6 — define solana `network_id` at enablement |
| warmindex EVM-only gates | `warmindex/queue.go:47`, `seed.go:195-203` | base58 must never enter the queue |
| `is_active` flip on `solana` row | migration `000026` | the §15 gate itself |
| `Transfer.TxHash` doc "lowercase" | `provider.go:223` | hex-shaped doc written for EVM providers; verbatim base58 is the honest value — doc vs behavior reconcile at enablement |
| `wallets.address_type` | `000001:20` `VARCHAR(30)` no CHECK | `'solana'` is schema-legal today — no DDL |

## Calls-per-ensure reconciliation

Worst case under `MaxPagesHop0=5` (`service.go:49-57`), pinned bounds
§b, floor probe §f:

```
1  head            getSlot@finalized
1  floor           getFirstAvailableBlock (first step)
≤5 seed            getBlock signatures (skipped-slot walk; only when
                   fetchTo < head, first step)
≤5×10 sig pages    getSignaturesForAddress desc collection
≤5×2 batch HTTP    getTransaction fan-out (≤100 logical calls/step,
                   batchSize=50 ⇒ 2 HTTP requests)
```

Worst case = `1 + 1 + 5 + 50 + 10` = **67 HTTP requests/ensure**
(`UpstreamCalls` — batch=1 request) plus ≤500 logical `getTransaction`
calls upstream. Documented public limits: 100 req/10s/IP total,
40/10s per method — the adapter limiter is **~2 req/s** (429 arrives as
a JSON-RPC `code:429` error body on HTTP 200, typed `ErrRateLimited`),
so ≤20 calls/10s stays far under both caps; a saturated worst-case
ensure paces to ~34 s. `Budget["solana-rpc"]` must tolerate ≤18 calls
in a single step (`settle` is post-hoc, `service.go:339-344`).
Reconciled in `docs/solana-provider-decision.md` — fits the keyless
quota with the adapter-side pacing constants above.
